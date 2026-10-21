package syslogs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadable(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.log")
	if err := os.WriteFile(ok, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !readable(ok) {
		t.Errorf("readable(%s) = false, want true", ok)
	}
	// A missing file is tail -F's to wait for, not a reason to escalate.
	if !readable(filepath.Join(dir, "gone.log")) {
		t.Error("readable(missing) = false, want true")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file")
	}
	locked := filepath.Join(dir, "locked.log")
	if err := os.WriteFile(locked, []byte("x\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if readable(locked) {
		t.Errorf("readable(%s) = true, want false", locked)
	}
}
