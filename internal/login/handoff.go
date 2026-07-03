package login

// SCM_RIGHTS handoff from wash-login to per-user wash-router.
//
// Per docs/MULTIUSER.md: wash-login validates the cookie + picks
// the target session, then hands the browser-facing TCP fd off to
// the per-user wash-router. The router takes over WS framing and
// runs the shell session; wash-login is not in the data path.
//
// In production with nginx in front: the hijacked fd is the
// nginx→wash-login TCP socket. After handoff, nginx faithfully
// proxies between the browser's WSS connection and the per-user
// router. wash-login holds zero state for the session.

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
)

// Handoff dials the per-user router's ctl socket and sends the
// browser-facing TCP fd (plus a replay payload — the original HTTP
// upgrade request bytes the router will re-parse) via SCM_RIGHTS.
//
// On return the hijacked net.Conn is closed; the kernel has dup'd
// the underlying fd into the per-user router during sendmsg, so
// closing wash-login's reference does not affect the live session.
//
// The replay payload is constructed from the parsed *http.Request
// plus any post-request bytes the bufio.Reader had buffered
// (typically zero for a well-behaved WS client). This lets the
// per-user router run http.ReadRequest + websocket.Accept normally,
// without coordination over the request's identity.
func Handoff(req *http.Request, conn net.Conn, brw *bufio.ReadWriter, ctlPath string) error {
	defer conn.Close()

	replay, err := buildReplay(req, brw)
	if err != nil {
		return fmt.Errorf("build replay: %w", err)
	}

	// Get the raw fd of the hijacked TCP conn. net.FileConn-style
	// SyscallConn lets us read the fd inside a Control closure
	// without race-y dup logic on our side; the kernel will dup it
	// into the router during Sendmsg.
	sysconn, ok := conn.(syscall.Conn)
	if !ok {
		return fmt.Errorf("hijacked conn is not a syscall.Conn (%T)", conn)
	}
	raw, err := sysconn.SyscallConn()
	if err != nil {
		return fmt.Errorf("hijacked SyscallConn: %w", err)
	}

	var ctlErr error
	if err := raw.Control(func(connFD uintptr) {
		ctlErr = sendFDToRouter(int(connFD), replay, ctlPath)
	}); err != nil {
		return fmt.Errorf("raw Control: %w", err)
	}
	return ctlErr
}

// ProxyHandoff is the handoff variant for a wash-login that terminates
// TLS itself (the default self-signed HTTPS listener). A *tls.Conn's
// plaintext lives inside wash-login's process — the encryption keys
// can't ride an SCM_RIGHTS fd — so the raw-fd Handoff can't be used.
//
// Instead we create a Unix socketpair, SCM_RIGHTS one end (plus the
// replayed upgrade request) into the per-user router — which reads it
// exactly as it would a browser TCP fd — and keep the other end here,
// pumping decrypted bytes between the browser's TLS conn and the router.
// wash-login stays in the data path only for this TLS case; the
// plaintext / nginx-front deployments keep the zero-copy fd handoff.
//
// Blocks until either side closes, so callers run it in the hijacked
// handler's own goroutine.
func ProxyHandoff(req *http.Request, conn net.Conn, brw *bufio.ReadWriter, ctlPath string) error {
	defer conn.Close()

	replay, err := buildReplay(req, brw)
	if err != nil {
		return fmt.Errorf("build replay: %w", err)
	}

	// AF_UNIX stream socketpair: fds[0] stays here, fds[1] goes to the
	// router. Both behave like a connected stream socket, so the router's
	// net.FileConn + http.ReadRequest path is unchanged.
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("socketpair: %w", err)
	}
	// Send the router's end, then close our copy of it (the kernel dup'd
	// it into the router during Sendmsg).
	if err := sendFDToRouter(fds[1], replay, ctlPath); err != nil {
		_ = syscall.Close(fds[0])
		_ = syscall.Close(fds[1])
		return err
	}
	_ = syscall.Close(fds[1])

	localFile := os.NewFile(uintptr(fds[0]), "ws-proxy")
	if localFile == nil {
		_ = syscall.Close(fds[0])
		return fmt.Errorf("os.NewFile for socketpair fd failed")
	}
	local, err := net.FileConn(localFile)
	_ = localFile.Close() // FileConn dup'd the fd
	if err != nil {
		return fmt.Errorf("FileConn socketpair: %w", err)
	}
	defer local.Close()

	// Pump both directions. When either finishes (browser hangup or the
	// session ending on the router side), close both conns so the other
	// copy unblocks, then wait for it. io.Copy errors are expected on the
	// closing side and not surfaced.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(local, conn); done <- struct{}{} }() // browser → router
	go func() { _, _ = io.Copy(conn, local); done <- struct{}{} }() // router → browser
	<-done
	_ = conn.Close()
	_ = local.Close()
	<-done
	return nil
}

