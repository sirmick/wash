package bulkops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The rejection shapes ValidatePaths guards. Each one is a verified
// data-loss path in the unguarded worker (see the ValidatePaths doc
// comment), so besides the error text every test also asserts the
// manager queued NOTHING — no job, no update — and the fixture is
// untouched on disk.

// rejects runs Enqueue and asserts it was refused with a message
// containing want, that no job was created, and that nothing was
// emitted.
func rejects(t *testing.T, op Op, paths []string, dest, want string) {
	t.Helper()
	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()
	id, err := m.Enqueue(op, paths, dest)
	if err == nil {
		t.Fatalf("%s %v -> %q: accepted as %s, want rejection", op, paths, dest, id)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("%s %v -> %q: error %q, want it to contain %q", op, paths, dest, err, want)
	}
	if id != "" {
		t.Fatalf("rejected enqueue returned id %q", id)
	}
	if n := len(m.Jobs()); n != 0 {
		t.Fatalf("rejected enqueue left %d job(s) in the queue", n)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.updates) != 0 {
		t.Fatalf("rejected enqueue emitted %d update(s)", len(c.updates))
	}
}

// fixture builds root/dir/{a.txt, sub/b.txt} and returns (dir, a.txt).
func fixture(t *testing.T) (dir, file string) {
	t.Helper()
	root := t.TempDir()
	dir = filepath.Join(root, "dir")
	mustMkdir(t, filepath.Join(dir, "sub"))
	file = filepath.Join(dir, "a.txt")
	mustWrite(t, file, "keep me")
	mustWrite(t, filepath.Join(dir, "sub", "b.txt"), "b")
	return dir, file
}

func TestValidateRejectsSameFolderPaste(t *testing.T) {
	// Ctrl+C / Ctrl+V in the folder the file lives in. Unguarded this
	// resolves dst == src, prompts, and a Replace answer RemoveAll's
	// the source.
	for _, op := range []Op{OpCopy, OpMove} {
		t.Run(string(op), func(t *testing.T) {
			dir, file := fixture(t)
			rejects(t, op, []string{file}, dir, file+" is already in "+dir)
			if got := readAll(t, file); got != "keep me" {
				t.Fatalf("source damaged: %q", got)
			}
		})
	}
}

func TestValidateRejectsSameFolderPasteOfDir(t *testing.T) {
	dir, _ := fixture(t)
	sub := filepath.Join(dir, "sub")
	rejects(t, OpMove, []string{sub}, dir, sub+" is already in "+dir)
}

func TestValidateRejectsDestEqualsSource(t *testing.T) {
	for _, op := range []Op{OpCopy, OpMove} {
		t.Run(string(op), func(t *testing.T) {
			dir, _ := fixture(t)
			rejects(t, op, []string{dir}, dir, "cannot "+string(op)+" "+dir+" into itself")
		})
	}
}

func TestValidateRejectsIntoOwnSubtree(t *testing.T) {
	// Copy a folder, paste inside it: copyTree would recurse into its
	// own output until ENAMETOOLONG. Move: EINVAL from rename, or a
	// cross-device fallback that copies forever then deletes.
	for _, op := range []Op{OpCopy, OpMove} {
		t.Run(string(op), func(t *testing.T) {
			dir, _ := fixture(t)
			sub := filepath.Join(dir, "sub")
			rejects(t, op, []string{dir}, sub, "cannot "+string(op)+" "+dir+" into its own subfolder "+sub)
		})
	}
}

func TestValidateRejectsWholeJobOnOneOffender(t *testing.T) {
	// Reject-whole-job: one bad path in a multi-select refuses the
	// lot — the good sibling must not have been queued either.
	dir, file := fixture(t)
	root := filepath.Dir(dir)
	other := filepath.Join(root, "other.txt")
	mustWrite(t, other, "other")
	rejects(t, OpCopy, []string{other, file}, dir, file+" is already in "+dir)
	if _, err := os.Lstat(filepath.Join(dir, "other.txt")); !os.IsNotExist(err) {
		t.Fatal("good sibling was copied despite the rejection")
	}
}

func TestValidateSubtreeCheckIsSeparatorAware(t *testing.T) {
	// /root/dir-2 is NOT inside /root/dir — a naive HasPrefix says it is.
	dir, _ := fixture(t)
	sibling := dir + "-2"
	mustMkdir(t, sibling)
	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()
	id := mustEnqueue(t, m, OpCopy, []string{dir}, sibling)
	if st, msg := waitForStatus(t, c, id, 2*time.Second); st != StatusDone {
		t.Fatalf("status=%s msg=%s, want done", st, msg)
	}
	if got := readAll(t, filepath.Join(sibling, "dir", "a.txt")); got != "keep me" {
		t.Fatalf("copy into sibling: %q", got)
	}
}

