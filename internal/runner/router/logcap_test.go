package routerrun

import (
	"os"
	"path/filepath"
	"testing"
)

// A log over the cap moves to .1 and the live file empties in place, so an
// O_APPEND writer that holds it open keeps writing to the live file.
func TestCapLogMovesAndEmpties(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.log")
	w, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.WriteString("old line\n"); err != nil {
		t.Fatal(err)
	}

	if err := capLog(path, 1<<20); err != nil {
		t.Fatalf("under the cap: %v", err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatal("rotated a log under the cap")
	}

	if err := capLog(path, 4); err != nil {
		t.Fatalf("capLog: %v", err)
	}
	if b, _ := os.ReadFile(path + ".1"); string(b) != "old line\n" {
		t.Fatalf(".1 = %q, want the old contents", b)
	}
	if _, err := w.WriteString("new line\n"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new line\n" {
		t.Fatalf("live log = %q, want only what came after", b)
	}
}