// sendFDToRouter dials the per-user router's ctl socket and sends fd
// (a browser-facing stream socket) plus the replay payload — the
// original HTTP upgrade request bytes the router re-parses — via
// SCM_RIGHTS. The kernel dup's fd into the router during Sendmsg;
// the caller still owns its own copy and closes it.
func sendFDToRouter(fd int, replay []byte, ctlPath string) error {
	ctl, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: ctlPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("dial %s: %w", ctlPath, err)
	}
	defer ctl.Close()

	ctlSysconn, err := ctl.SyscallConn()
	if err != nil {
		return fmt.Errorf("ctl SyscallConn: %w", err)
	}

	oob := syscall.UnixRights(fd)
	var sendErr error
	if err := ctlSysconn.Control(func(ctlFD uintptr) {
		sendErr = syscall.Sendmsg(int(ctlFD), replay, oob, nil, 0)
	}); err != nil {
		return fmt.Errorf("ctl Control: %w", err)
	}
	if sendErr != nil {
		return fmt.Errorf("sendmsg: %w", sendErr)
	}
	return nil
}

// buildReplay synthesises the HTTP upgrade request bytes from req
// plus any unconsumed bytes left in brw.Reader. The per-user router
// reconstructs its read stream as (replay | live TCP) and runs
// http.ReadRequest on it.
//
// Header reconstruction uses Go's canonical Header form. The
// resulting bytes are semantically equivalent to the on-wire request
// (per RFC 7230), even if the casing or ordering differ slightly
// from what the browser sent.
func buildReplay(req *http.Request, brw *bufio.ReadWriter) ([]byte, error) {
	var sb strings.Builder
	// Request line. http.Request.URL was parsed relative to the
	// server's mux; for the ctl-side parse we want the original
	// path + query unmodified.
	target := req.URL.RequestURI()
	fmt.Fprintf(&sb, "%s %s HTTP/1.1\r\n", req.Method, target)

	// Host header isn't in req.Header — it lives in req.Host.
	// Re-emit explicitly so the synthesized request is RFC-valid.
	if req.Host != "" {
		fmt.Fprintf(&sb, "Host: %s\r\n", req.Host)
	}

	// All other headers. Skip Host (handled above) and any
	// hop-by-hop headers net/http might have stripped already.
	for k, vs := range req.Header {
		if strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			fmt.Fprintf(&sb, "%s: %s\r\n", k, v)
		}
	}
	sb.WriteString("\r\n")

	prefix := sb.String()

	// Any bytes the bufio.Reader had past the request (rare — only
	// happens if the client pipelined a first frame before reading
	// the 101). Drain them into the replay so they aren't lost.
	var leftover []byte
	if n := brw.Reader.Buffered(); n > 0 {
		buf := make([]byte, n)
		if _, err := io.ReadFull(brw.Reader, buf); err != nil {
			return nil, fmt.Errorf("drain bufio: %w", err)
		}
		leftover = buf
	}

	out := make([]byte, 0, len(prefix)+len(leftover))
	out = append(out, prefix...)
	out = append(out, leftover...)
	return out, nil
}
