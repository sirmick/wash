package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ListN reports the directory's full entry count alongside the capped
// slice, so a file manager can say "showing first N of total" instead of
// a bare "truncated".
func TestListNReportsTotalPastCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 7; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f-%d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := New(root)

	entries, _, total, err := f.ListN(root, 5)
	if err != nil {
		t.Fatalf("ListN: %v", err)
	}
	if len(entries) != 5 || total != 7 {
		t.Fatalf("ListN cap=5: got %d entries, total %d; want 5 / 7", len(entries), total)
	}

	// Under the cap: total equals the slice length.
	entries, _, total, err = f.ListN(root, 50)
	if err != nil {
		t.Fatalf("ListN: %v", err)
	}
	if len(entries) != 7 || total != 7 {
		t.Fatalf("ListN cap=50: got %d entries, total %d; want 7 / 7", len(entries), total)
	}

	// List's truncated flag is derived from the same count.
	_, _, truncated, err := f.List(root, 5)
	if err != nil || !truncated {
		t.Fatalf("List cap=5: truncated=%v err=%v; want true", truncated, err)
	}
	_, _, truncated, err = f.List(root, 50)
	if err != nil || truncated {
		t.Fatalf("List cap=50: truncated=%v err=%v; want false", truncated, err)
	}
}
