package routerrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteReadTokenFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "router.token") // sub/ doesn't exist yet
	const token = "0a0738ea5bd3d9cd5ebe2edd65173639"

	if err := writeTokenFile(path, token); err != nil {
		t.Fatalf("writeTokenFile: %v", err)
	}
	got, err := readTokenFile(path)
	if err != nil {
		t.Fatalf("readTokenFile: %v", err)
	}
	if got != token {
		t.Errorf("round trip: got %q want %q", got, token)
	}
}

func TestWriteTokenFileMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.token")
	if err := writeTokenFile(path, "abc"); err != nil {
		t.Fatalf("writeTokenFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file mode = %o, want 600", perm)
	}
}

func TestReadTokenFileTrimsWhitespace(t *testing.T) {
	// writeTokenFile appends a newline; a hand-edited file might have
	// extra surrounding whitespace. readTokenFile must strip both so
	// the token compares equal to what the browser sends.
	path := filepath.Join(t.TempDir(), "router.token")
	if err := os.WriteFile(path, []byte("  deadbeef\n\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := readTokenFile(path)
	if err != nil {
		t.Fatalf("readTokenFile: %v", err)
	}
	if got != "deadbeef" {
		t.Errorf("got %q, want trimmed deadbeef", got)
	}
}

func TestReadTokenFileMissing(t *testing.T) {
	_, err := readTokenFile(filepath.Join(t.TempDir(), "nope.token"))
	if err == nil {
		t.Fatal("expected error reading a missing token file")
	}
}

func TestDefaultTokenFilePathUsesRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	got := defaultTokenFilePath(4242)
	want := "/run/user/1000/wash/router-4242.token"
	if got != want {
		t.Errorf("defaultTokenFilePath = %q, want %q", got, want)
	}
	// Without XDG it falls back under /tmp/wash-<uid>.
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := defaultTokenFilePath(7); !strings.Contains(got, "/tmp/wash-") || !strings.HasSuffix(got, "/router-7.token") {
		t.Errorf("fallback path %q not under /tmp/wash-<uid>", got)
	}
}
