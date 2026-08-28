package login

// Integration test for the TLS proxying handoff (issue #11).
//
// When wash-login terminates its own self-signed HTTPS, the hijacked
// conn is a *tls.Conn whose plaintext can't ride an SCM_RIGHTS fd. This
// test drives that path end-to-end: a TLS wash-login, an auth round-trip
// over HTTPS, then a raw TLS WebSocket upgrade that must come back as a
// 101 proxied from a real per-user wash-router over the socketpair.

import (
	"bufio"
	"crypto/tls"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProxyHandoffOverTLS(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test; skipping under -short")
	}
	if runtime.GOOS != "linux" {
		t.Skip("SCM_RIGHTS + SO_PEERCRED dance is Linux-specific")
	}

	routerBin := testRouterBinary(t)
	runRoot := t.TempDir()

	auth, err := NewTestAuth("alice:hunter2", uint32(os.Getuid()), uint32(os.Getgid()), "/bin/sh")
	if err != nil {
		t.Fatalf("NewTestAuth: %v", err)
	}
	srv, err := NewServer(Config{
		Auth:     auth,
		Signer:   signerForTest(t),
		Logger:   log.New(io.Discard, "", 0),
		Sessions: &ProcRegistry{SockRoot: runRoot},
		Spawner: &Spawner{
			RouterBinary: routerBin,
			AllowUID:     uint32(os.Getuid()),
			RunRoot:      runRoot,
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// NewTLSServer terminates TLS itself, so the hijacked conn inside the
	// handler is a *tls.Conn — exactly the local-HTTPS wash-login case.
	ts := httptest.NewTLSServer(srv.Handler())
	defer ts.Close()
	defer killSpawnedRouters(t, runRoot)

	// Auth over HTTPS using the server-trusting client, capturing the cookie.
	client := ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.PostForm(ts.URL+"/auth", url.Values{
		"user":     []string{"alice"},
		"password": []string{"hunter2"},
	})
	if err != nil {
		t.Fatalf("auth POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("auth status: got %d want 302", resp.StatusCode)
	}
	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatalf("auth did not set %s cookie", CookieName)
	}

	// Raw TLS dial + manual WS upgrade so we control the socket (an
	// http.Client can't drive the hijacked response side).
	addr := strings.TrimPrefix(ts.URL, "https://")
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer conn.Close()

	upgradeReq := "GET /ws HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Cookie: " + CookieName + "=" + sessionCookie.Value + "\r\n" +
		"\r\n"
	if _, err := conn.Write([]byte(upgradeReq)); err != nil {
		t.Fatalf("write upgrade: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.HasPrefix(statusLine, "HTTP/1.1 101 ") {
		rest := make([]byte, 2048)
		n, _ := br.Read(rest)
		t.Fatalf("expected 101 Switching Protocols over TLS, got %q + %q", statusLine, string(rest[:n]))
	}
}
