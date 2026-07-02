package router

// Unix-socket ctl listener for multi-user mode (docs/MULTIUSER.md).
//
// In multi-user deployments the per-user wash-router doesn't bind TCP
// itself; instead it binds a Unix socket at cfg.ListenUnix and accepts
// SCM_RIGHTS handoffs from wash-login. Each accepted handoff carries
// the browser-facing TCP fd plus the buffered HTTP-upgrade request
// bytes that wash-login peeked but did not consume. The router runs
// the WS upgrade on the received fd itself and then handles the
// resulting WebSocket via the existing NewWSTransport + HandleShell
// path. wash-login is not in the data path post-handoff.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

// ensureXDGRuntimeDir creates dir 0700 if it doesn't exist, and pins a dir we
// created to 0700 (MkdirAll's mode is umask-masked). Called in the router's
// setuid (target-uid) startup so the per-user XDG_RUNTIME_DIR wash-login points
// us at (<runRoot>/<uid>/xdg) is owned by the user at 0700 — what libwayland
// requires for wash-display's wayland socket (REVIEW-X11-WAYLAND #4). Empty
// path is a no-op; an already-provisioned logind /run/user/<uid> is left as-is.
func ensureXDGRuntimeDir(dir string) error {
	if dir == "" {
		return nil
	}
	_, statErr := os.Stat(dir)
	created := os.IsNotExist(statErr)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if created {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("chmod %s: %w", dir, err)
		}
	}
	return nil
}

// MaxHandoffReplay caps the size of the replay-byte payload on the
// ctl socket. wash-login peeks the HTTP upgrade request (a few
// hundred bytes typically — a few cookies plus headers), so 16 KiB
// is generous.
const MaxHandoffReplay = 16 * 1024

// RunUnixListener binds cfg.ListenUnix as the SCM_RIGHTS handoff
// socket and serves it until ctx cancels or the listener fails.
//
// Each accepted handoff is run on its own goroutine; the goroutine
// receives one fd plus replay bytes, reconstructs the connection,
// performs the WS upgrade itself, and runs HandleShell. Errors are
// logged and the offending handoff is dropped; one bad handoff
// does not affect siblings.
func (r *Router) RunUnixListener(ctx context.Context) error {
	if r.cfg.ListenUnix == "" {
		return errors.New("ListenUnix not configured")
	}

	// Stale-socket cleanup. ENOENT is fine; other errors (busy,
	// permission) surface below at Listen.
	if err := os.Remove(r.cfg.ListenUnix); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("unlink %s: %w", r.cfg.ListenUnix, err)
	}
	if err := os.MkdirAll(filepath.Dir(r.cfg.ListenUnix), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(r.cfg.ListenUnix), err)
	}
	// Provision the per-user XDG_RUNTIME_DIR (wash-login points us at
	// <runRoot>/<uid>/xdg via childEnv). We run AS the target uid here, so it
	// lands owned by the user at 0700 — satisfying libwayland's same-uid 0700
	// requirement so wash-display's wayland socket binds, and isolating it per
	// user (REVIEW-X11-WAYLAND #4). Best-effort: the display layer is optional,
	// so a failure here must not stop the router serving terminals.
	if err := ensureXDGRuntimeDir(os.Getenv("XDG_RUNTIME_DIR")); err != nil {
		r.log("xdg runtime dir: %v", err)
	}

	addr := &net.UnixAddr{Name: r.cfg.ListenUnix, Net: "unix"}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		return fmt.Errorf("listen-unix %s: %w", r.cfg.ListenUnix, err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(r.cfg.ListenUnix)
	}()

	// Socket mode is 0660 so wash-login (membership in group `wash`)
	// can dial without CAP_DAC_OVERRIDE. The group itself is
	// inherited from the parent directory's setgid bit (spawn.go
	// creates /run/wash/<uid>/sessions/ as 02750 owner:wash), so
	// when the wash group exists the socket lands as group=wash
	// automatically. Single-user / dev installs without the wash
	// group end up with group=<user>, and wash-login (running as
	// the same user) still has access. peer-cred is the real gate;
	// permissions are defense-in-depth.
	if err := os.Chmod(r.cfg.ListenUnix, 0o660); err != nil {
		return fmt.Errorf("chmod %s: %w", r.cfg.ListenUnix, err)
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	r.log("ctl listening on %s (allow uid=%d, name=%q, idle=%s)",
		r.cfg.ListenUnix, r.cfg.AllowUID, r.cfg.Name, r.cfg.IdleTimeout)

	// Track in-flight handoffs so a clean shutdown joins them before
	// returning: each handleHandoff runs HandleShell, which honours ctx
	// and unwinds (logging its disconnect summary) on cancel. The
	// caller's "listener stopped" signal must not race ahead of that
	// teardown — mirrors runRawListener's session join.
	var handoffs sync.WaitGroup

	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				handoffs.Wait()
				return nil
			}
			r.log("ctl accept: %v", err)
			// Transient accept errors back off briefly so a hot
			// failure loop doesn't peg a CPU.
			select {
			case <-ctx.Done():
				handoffs.Wait()
				return nil
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		handoffs.Add(1)
		go func() {
			defer handoffs.Done()
			r.handleHandoff(ctx, c)
		}()
	}
}

