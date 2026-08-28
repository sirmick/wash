package login

// End-to-end integration test for the M3 handoff path.
//
// Spawns a real wash-router via the Spawner, sets up a wash-login
// httptest.Server with the /ws handoff endpoint live, does the auth
// round-trip, then opens a raw TCP connection and writes a synthetic
// WS upgrade request. Verifies that the per-user router (via
// SCM_RIGHTS) responds with a 101 Switching Protocols.
//
// Test is skipped under `go test -short` because it requires
// building wash-router from source — a few seconds of go-build per
// test invocation if the cache is cold.

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	testRouterBinOnce sync.Once
	testRouterBinPath string
	testRouterBinErr  error
)

// testRouterBinary returns the path to a built wash-router suitable
// for use as Spawner.RouterBinary. First invocation builds via
// `go build`; subsequent calls reuse the result.
func testRouterBinary(t *testing.T) string {
	t.Helper()
	// Spawned routers use --listen-unix, which auto-enables login-env
	// adoption (running the invoking user's real shell profile per
	// spawn). Veto it so tests stay deterministic and profile-free;
	// the Spawner's childEnv passes this through to the router.
	t.Setenv("WASH_LOGIN_ENV", "off")
	testRouterBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wash-router-test-bin-")
		if err != nil {
			testRouterBinErr = err
			return
		}
		bin := filepath.Join(dir, "wash-router")
		cmd := exec.Command("go", "build", "-o", bin, "github.com/sirmick/wash/cmd/wash-router")
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			testRouterBinErr = fmt.Errorf("go build wash-router: %w", err)
			return
		}
		testRouterBinPath = bin
	})
	if testRouterBinErr != nil {
		t.Fatalf("build wash-router: %v", testRouterBinErr)
	}
	return testRouterBinPath
}

// TestHandoffEndToEnd: auth → /ws → spawn → SCM_RIGHTS → 101 from
// the per-user router. The full M3 path in one test.
func TestHandoffEndToEnd(t *testing.T) {
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
		Auth:   auth,
		Signer: signerForTest(t),
		Logger: log.New(io.Discard, "", 0),
		// Scoped to runRoot: /proc is host-global, so an unscoped
		// registry adopts the developer's real wash session instead
		// of spawning one here.
		Sessions: &ProcRegistry{SockRoot: runRoot},
		Spawner: &Spawner{
			RouterBinary: routerBin,
			AllowUID:     uint32(os.Getuid()),
			RunRoot:      runRoot,
			// No idle timeout — the test cleans up via SIGTERM.
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer killSpawnedRouters(t, runRoot)

	// Auth: get a cookie.
	jar := newSimpleJar(ts.URL)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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

	// Now open a raw TCP connection to the httptest.Server and write
	// the WS upgrade request manually. We can't use http.Client
	// because the response side hijacks; httptest's TCP listener
	// gives us a real socket so SCM_RIGHTS can move it.
	addr := strings.TrimPrefix(ts.URL, "http://")
	tcp, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial httptest server: %v", err)
	}
	defer tcp.Close()

	upgradeReq := "GET /ws HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Cookie: " + CookieName + "=" + sessionCookie.Value + "\r\n" +
		"\r\n"
	if _, err := tcp.Write([]byte(upgradeReq)); err != nil {
		t.Fatalf("write upgrade: %v", err)
	}

	// Read the response with a deadline. The per-user router will
	// take a few hundred ms to spawn and respond.
	if err := tcp.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	br := bufio.NewReader(tcp)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.HasPrefix(statusLine, "HTTP/1.1 101 ") {
		// Drain a bit more for the failure message.
		rest := make([]byte, 2048)
		n, _ := br.Read(rest)
		t.Fatalf("expected 101 Switching Protocols, got status line %q + %q", statusLine, string(rest[:n]))
	}

	// Verify the session actually shows up in /proc.
	reg := &ProcRegistry{SockRoot: runRoot}
	sessions, err := reg.List(uint32(os.Getuid()))
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) == 0 {
		t.Errorf("spawned session not visible in /proc registry (run-root=%s)", runRoot)
	}
	for _, s := range sessions {
		t.Logf("found spawned session: pid=%d sessid=%s name=%q sock=%s", s.Pid, s.SessID, s.Name, s.Sock)
	}
}

// killSpawnedRouters walks the test's run-root, finds any wash-
// router pids whose ctl socket lives under runRoot, and SIGTERMs
// them. Without this, an aborted test leaks per-user routers that
// hold the socket open and eat inotify instances over time
// (see feedback_e2e_orphan_accumulation).
func killSpawnedRouters(t *testing.T, runRoot string) {
	t.Helper()
	reg := &ProcRegistry{SockRoot: runRoot}
	sessions, err := reg.List(uint32(os.Getuid()))
	if err != nil {
		t.Logf("cleanup list: %v", err)
		return
	}
	for _, s := range sessions {
		if err := syscall.Kill(s.Pid, syscall.SIGTERM); err != nil {
			t.Logf("cleanup SIGTERM pid=%d: %v", s.Pid, err)
			continue
		}
		// Give the process a moment to clean up.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && processAlive(s.Pid) {
			time.Sleep(20 * time.Millisecond)
		}
		if processAlive(s.Pid) {
			_ = syscall.Kill(s.Pid, syscall.SIGKILL)
		}
	}
}
