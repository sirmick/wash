package vscode

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScrubVSCodeEnv(t *testing.T) {
	in := []string{"PATH=/usr/bin", "VSCODE_IPC_HOOK_CLI=/tmp/x.sock", "HOME=/home/u", "VSCODE_INJECTION=1", "TERM=xterm"}
	for _, kv := range scrubVSCodeEnv(in) {
		if strings.HasPrefix(kv, "VSCODE_") {
			t.Errorf("VSCODE_ var survived scrub: %q", kv)
		}
	}
	// Non-VSCODE vars preserved.
	got := strings.Join(scrubVSCodeEnv(in), " ")
	for _, want := range []string{"PATH=/usr/bin", "HOME=/home/u", "TERM=xterm"} {
		if !strings.Contains(got, want) {
			t.Errorf("dropped non-VSCODE var %q", want)
		}
	}
}

func TestBuildInstallScript(t *testing.T) {
	s := buildInstallScript("4.107.0", "amd64", "/cache/cs")
	for _, want := range []string{
		"code-server-4.107.0-linux-amd64.tar.gz",
		"--strip-components=1",
		`"/cache/cs"`,
		`"$CACHE/current"`,
		"ln -sfn",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("install script missing %q", want)
		}
	}
}

func TestCodeServerArch(t *testing.T) {
	suffix, ok := codeServerArch()
	switch suffix {
	case "amd64", "arm64":
		if !ok {
			t.Errorf("arch %q reported not-ok", suffix)
		}
	case "":
		if ok {
			t.Error("empty suffix but ok=true")
		}
	}
}

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{"i-42": "i-42", "a/b": "a_b", "x:y": "x_y"} {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q)=%q want %q", in, got, want)
		}
	}
}

func TestDataHomeConfined(t *testing.T) {
	m := &manager{root: "/srv/sess"}
	if got := m.dataHome(); !strings.HasPrefix(got, "/srv/sess/") {
		t.Errorf("confined dataHome=%q", got)
	}
}

func TestCacheRoot(t *testing.T) {
	if filepath.Base(cacheRoot()) != "code-server" {
		t.Errorf("cacheRoot=%q", cacheRoot())
	}
}

func TestWaitHealthy(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "h.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitHealthy(ctx, sock, 5*time.Second); err != nil {
		t.Fatalf("waitHealthy: %v", err)
	}
}

func TestWaitHealthyTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := waitHealthy(ctx, filepath.Join(t.TempDir(), "dead.sock"), 500*time.Millisecond); err == nil {
		t.Fatal("expected timeout")
	}
}
