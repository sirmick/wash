package term

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallShimsPerBinary(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	// Another build's directory whose binary is gone, and one whose binary
	// still exists: the first is pruned, the second is someone else's.
	prefix := filepath.Join(base, fmt.Sprintf("wash-shims-%d-", os.Getuid()))
	gone, live := prefix+"000000000000", prefix+"111111111111"
	for dir, target := range map[string]string{gone: filepath.Join(base, "deleted", "wash"), live: self} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "xdg-open")); err != nil {
			t.Fatal(err)
		}
	}

	dir := installShims()
	if dir == "" || dir == gone || dir == live {
		t.Fatalf("installShims() = %q, want a directory of its own", dir)
	}
	if got, err := os.Readlink(filepath.Join(dir, "xdg-open")); err != nil || got != self {
		t.Errorf("xdg-open -> %q, %v; want %q", got, err, self)
	}
	if again := installShims(); again != dir {
		t.Errorf("second installShims() = %q, want the same %q", again, dir)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("stale %s still there (%v)", gone, err)
	}
	if got, err := os.Readlink(filepath.Join(live, "xdg-open")); err != nil || got != self {
		t.Errorf("live sibling repointed: %q, %v", got, err)
	}
}
