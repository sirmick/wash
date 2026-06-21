package router

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestUnixListenerHandoff proves the M1 round-trip: a SCM_RIGHTS
// handoff of a peer-fd plus replay bytes (a synthetic HTTP/WS
// upgrade request) into RunUnixListener results in a 101 Switching
// Protocols response written to the peer fd. This is the "wash-login
// to per-user wash-router" handshake without involving any real
// wash-login binary or browser.
//
// Test topology:
//
//	socketpair (a, b)   ── a kept by test as "browser end"
//	                     │
//	                     │  test writes WS-upgrade bytes to a
//	                     │  (but those land in the kernel buffer
//	                     │  for b to read)
//	                     ▼
//	test SCM_RIGHTS-sends b's fd + empty replay-payload to the
//	router's ctl socket; the router reconstructs b as a net.Conn,
//	runs websocket.Accept on it (reading the upgrade request from
//	the kernel buffer), and writes the 101 response back through b
//	(which the test reads from a).
func TestUnixListenerHandoff(t *testing.T) {
	// socketpair gives us two connected fds; SCM_RIGHTS to the
	// router lets it own one end while we keep the other.
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	aFD, bFD := pair[0], pair[1]
	aFile := os.NewFile(uintptr(aFD), "browser-end")
	a, err := net.FileConn(aFile)
	if err != nil {
		t.Fatalf("FileConn: %v", err)
	}
	_ = aFile.Close()
	defer a.Close()

	tmpDir := t.TempDir()
	sock := filepath.Join(tmpDir, "ctl.sock")

	r := NewRouter(Config{
		ListenUnix:   sock,
		AllowUID:     uint32(os.Getuid()),
		NoSession:    true,
		SessionAppID: "com.wash.session",
		IdleTimeout:  0, // disable reaping for this test
	}, NewRegistry(), func(format string, args ...any) {
		t.Logf("router: "+format, args...)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenerErr := make(chan error, 1)
	go func() { listenerErr <- r.RunUnixListener(ctx) }()

	// Wait for the socket file to appear.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("ctl socket never appeared: %v", err)
	}

	// Dial the ctl socket as ourselves; AllowUID is our own uid so
	// SO_PEERCRED accepts us.
	ctl, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatalf("dial ctl: %v", err)
	}
	defer ctl.Close()

	// SCM_RIGHTS the bFD over to the router, with the WS upgrade
	// request as the replay payload — this is exactly what wash-login
	// will do in production: it peeks the HTTP upgrade bytes off the
	// browser TCP stream via bufio, validates the cookie, then sends
	// (fd, peeked-bytes) so the per-user router can reconstruct the
	// request stream without further coordination.
	const upgradeReq = "GET /ws HTTP/1.1\r\n" +
		"Host: example.test\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"\r\n"
	oob := syscall.UnixRights(bFD)
	if _, _, err := ctl.WriteMsgUnix([]byte(upgradeReq), oob, nil); err != nil {
		t.Fatalf("sendmsg: %v", err)
	}
	// We've passed bFD; close our reference (kernel dup'd into the
	// router's fd table during sendmsg).
	_ = syscall.Close(bFD)

	// Now read the WS handshake response from a. The router runs
	// websocket.Accept which writes:
	//   HTTP/1.1 101 Switching Protocols\r\n
	//   Upgrade: websocket\r\n
	//   Connection: Upgrade\r\n
	//   Sec-WebSocket-Accept: <sha1-of-key+magic>\r\n\r\n
	if err := a.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 4096)
	n, err := a.Read(buf)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	response := string(buf[:n])
	if !strings.HasPrefix(response, "HTTP/1.1 101 ") {
		t.Fatalf("expected 101 Switching Protocols, got:\n%s", response)
	}
	if !strings.Contains(response, "Upgrade: websocket") {
		t.Errorf("response missing Upgrade: websocket header:\n%s", response)
	}
	// Go's http.Header canonicalises "Sec-WebSocket-Accept" to
	// "Sec-Websocket-Accept" via textproto; accept either form.
	if !strings.Contains(strings.ToLower(response), "sec-websocket-accept:") {
		t.Errorf("response missing Sec-WebSocket-Accept header:\n%s", response)
	}

	cancel()
	select {
	case err := <-listenerErr:
		if err != nil {
			t.Errorf("listener returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("listener did not shut down within 2s after ctx cancel")
	}
}

// TestUnixListenerRejectsWrongPeerUID verifies the SO_PEERCRED gate:
// a connection from a uid that doesn't match AllowUID is dropped
// without the router reading its message.
//
// We can't easily test this with a different uid in a unit test
// (would need a setuid binary), but we CAN verify the policy by
// pointing AllowUID at a uid we definitely aren't and observing
// that our dial+sendmsg never produces a 101.
func TestUnixListenerRejectsWrongPeerUID(t *testing.T) {
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	aFD, bFD := pair[0], pair[1]
	aFile := os.NewFile(uintptr(aFD), "browser-end")
	a, err := net.FileConn(aFile)
	if err != nil {
		t.Fatalf("FileConn: %v", err)
	}
	_ = aFile.Close()
	defer a.Close()
	defer syscall.Close(bFD)

	tmpDir := t.TempDir()
	sock := filepath.Join(tmpDir, "ctl.sock")

	// Pick a uid we are demonstrably not. uid+1 works on any
	// machine where we aren't already the max uid (true in practice).
	wrongUID := uint32(os.Getuid()) + 1

	r := NewRouter(Config{
		ListenUnix:   sock,
		AllowUID:     wrongUID,
		NoSession:    true,
		SessionAppID: "com.wash.session",
	}, NewRegistry(), func(format string, args ...any) {
		t.Logf("router: "+format, args...)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenerErr := make(chan error, 1)
	go func() { listenerErr <- r.RunUnixListener(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("ctl socket never appeared: %v", err)
	}

	ctl, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatalf("dial ctl: %v", err)
	}
	defer ctl.Close()

	oob := syscall.UnixRights(bFD)
	if _, _, err := ctl.WriteMsgUnix([]byte("GET /ws HTTP/1.1\r\n\r\n"), oob, nil); err != nil {
		// Some kernels return EPIPE here if the router has already
		// closed; either ECONNRESET-equivalent or success-but-discarded
		// is acceptable, the assertion below is the real test.
		t.Logf("sendmsg (allowed to fail): %v", err)
	}

	// Read with a tight deadline. If the router were to accept the
	// handoff and respond with 101, we'd see it within ~100ms. The
	// peer-cred rejection means we get nothing.
	if err := a.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 4096)
	n, err := a.Read(buf)
	if err == nil && strings.HasPrefix(string(buf[:n]), "HTTP/1.1 101 ") {
		t.Fatalf("router accepted handoff from wrong uid: %s", string(buf[:n]))
	}
	// EOF / deadline / connection-reset are all acceptable here —
	// the only failure mode is a 101 response being produced.

	cancel()
	<-listenerErr
}

// TestUnixListenerIngressHandoff proves the multi-user ingress path: a
// SCM_RIGHTS handoff carrying a plain HTTP GET for /app/<token>/... is
// served by the router as an HTTP request (not a shell-WS attach) and
// reverse-proxied to the published backend, with the response written
// back over the handoff conn. This is the wash-login → per-router hop
// that lets code-server (and any embedded web app) work behind the
// multi-user login front, where the router binds no TCP.
func TestUnixListenerIngressHandoff(t *testing.T) {
	// Backend the ingress will proxy to: echoes the stripped path and
	// returns a known body so we can assert end-to-end.
	var gotPath, gotIngress string
	backendSock := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		gotIngress = req.Header.Get("X-Wash-Ingress-Path")
		_, _ = w.Write([]byte("hello from backend"))
	}))

	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	aFD, bFD := pair[0], pair[1]
	aFile := os.NewFile(uintptr(aFD), "browser-end")
	a, err := net.FileConn(aFile)
	if err != nil {
		t.Fatalf("FileConn: %v", err)
	}
	_ = aFile.Close()
	defer a.Close()

	tmpDir := t.TempDir()
	sock := filepath.Join(tmpDir, "ctl.sock")

	r := NewRouter(Config{
		ListenUnix:   sock,
		AllowUID:     uint32(os.Getuid()),
		NoSession:    true,
		SessionAppID: "com.wash.session",
		IdleTimeout:  0,
	}, NewRegistry(), func(format string, args ...any) {
		t.Logf("router: "+format, args...)
	})

	// Publish a backend; this is what an app BE does via PublishIngress.
	path, token, err := r.ingress.publish("i-1", "unix", backendSock)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenerErr := make(chan error, 1)
	go func() { listenerErr <- r.RunUnixListener(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("ctl socket never appeared: %v", err)
	}

	ctl, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatalf("dial ctl: %v", err)
	}
	defer ctl.Close()

	// The whole GET sits in the replay payload — exactly what wash-login
	// peeks off the browser before the handoff. The router must route on
	// /app/ and serve it as HTTP rather than try a WS upgrade.
	req := "GET " + path + "sub/page?x=1 HTTP/1.1\r\n" +
		"Host: example.test\r\n" +
		"Connection: close\r\n" +
		"\r\n"
	oob := syscall.UnixRights(bFD)
	if _, _, err := ctl.WriteMsgUnix([]byte(req), oob, nil); err != nil {
		t.Fatalf("sendmsg: %v", err)
	}
	_ = syscall.Close(bFD)

	if err := a.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(a), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if string(body) != "hello from backend" {
		t.Errorf("body = %q, want %q", body, "hello from backend")
	}
	if gotPath != "/sub/page" {
		t.Errorf("backend path = %q, want /sub/page (ingress prefix not stripped)", gotPath)
	}
	if want := strings.TrimSuffix(path, "/"); gotIngress != want {
		t.Errorf("X-Wash-Ingress-Path = %q, want %q", gotIngress, want)
	}
	_ = token

	cancel()
	select {
	case err := <-listenerErr:
		if err != nil {
			t.Errorf("listener returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("listener did not shut down within 2s after ctx cancel")
	}
}