// handleHandoff processes one SCM_RIGHTS message from a ctl-socket
// peer: peer-cred check, receive fd + replay, reconstruct the TCP
// conn, parse the HTTP/WS upgrade request, and dispatch to
// serveHandoffWS for the WebSocket attach.
//
// We don't use net/http's Server here because it pre-validates
// requests in ways that don't fit the synthetic-handoff model
// (rejecting requests with unusual Host fields or empty bodies before
// the handler runs). Instead we parse the request ourselves with
// http.ReadRequest and present a minimal http.ResponseWriter to
// websocket.Accept — which only needs Header / WriteHeader / Hijack.
func (r *Router) handleHandoff(ctx context.Context, ctl *net.UnixConn) {
	defer ctl.Close()

	if !r.checkCtlPeerCred(ctl) {
		return
	}

	fd, replay, err := recvHandoffMsg(ctl)
	if err != nil {
		r.log("ctl recvmsg: %v", err)
		return
	}

	// Wrap the received fd as a net.Conn. os.NewFile + net.FileConn
	// dup the fd internally; we close the *os.File handle on the
	// return path, and the dup'd descriptor lives on inside the
	// net.Conn until Close.
	f := os.NewFile(uintptr(fd), "ws-handoff")
	if f == nil {
		_ = syscall.Close(fd)
		r.log("ctl: NewFile failed for fd %d", fd)
		return
	}
	nc, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		r.log("ctl FileConn: %v", err)
		return
	}

	// Prepend the replay payload so http.ReadRequest sees the buffered
	// upgrade bytes wash-login captured before handing off.
	conn := &replayConn{Conn: nc, replay: replay}

	// Generic-ingress requests (/app/<token>/...) are plain HTTP — many
	// keep-alive asset fetches plus the backend's own WS upgrades — not
	// the single shell-WS attach the /ws path expects. Peek the request
	// path out of the replay bytes (which carry the whole first request)
	// without disturbing conn, and serve those through a real http.Server
	// over the handoff conn. wash-login forwards them with the same
	// SCM_RIGHTS handoff it uses for /ws (server.go handleAppIngress), so
	// in multi-user mode the per-user router — which binds no TCP — still
	// serves its ingress routes.
	if strings.HasPrefix(peekHandoffPath(replay), "/app/") {
		r.serveHandoffHTTP(ctx, conn)
		return
	}

	// Bound the read for the HTTP header so a malformed peer can't
	// keep this goroutine alive forever waiting for "\r\n\r\n".
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		// SetReadDeadline failure on a FileConn isn't fatal — proceed.
		r.log("ctl: set read deadline: %v", err)
	}

	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		r.log("ctl read request: %v", err)
		_ = conn.Close()
		return
	}
	// Clear the read deadline now that the headers have arrived;
	// the WebSocket session is long-lived.
	_ = conn.SetReadDeadline(time.Time{})

	req.RemoteAddr = "ws-handoff"
	rw := &hijackableRW{conn: conn, br: br, headers: make(http.Header)}
	r.serveHandoffWS(ctx, rw, req)
}

