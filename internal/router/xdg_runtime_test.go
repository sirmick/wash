package router

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureXDGRuntimeDirCreates0700 — (REVIEW-X11-WAYLAND #4) the per-user
// XDG_RUNTIME_DIR the router provisions in its setuid context must be 0700 so
// libwayland accepts it for wash-display's wayland socket. (Owner is the
// process euid — the target user in production; the test runs as itself.)
func TestEnsureXDGRuntimeDirCreates0700(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "1234", "xdg")

	if err := ensureXDGRuntimeDir(dir); err != nil {
		t.Fatalf("ensureXDGRuntimeDir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !fi.IsDir() {
		t.Fatal("not a directory")
	}
	// Mask to the permission bits. Created-by-us dir is pinned to 0700 even
	// under a loose umask (the Chmod in ensureXDGRuntimeDir).
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("mode = %o, want 0700 (same-uid private runtime dir)", perm)
	}
}

// TestEnsureXDGRuntimeDirEmptyNoop — no XDG_RUNTIME_DIR set is a clean no-op
// (a display layer is optional; the router must still serve terminals).
func TestEnsureXDGRuntimeDirEmptyNoop(t *testing.T) {
	if err := ensureXDGRuntimeDir(""); err != nil {
		t.Errorf("empty path should be a no-op, got %v", err)
	}
}

// TestEnsureXDGRuntimeDirExistingLeftAsIs — an already-provisioned dir (e.g. a
// logind /run/user/<uid>) is not re-chmod'd by us.
func TestEnsureXDGRuntimeDirExistingLeftAsIs(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "existing")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("pre-create: %v", err)
	}
	if err := ensureXDGRuntimeDir(dir); err != nil {
		t.Fatalf("ensureXDGRuntimeDir: %v", err)
	}
	fi, _ := os.Stat(dir)
	if perm := fi.Mode().Perm(); perm != 0o750 {
		t.Errorf("existing dir mode = %o, want 0750 unchanged (we only pin dirs we create)", perm)
	}
}
