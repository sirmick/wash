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

	// Open Unix conn to the per-user router's ctl socket.
	ctl, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: ctlPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("dial %s: %w", ctlPath, err)
	}
	defer ctl.Close()

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

	// Same dance for the ctl socket so we can call Sendmsg directly.
	ctlSysconn, err := ctl.SyscallConn()
	if err != nil {
		return fmt.Errorf("ctl SyscallConn: %w", err)
	}

	var sendErr error
	err = raw.Control(func(connFD uintptr) {
		oob := syscall.UnixRights(int(connFD))
		ctlErr := ctlSysconn.Control(func(ctlFD uintptr) {
			sendErr = syscall.Sendmsg(int(ctlFD), replay, oob, nil, 0)
		})
		if sendErr == nil && ctlErr != nil {
			sendErr = ctlErr
		}
	})
	if err != nil {
		return fmt.Errorf("raw Control: %w", err)
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