// checkCtlPeerCred reads SO_PEERCRED on the ctl-socket conn and
// returns true if the peer's uid matches r.cfg.AllowUID. Returns
// false (and logs) on any failure or mismatch — the caller drops
// the connection.
func (r *Router) checkCtlPeerCred(c *net.UnixConn) bool {
	raw, err := c.SyscallConn()
	if err != nil {
		r.log("ctl peer SyscallConn: %v", err)
		return false
	}
	var ucred *syscall.Ucred
	var ucredErr error
	if err := raw.Control(func(fd uintptr) {
		ucred, ucredErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || ucredErr != nil || ucred == nil {
		r.log("ctl peer-cred: ctrl=%v ucred=%v ucred=%v", err, ucredErr, ucred)
		return false
	}
	if ucred.Uid != r.cfg.AllowUID {
		r.log("ctl peer uid=%d rejected (allow=%d)", ucred.Uid, r.cfg.AllowUID)
		return false
	}
	return true
}

// serveHandoffWS is the http.Handler for a single accepted handoff:
// run websocket.Accept on the synthesized request, wrap the result in
// NewWSTransport, and dispatch to HandleShell.
func (r *Router) serveHandoffWS(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	r.log("ctl handoff: %s %s upgrade=%q connection=%q ws-key=%q ws-ver=%q",
		req.Method, req.URL.Path,
		req.Header.Get("Upgrade"), req.Header.Get("Connection"),
		req.Header.Get("Sec-WebSocket-Key"), req.Header.Get("Sec-WebSocket-Version"))
	ws, err := websocket.Accept(w, req, &websocket.AcceptOptions{
		// The peer is wash-login (or our test harness), not the
		// browser directly. Same-origin is enforced by wash-login
		// upstream before the handoff happens.
		InsecureSkipVerify: true,
	})
	if err != nil {
		r.log("ctl handoff ws accept: %v", err)
		return
	}
	ws.SetReadLimit(int64(MaxWSReadLimit))

	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	t := NewWSTransport(sessCtx, ws)
	if err := r.HandleShell(sessCtx, t); err != nil && !errors.Is(err, context.Canceled) {
		r.log("ctl handoff shell session: %v", err)
	}
}

// peekHandoffPath parses just the request line + headers out of the
// replay bytes (the first request wash-login captured before the
// handoff) to recover the request path, without touching the live conn.
// Returns "" when the replay doesn't parse — the caller then falls
// through to the shell-WS path, which re-reads the request from the conn
// itself, so a peek miss never strands a handoff.
func peekHandoffPath(replay []byte) string {
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(replay)))
	if err != nil {
		return ""
	}
	return req.URL.Path
}

// serveHandoffHTTP serves a handed-off conn as an HTTP/1.1 keep-alive
// connection through the router's ingress mux. The conn still carries
// the full first request in its replay prefix, so http.Server reads it
// (and any subsequent keep-alive requests, and the WS-upgrade hijack the
// ingress reverse-proxy performs for code-server's own sockets) until
// the peer closes the conn or ctx cancels. One handoff == one browser
// TCP connection == one short-lived http.Server.
func (r *Router) serveHandoffHTTP(ctx context.Context, conn net.Conn) {
	ln := newSingleConnListener(conn)
	srv := &http.Server{
		Handler:           r.ingressMux(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	// Serve returns once the listener closes: the single-conn listener
	// trips its own Close when the served conn is closed (peer hangup),
	// and srv.Close() trips it on ctx cancel.
	_ = srv.Serve(ln)
}

// ingressMux routes /app/<token>/* to the shared ingress reverse-proxy.
// It is the handoff-path equivalent of the /app/ route the direct-TCP
// HTTPServer mounts (http.go); both dispatch to Router.handleIngress so
// single-user and multi-user deployments proxy identically.
func (r *Router) ingressMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/app/", r.handleIngress)
	return mux
}

// singleConnListener is a net.Listener that yields exactly one
// already-open conn and then blocks Accept until Close. http.Server
// wants a Listener; this adapts our single handoff conn to that shape.
// The yielded conn is wrapped so its Close trips the listener, letting
// http.Server.Serve return when the peer hangs up.
type singleConnListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	l := &singleConnListener{ch: make(chan net.Conn, 1), done: make(chan struct{})}
	l.ch <- &closeNotifyConn{Conn: conn, onClose: l.Close}
	return l
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *singleConnListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *singleConnListener) Addr() net.Addr { return handoffAddr{} }

type handoffAddr struct{}

func (handoffAddr) Network() string { return "unix" }
func (handoffAddr) String() string  { return "ws-handoff" }

// closeNotifyConn fires onClose exactly once when the conn is closed,
// so the single-conn listener can unblock its Accept and let
// http.Server.Serve return instead of hanging forever.
type closeNotifyConn struct {
	net.Conn
	once    sync.Once
	onClose func() error
}

func (c *closeNotifyConn) Close() error {
	c.once.Do(func() { _ = c.onClose() })
	return c.Conn.Close()
}

