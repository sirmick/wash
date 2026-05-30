package router

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIngressProxy_RealCodeServer drives a REAL code-server behind the
// real ingress proxy and confirms the workbench is served correctly
// under the /app/<token>/ prefix — the one architectural unknown
// (does code-server cope with our prefix-stripping proxy?).
//
// Gated: skips unless WASH_VSCODE_LIVE=1 and a code-server binary is
// found (managed cache or PATH). It spawns a ~heavy process, so it's
// opt-in, not part of the default suite.
func TestIngressProxy_RealCodeServer(t *testing.T) {
	if os.Getenv("WASH_VSCODE_LIVE") != "1" {
		t.Skip("set WASH_VSCODE_LIVE=1 to run the real code-server integration test")
	}
	bin := findCodeServer()
	if bin == "" {
		t.Skip("code-server not installed")
	}

	dir := t.TempDir()
	sock := filepath.Join(dir, "cs.sock")
	cmd := exec.Command(bin,
		"--auth", "none", "--disable-telemetry", "--disable-update-check",
		"--socket", sock,
		"--user-data-dir", filepath.Join(dir, "ud"),
		"--extensions-dir", filepath.Join(dir, "ext"),
		filepath.Join(dir, "ws"),
	)
	// Same env scrub the daemon applies (we may be inside a VS Code terminal).
	cmd.Env = scrubVSCode(os.Environ())
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := os.MkdirAll(filepath.Join(dir, "ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start code-server: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	if err := pollHealthz(sock, 30*time.Second); err != nil {
		t.Fatalf("code-server never healthy: %v", err)
	}

	r, front := newTestFront(t)
	path, token, err := r.ingress.publish("i-cs", "unix", sock)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	t.Logf("ingress path %s", path)

	// 1. /healthz through the proxy.
	if code, _ := proxyGet(t, front.URL+path+"healthz"); code != 200 {
		t.Errorf("/healthz via proxy = %d, want 200", code)
	}

	// 2. The workbench root through the proxy: must be 200 and look
	// like VS Code. This is the prefix-stripping acid test.
	code, body := proxyGet(t, front.URL+path)
	if code != 200 {
		t.Fatalf("workbench root via proxy = %d, want 200", code)
	}
	low := strings.ToLower(body)
	if !strings.Contains(low, "vscode") && !strings.Contains(low, "workbench") {
		t.Errorf("served root doesn't look like the VS Code workbench (len=%d)", len(body))
	}
	// Report asset-URL style so a regression in subpath handling is
	// visible in the log even when the page still 200s.
	t.Logf("workbench root %d bytes; token=%s", len(body), token)
	if strings.Contains(body, `src="/`) || strings.Contains(body, `href="/`) {
		t.Logf("NOTE: root references some absolute (/) URLs — verify assets still load under the prefix in a browser")
	}
}

func findCodeServer() string {
	managed := filepath.Join(os.Getenv("HOME"), ".cache", "wash", "code-server", "current", "bin", "code-server")
	if fi, err := os.Stat(managed); err == nil && !fi.IsDir() {
		return managed
	}
	if p, err := exec.LookPath("code-server"); err == nil {
		return p
	}
	return ""
}

func scrubVSCode(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "VSCODE_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func pollHealthz(sock string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://unix/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return context.DeadlineExceeded
}

func proxyGet(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(b)
}
