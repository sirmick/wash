package bulkops

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// "Keep both" is the answer to a copy/move collision that loses nothing:
// the source lands beside the existing entry under a free "(copy)" name
// instead of replacing, merging or being skipped.

func TestCopyKeepBothWritesASiblingCopy(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	mustMkdir(t, src)
	mustMkdir(t, dst)
	mustWrite(t, filepath.Join(src, "a.txt"), "from-src")
	mustWrite(t, filepath.Join(dst, "a.txt"), "already-here")

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate), WithOnConflict(func(ConflictInfo) ConflictAction {
		return ConflictKeepBoth
	}))
	defer m.Close()

	id := mustEnqueue(t, m, OpCopy, []string{filepath.Join(src, "a.txt")}, dst)
	if st, msg := waitForStatus(t, c, id, 2*time.Second); st != StatusDone {
		t.Fatalf("status=%s msg=%s", st, msg)
	}
	assertFile(t, filepath.Join(dst, "a.txt"), "already-here")
	assertFile(t, filepath.Join(dst, "a (copy).txt"), "from-src")
}

func TestCopyKeepBothCountsPastExistingCopies(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	mustMkdir(t, src)
	mustMkdir(t, dst)
	mustWrite(t, filepath.Join(src, "a.txt"), "from-src")
	mustWrite(t, filepath.Join(dst, "a.txt"), "0")
	mustWrite(t, filepath.Join(dst, "a (copy).txt"), "1")
	mustWrite(t, filepath.Join(dst, "a (copy 2).txt"), "2")

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate), WithOnConflict(func(ConflictInfo) ConflictAction {
		return ConflictKeepBoth
	}))
	defer m.Close()

	id := mustEnqueue(t, m, OpCopy, []string{filepath.Join(src, "a.txt")}, dst)
	if st, msg := waitForStatus(t, c, id, 2*time.Second); st != StatusDone {
		t.Fatalf("status=%s msg=%s", st, msg)
	}
	assertFile(t, filepath.Join(dst, "a (copy 3).txt"), "from-src")
}

func TestMoveKeepBothRenamesAndLeavesTheOriginal(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	mustMkdir(t, src)
	mustMkdir(t, dst)
	mustWrite(t, filepath.Join(src, "a.txt"), "moved")
	mustWrite(t, filepath.Join(dst, "a.txt"), "kept")

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate), WithOnConflict(func(ConflictInfo) ConflictAction {
		return ConflictKeepBoth
	}))
	defer m.Close()

	id := mustEnqueue(t, m, OpMove, []string{filepath.Join(src, "a.txt")}, dst)
	if st, msg := waitForStatus(t, c, id, 2*time.Second); st != StatusDone {
		t.Fatalf("status=%s msg=%s", st, msg)
	}
	assertFile(t, filepath.Join(dst, "a.txt"), "kept")
	assertFile(t, filepath.Join(dst, "a (copy).txt"), "moved")
	if _, err := os.Lstat(filepath.Join(src, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("source survived the move: %v", err)
	}
}

func TestKeepBothAllIsStickyForTheRestOfTheJob(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	mustMkdir(t, src)
	mustMkdir(t, dst)
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		mustWrite(t, filepath.Join(src, n), "new-"+n)
		mustWrite(t, filepath.Join(dst, n), "old-"+n)
	}

	var asked int
	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate), WithOnConflict(func(ConflictInfo) ConflictAction {
		asked++
		return ConflictKeepBothAll
	}))
	defer m.Close()

	id := mustEnqueue(t, m, OpCopy, []string{
		filepath.Join(src, "a.txt"), filepath.Join(src, "b.txt"), filepath.Join(src, "c.txt"),
	}, dst)
	if st, msg := waitForStatus(t, c, id, 2*time.Second); st != StatusDone {
		t.Fatalf("status=%s msg=%s", st, msg)
	}
	if asked != 1 {
		t.Fatalf("prompted %d times, want 1 (the rest are sticky)", asked)
	}
	for _, n := range []string{"a", "b", "c"} {
		assertFile(t, filepath.Join(dst, n+".txt"), "old-"+n+".txt")
		assertFile(t, filepath.Join(dst, n+" (copy).txt"), "new-"+n+".txt")
	}
}

func TestKeepBothOnADirectoryCopiesTheWholeTree(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	mustMkdir(t, filepath.Join(src, "docs", "sub"))
	mustWrite(t, filepath.Join(src, "docs", "sub", "deep.txt"), "deep")
	mustMkdir(t, filepath.Join(dst, "docs"))
	mustWrite(t, filepath.Join(dst, "docs", "existing.txt"), "existing")

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate), WithOnConflict(func(ConflictInfo) ConflictAction {
		return ConflictKeepBoth
	}))
	defer m.Close()

	id := mustEnqueue(t, m, OpCopy, []string{filepath.Join(src, "docs")}, dst)
	if st, msg := waitForStatus(t, c, id, 2*time.Second); st != StatusDone {
		t.Fatalf("status=%s msg=%s", st, msg)
	}
	assertFile(t, filepath.Join(dst, "docs", "existing.txt"), "existing")
	assertFile(t, filepath.Join(dst, "docs (copy)", "sub", "deep.txt"), "deep")
}

// Job.Names: fm's Duplicate copies a source into the folder it already
// lives in, which only works because the destination name differs.

func TestEnqueueAsCopiesUnderTheGivenName(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "notes.txt"), "body")
	mustMkdir(t, filepath.Join(root, "photos"))
	mustWrite(t, filepath.Join(root, "photos", "p.txt"), "pic")

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()

	id, err := m.EnqueueAs(OpCopy,
		[]string{filepath.Join(root, "notes.txt"), filepath.Join(root, "photos")},
		root,
		[]string{"notes (copy).txt", "photos (copy)"})
	if err != nil {
		t.Fatalf("EnqueueAs: %v", err)
	}
	if st, msg := waitForStatus(t, c, id, 2*time.Second); st != StatusDone {
		t.Fatalf("status=%s msg=%s", st, msg)
	}
	assertFile(t, filepath.Join(root, "notes (copy).txt"), "body")
	assertFile(t, filepath.Join(root, "photos (copy)", "p.txt"), "pic")
	// The originals are untouched.
	assertFile(t, filepath.Join(root, "notes.txt"), "body")
}

func TestValidateNamedPathsRejectsBadNames(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	mustWrite(t, src, "a")
	if err := ValidateNamedPaths(OpCopy, []string{src}, root, []string{"a", "b"}); err == nil {
		t.Fatal("mismatched lengths accepted")
	}
	if err := ValidateNamedPaths(OpCopy, []string{src}, root, []string{"sub/a.txt"}); err == nil {
		t.Fatal("a path separator in a name accepted")
	}
	if err := ValidateNamedPaths(OpCopy, []string{src}, root, []string{".."}); err == nil {
		t.Fatal("\"..\" accepted as a name")
	}
	// Same folder, same name is still the no-op it always was.
	if err := ValidateNamedPaths(OpCopy, []string{src}, root, nil); err == nil {
		t.Fatal("copy into the source's own folder accepted")
	}
	// Same folder, different name is exactly Duplicate.
	if err := ValidateNamedPaths(OpCopy, []string{src}, root, []string{"a (copy).txt"}); err != nil {
		t.Fatalf("duplicate rejected: %v", err)
	}
}

func assertFile(t *testing.T, p, want string) {
	t.Helper()
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", p, got, want)
	}
}