// recvHandoffMsg reads one Recvmsg on ctl, expecting exactly one fd
// passed via SCM_RIGHTS and 0..MaxHandoffReplay bytes of replay
// payload. Returns the received fd and the replay bytes. Caller owns
// closing the fd on success and the various error paths leave nothing
// for the caller to close.
func recvHandoffMsg(ctl *net.UnixConn) (int, []byte, error) {
	buf := make([]byte, MaxHandoffReplay)
	oob := make([]byte, syscall.CmsgSpace(4)) // one int32 fd
	n, oobn, _, _, err := ctl.ReadMsgUnix(buf, oob)
	if err != nil {
		return -1, nil, fmt.Errorf("readmsg: %w", err)
	}
	msgs, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return -1, nil, fmt.Errorf("parse control: %w", err)
	}
	fd := -1
	for _, m := range msgs {
		if m.Header.Type != syscall.SCM_RIGHTS {
			continue
		}
		fds, err := syscall.ParseUnixRights(&m)
		if err != nil {
			closeAll(fds)
			return -1, nil, fmt.Errorf("parse rights: %w", err)
		}
		if fd != -1 {
			// Extra fd in a second SCM_RIGHTS — close everything,
			// reject. Protocol is exactly one fd per handoff.
			closeAll(fds)
			_ = syscall.Close(fd)
			return -1, nil, errors.New("handoff: multiple SCM_RIGHTS not allowed")
		}
		if len(fds) != 1 {
			closeAll(fds)
			return -1, nil, fmt.Errorf("handoff: expected exactly 1 fd, got %d", len(fds))
		}
		fd = fds[0]
	}
	if fd < 0 {
		return -1, nil, errors.New("handoff: no SCM_RIGHTS fd present")
	}
	// Copy the buffered prefix out of the recv buffer so the caller
	// owns it independent of buf's storage.
	replay := make([]byte, n)
	copy(replay, buf[:n])
	return fd, replay, nil
}

func closeAll(fds []int) {
	for _, fd := range fds {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
	}
}

// replayConn is a net.Conn whose first Reads drain a pre-supplied
// buffer (the bytes wash-login peeked off the HTTP upgrade request
// before doing the SCM_RIGHTS handoff) before falling through to the
// underlying Conn. Reads are not concurrent-safe; the http.Server
// only ever has one reader.
type replayConn struct {
	net.Conn
	replay []byte
}

func (c *replayConn) Read(p []byte) (int, error) {
	if len(c.replay) > 0 {
		n := copy(p, c.replay)
		c.replay = c.replay[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// hijackableRW is a minimal http.ResponseWriter wrapper around an
// already-accepted net.Conn that supports http.Hijacker. It exists
// because handoff connections are presented to websocket.Accept
// without going through net/http's Server (which would re-parse and
// pre-filter our synthetic request bytes). websocket.Accept only
// touches Header / WriteHeader / Hijack on its ResponseWriter.
type hijackableRW struct {
	conn          net.Conn
	br            *bufio.Reader
	headers       http.Header
	status        int
	headerWritten bool
}

func (w *hijackableRW) Header() http.Header { return w.headers }

func (w *hijackableRW) WriteHeader(code int) {
	if w.headerWritten {
		return
	}
	w.headerWritten = true
	w.status = code
	var sb strings.Builder
	fmt.Fprintf(&sb, "HTTP/1.1 %d %s\r\n", code, http.StatusText(code))
	for k, vs := range w.headers {
		for _, v := range vs {
			fmt.Fprintf(&sb, "%s: %s\r\n", k, v)
		}
	}
	sb.WriteString("\r\n")
	_, _ = w.conn.Write([]byte(sb.String()))
}

func (w *hijackableRW) Write(p []byte) (int, error) {
	if !w.headerWritten {
		w.WriteHeader(http.StatusOK)
	}
	return w.conn.Write(p)
}

func (w *hijackableRW) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	bw := bufio.NewWriter(w.conn)
	return w.conn, bufio.NewReadWriter(w.br, bw), nil
}

// ShellCount returns the number of shells currently attached to the
// router. Used by the idle reaper in multi-user mode.
func (r *Router) ShellCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.shells)
}

// idleReapTickInterval bounds how often the reaper polls r.shells.
// A coarse cadence is fine — idle-timeout granularity at the second
// level is plenty, and a polling loop avoids retrofitting an
// observer pattern onto register/unregisterShell.
const idleReapTickInterval = 5 * time.Second

// ReapWhenIdle blocks until either ctx is cancelled or the router
// has been continuously idle (zero attached shells) for timeout.
//
// Returns ctx.Err() when ctx cancels (normal shutdown via SIGINT /
// SIGTERM); returns nil when the idle threshold is hit and the
// caller should drive a graceful exit.
//
// A router that never receives a handoff is considered idle from
// t=0 — wash-login spawning a router that no browser connects to
// must not leak indefinitely. timeout==0 disables reaping.
func (r *Router) ReapWhenIdle(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	tick := time.NewTicker(idleReapTickInterval)
	defer tick.Stop()
	var idleSince time.Time
	if r.ShellCount() == 0 {
		idleSince = time.Now()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-tick.C:
			if r.ShellCount() > 0 {
				idleSince = time.Time{}
				continue
			}
			if idleSince.IsZero() {
				idleSince = now
				continue
			}
			if now.Sub(idleSince) >= timeout {
				r.log("idle for %s — exiting", timeout)
				return nil
			}
		}
	}
}