func TestValidateUncleanedPathsStillMatch(t *testing.T) {
	// Callers spell paths with trailing slashes and dot segments; the
	// guard must see through them.
	dir, file := fixture(t)
	rejects(t, OpCopy, []string{file}, dir+string(filepath.Separator), " is already in ")
	rejects(t, OpMove, []string{filepath.Join(dir, "sub", "..", "a.txt")}, dir, " is already in ")
	rejects(t, OpCopy, []string{dir}, filepath.Join(dir, ".", "sub"), "into its own subfolder")
}

func TestValidateSameFolderViaSymlinkAlias(t *testing.T) {
	// /alias -> /dir. Pasting /alias/a.txt into /dir is lexically a copy
	// between two folders but physically dst == src: the collision
	// prompt fires and Replace deletes the only copy. The resolved-pair
	// check catches it, in both directions.
	dir, file := fixture(t)
	alias := filepath.Join(filepath.Dir(dir), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	rejects(t, OpCopy, []string{filepath.Join(alias, "a.txt")}, dir, " is already in ")
	rejects(t, OpMove, []string{file}, alias, " is already in ")
	if got := readAll(t, file); got != "keep me" {
		t.Fatalf("source damaged: %q", got)
	}
}

func TestValidateIntoOwnSubtreeViaSymlinkAlias(t *testing.T) {
	// /alias -> /dir; copy /dir into /alias/sub is physically /dir into
	// /dir/sub.
	dir, _ := fixture(t)
	alias := filepath.Join(filepath.Dir(dir), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	rejects(t, OpCopy, []string{dir}, filepath.Join(alias, "sub"), "into its own subfolder")
	// The reverse is NOT a rejection: /alias is a symlink ENTRY, and
	// moving that entry into /dir/sub just creates /dir/sub/alias — the
	// link, not the tree. Only src's parent is resolved, never src.
	if err := ValidatePaths(OpMove, []string{alias}, filepath.Join(dir, "sub")); err != nil {
		t.Fatalf("moving the symlink entry into its target's subtree should be allowed: %v", err)
	}
}

func TestValidateSymlinkEntryNextToTargetIsAllowed(t *testing.T) {
	// A symlink is copied as a directory entry, not dereferenced:
	// pasting /root/link (-> /dir/a.txt) into /dir creates /dir/link
	// and must NOT be mistaken for a same-folder paste of a.txt.
	dir, file := fixture(t)
	link := filepath.Join(filepath.Dir(dir), "link")
	if err := os.Symlink(file, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()
	id := mustEnqueue(t, m, OpCopy, []string{link}, dir)
	if st, msg := waitForStatus(t, c, id, 2*time.Second); st != StatusDone {
		t.Fatalf("status=%s msg=%s, want done", st, msg)
	}
	if target, err := os.Readlink(filepath.Join(dir, "link")); err != nil || target != file {
		t.Fatalf("link not copied as a link: %q %v", target, err)
	}
	if got := readAll(t, file); got != "keep me" {
		t.Fatalf("target damaged: %q", got)
	}
}

func TestValidateRejectsEmptyAndDestless(t *testing.T) {
	rejects(t, OpCopy, nil, t.TempDir(), "nothing to copy")
	rejects(t, OpDelete, []string{}, "", "nothing to delete")
	rejects(t, OpMove, []string{filepath.Join(t.TempDir(), "x")}, "", "move requires a destination")
}

// Positive controls: the ordinary shapes every op is for still queue
// and complete. (TestMoveSameFS / TestCopyRecursive / TestDeleteRecursive
// cover the full runs; these pin that the guard itself passes them.)
func TestValidateAcceptsOrdinaryJobs(t *testing.T) {
	dir, file := fixture(t)
	dst := filepath.Join(filepath.Dir(dir), "dst")
	mustMkdir(t, dst)
	for _, tc := range []struct {
		op    Op
		paths []string
		dest  string
	}{
		{OpCopy, []string{file}, dst},
		{OpMove, []string{file, filepath.Join(dir, "sub")}, dst},
		{OpCopy, []string{dir}, dst},
		{OpDelete, []string{file}, ""},
		{OpDelete, []string{dir}, "/anything"}, // delete ignores dest
	} {
		if err := ValidatePaths(tc.op, tc.paths, tc.dest); err != nil {
			t.Errorf("%s %v -> %q: unexpected rejection: %v", tc.op, tc.paths, tc.dest, err)
		}
	}
}
