package connect

import (
	"testing"
)

// TestBookmarksRoundTrip covers the disk persistence wash-connect's
// bookmarks rely on (docs/REMOTE.md §6.1): unlike router-memory FE state,
// bookmarks must survive a router restart, so they live in a file under
// $XDG_CONFIG_HOME/wash/. A missing file reads as empty (no bookmarks
// yet), and a save → load round-trips the entries.
func TestBookmarksRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// No file yet → empty, not an error.
	got, err := loadBookmarks()
	if err != nil {
		t.Fatalf("load (missing): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}

	want := []Bookmark{
		{Host: "alice@build01", AppID: "com.wash.term", Label: "term · build01"},
		{Host: "root@db", Label: "db"},
	}
	if err := saveBookmarks(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err = loadBookmarks()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bookmark[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// A second save replaces (not appends).
	if err := saveBookmarks([]Bookmark{{Host: "solo@host"}}); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	got, _ = loadBookmarks()
	if len(got) != 1 || got[0].Host != "solo@host" {
		t.Fatalf("expected single replaced bookmark, got %v", got)
	}
}
