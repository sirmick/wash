package fm

import (
	"os"
	"path/filepath"
	"testing"

	wfs "github.com/sirmick/wash/internal/fs"
)

func seedSearchTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(root, "Notes.txt"), []byte("n"), 0o644))
	must(os.WriteFile(filepath.Join(root, "other.md"), []byte("o"), 0o644))
	must(os.MkdirAll(filepath.Join(root, "docs", "deep"), 0o755))
	must(os.WriteFile(filepath.Join(root, "docs", "notes-2.txt"), []byte("n"), 0o644))
	must(os.WriteFile(filepath.Join(root, "docs", "deep", "notes-3.txt"), []byte("n"), 0o644))
	must(os.MkdirAll(filepath.Join(root, "zz-notes-dir"), 0o755))
	return root
}

func collect(fsys *wfs.FS, root, q string, o searchOpts, cancelled func() bool) ([]searchHit, searchStats) {
	var hits []searchHit
	st := searchWalk(fsys, root, q, o, cancelled, func(h searchHit) { hits = append(hits, h) })
	return hits, st
}

func rels(hits []searchHit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Rel)
	}
	return out
}

func TestSearchWalkFindsByCaseInsensitiveSubstringInDirOrder(t *testing.T) {
	root := seedSearchTree(t)
	hits, st := collect(wfs.New(root), root, "NOTES", searchOpts{MaxDepth: 16, MaxVisited: 1000, MaxHits: 100}, nil)
	want := []string{"Notes.txt", "zz-notes-dir", "docs/notes-2.txt", "docs/deep/notes-3.txt"}
	got := rels(hits)
	if len(got) != len(want) {
		t.Fatalf("hits = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hits = %v, want %v", got, want)
		}
	}
	if st.Hits != 4 || st.Truncated || st.Cancelled || st.Skipped != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if hits[1].Entry.Type != "dir" || hits[0].Entry.Type != "file" {
		t.Fatalf("entry types not carried: %+v %+v", hits[0].Entry, hits[1].Entry)
	}
}

func TestSearchWalkEmptyQueryIsNoop(t *testing.T) {
	root := seedSearchTree(t)
	hits, st := collect(wfs.New(root), root, "   ", searchOpts{MaxDepth: 16, MaxVisited: 1000, MaxHits: 100}, nil)
	if len(hits) != 0 || st.Visited != 0 {
		t.Fatalf("expected no work, got %d hits, %+v", len(hits), st)
	}
}

func TestSearchWalkBoundsDepth(t *testing.T) {
	root := seedSearchTree(t)
	hits, _ := collect(wfs.New(root), root, "notes", searchOpts{MaxDepth: 2, MaxVisited: 1000, MaxHits: 100}, nil)
	for _, r := range rels(hits) {
		if r == "docs/deep/notes-3.txt" {
			t.Fatalf("depth cap not honoured: %v", rels(hits))
		}
	}
	if len(hits) != 3 {
		t.Fatalf("hits = %v", rels(hits))
	}
}

func TestSearchWalkCapsHitsAndVisited(t *testing.T) {
	root := seedSearchTree(t)
	fsys := wfs.New(root)
	hits, st := collect(fsys, root, "notes", searchOpts{MaxDepth: 16, MaxVisited: 1000, MaxHits: 2}, nil)
	if len(hits) != 2 || !st.Truncated {
		t.Fatalf("hit cap: hits=%v stats=%+v", rels(hits), st)
	}
	_, st = collect(fsys, root, "notes", searchOpts{MaxDepth: 16, MaxVisited: 3, MaxHits: 100}, nil)
	if !st.Truncated || st.Visited != 3 {
		t.Fatalf("visited cap: stats=%+v", st)
	}
}

func TestSearchWalkCancels(t *testing.T) {
	root := seedSearchTree(t)
	n := 0
	cancelled := func() bool { n++; return n > 1 }
	hits, st := collect(wfs.New(root), root, "notes", searchOpts{MaxDepth: 16, MaxVisited: 1000, MaxHits: 100}, cancelled)
	if !st.Cancelled {
		t.Fatalf("expected cancelled, stats=%+v", st)
	}
	if len(hits) >= 4 {
		t.Fatalf("cancel did not stop the walk early: %v", rels(hits))
	}
}

func TestSearchWalkSkipsUnreadableAndDoesNotFollowSymlinkedDirs(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	root := seedSearchTree(t)
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "notes-hidden.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	// A symlink loop back to the root: matched as an entry, never descended.
	if err := os.Symlink(root, filepath.Join(root, "notes-loop")); err != nil {
		t.Fatal(err)
	}
	hits, st := collect(wfs.New(root), root, "notes", searchOpts{MaxDepth: 16, MaxVisited: 10_000, MaxHits: 100}, nil)
	if st.Skipped != 1 {
		t.Fatalf("expected 1 skipped dir, stats=%+v", st)
	}
	for _, r := range rels(hits) {
		if r == "locked/notes-hidden.txt" || filepath.HasPrefix(r, "notes-loop/") {
			t.Fatalf("walked where it should not: %v", rels(hits))
		}
	}
	if st.Truncated {
		t.Fatalf("symlink loop was followed: stats=%+v", st)
	}
}
