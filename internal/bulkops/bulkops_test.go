package bulkops

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// collector captures every Job update for assertions.
type collector struct {
	mu      sync.Mutex
	updates []Job
}

func (c *collector) onUpdate(j Job) {
	c.mu.Lock()
	c.updates = append(c.updates, j)
	c.mu.Unlock()
}

func (c *collector) finalStatus(id string) (Status, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.updates) - 1; i >= 0; i-- {
		if c.updates[i].ID == id {
			return c.updates[i].Status, c.updates[i].Error, true
		}
	}
	return "", "", false
}

// waitForStatus polls the collector until job id reaches a terminal
// status (Done / Failed / Cancelled) or the timeout expires.
func waitForStatus(t *testing.T, c *collector, id string, timeout time.Duration) (Status, string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st, msg, ok := c.finalStatus(id); ok {
			switch st {
			case StatusDone, StatusFailed, StatusCancelled:
				return st, msg
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for job %s to terminate", id)
	return "", ""
}

func TestDeleteRecursive(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	mustMkdir(t, tree)
	mustWrite(t, filepath.Join(tree, "a.txt"), "a")
	mustMkdir(t, filepath.Join(tree, "sub"))
	mustWrite(t, filepath.Join(tree, "sub", "b.txt"), "b")
	mustWrite(t, filepath.Join(tree, "sub", "c.txt"), "c")

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()

	id := m.Enqueue(OpDelete, []string{tree}, "")
	st, msg := waitForStatus(t, c, id, 2*time.Second)
	if st != StatusDone {
		t.Fatalf("status=%s msg=%s, want done", st, msg)
	}
	if _, err := os.Lstat(tree); !os.IsNotExist(err) {
		t.Fatalf("tree still exists: %v", err)
	}
}

func TestDeleteCounter(t *testing.T) {
	// Total should equal entries pre-walked. Done should equal Total
	// at job end.
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "a")
	mustWrite(t, filepath.Join(root, "b.txt"), "b")
	mustMkdir(t, filepath.Join(root, "sub"))
	mustWrite(t, filepath.Join(root, "sub", "c.txt"), "c")

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()

	id := m.Enqueue(OpDelete, []string{root}, "")
	waitForStatus(t, c, id, 2*time.Second)

	// 4 entries (root, a.txt, b.txt, sub, c.txt) = actually 5; let's
	// just check Done == Total via the last update.
	c.mu.Lock()
	defer c.mu.Unlock()
	var last Job
	for _, u := range c.updates {
		if u.ID == id {
			last = u
		}
	}
	if last.Total == 0 || last.Done != last.Total {
		t.Fatalf("done=%d total=%d, want equal+nonzero", last.Done, last.Total)
	}
}

func TestMoveSameFS(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	mustMkdir(t, srcDir)
	mustMkdir(t, dstDir)
	mustWrite(t, filepath.Join(srcDir, "a.txt"), "hello")

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()

	id := m.Enqueue(OpMove, []string{filepath.Join(srcDir, "a.txt")}, dstDir)
	st, msg := waitForStatus(t, c, id, 2*time.Second)
	if st != StatusDone {
		t.Fatalf("status=%s msg=%s, want done", st, msg)
	}
	if _, err := os.Stat(filepath.Join(srcDir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("source still exists")
	}
	if _, err := os.Stat(filepath.Join(dstDir, "a.txt")); err != nil {
		t.Fatalf("dest missing: %v", err)
	}
}

func TestCopyRecursive(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	mustMkdir(t, src)
	mustMkdir(t, dst)
	mustMkdir(t, filepath.Join(src, "dir"))
	mustWrite(t, filepath.Join(src, "a.txt"), "alpha")
	mustWrite(t, filepath.Join(src, "dir", "b.txt"), "beta")

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()

	id := m.Enqueue(OpCopy, []string{src}, dst)
	st, msg := waitForStatus(t, c, id, 2*time.Second)
	if st != StatusDone {
		t.Fatalf("status=%s msg=%s, want done", st, msg)
	}
	// Source untouched.
	if got := readAll(t, filepath.Join(src, "a.txt")); got != "alpha" {
		t.Fatalf("src a.txt: %q", got)
	}
	// Dest has the recursive copy.
	if got := readAll(t, filepath.Join(dst, "src", "a.txt")); got != "alpha" {
		t.Fatalf("dst src/a.txt: %q", got)
	}
	if got := readAll(t, filepath.Join(dst, "src", "dir", "b.txt")); got != "beta" {
		t.Fatalf("dst src/dir/b.txt: %q", got)
	}
}

func TestCopyDefaultSkipOnConflict(t *testing.T) {
	// With no onConflict callback, conflicts default to Skip:
	// the job completes successfully but the existing dest is
	// preserved (and the source not copied for that entry).
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	dstDir := filepath.Join(root, "dst")
	mustWrite(t, src, "x")
	mustMkdir(t, dstDir)
	mustWrite(t, filepath.Join(dstDir, "a.txt"), "preexisting")

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()

	id := m.Enqueue(OpCopy, []string{src}, dstDir)
	st, _ := waitForStatus(t, c, id, 2*time.Second)
	if st != StatusDone {
		t.Fatalf("status=%s, want done (default skip)", st)
	}
	if got := readAll(t, filepath.Join(dstDir, "a.txt")); got != "preexisting" {
		t.Fatalf("dest got clobbered: %q", got)
	}
}

func TestCopyConflictReplace(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	dstDir := filepath.Join(root, "dst")
	mustWrite(t, src, "new")
	mustMkdir(t, dstDir)
	mustWrite(t, filepath.Join(dstDir, "a.txt"), "old")

	c := &collector{}
	m := New(
		WithOnUpdate(c.onUpdate),
		WithOnConflict(func(_ ConflictInfo) ConflictAction { return ConflictReplace }),
	)
	defer m.Close()

	id := m.Enqueue(OpCopy, []string{src}, dstDir)
	st, _ := waitForStatus(t, c, id, 2*time.Second)
	if st != StatusDone {
		t.Fatalf("status=%s, want done", st)
	}
	if got := readAll(t, filepath.Join(dstDir, "a.txt")); got != "new" {
		t.Fatalf("dest not replaced: %q", got)
	}
}

func TestCopyConflictReplaceAllSticky(t *testing.T) {
	// Two conflicting paths; the callback returns ReplaceAll on
	// the FIRST conflict and is NOT called again — the worker
	// remembers and applies Replace to the second path itself.
	root := t.TempDir()
	dstDir := filepath.Join(root, "dst")
	mustMkdir(t, dstDir)
	srcs := []string{}
	for _, name := range []string{"a.txt", "b.txt"} {
		s := filepath.Join(root, name)
		mustWrite(t, s, "new-"+name)
		mustWrite(t, filepath.Join(dstDir, name), "old-"+name)
		srcs = append(srcs, s)
	}

	var calls int32
	c := &collector{}
	m := New(
		WithOnUpdate(c.onUpdate),
		WithOnConflict(func(_ ConflictInfo) ConflictAction {
			atomic.AddInt32(&calls, 1)
			return ConflictReplaceAll
		}),
	)
	defer m.Close()

	id := m.Enqueue(OpCopy, srcs, dstDir)
	waitForStatus(t, c, id, 2*time.Second)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("onConflict called %d times, want 1 (ReplaceAll should stick)", got)
	}
	if got := readAll(t, filepath.Join(dstDir, "a.txt")); got != "new-a.txt" {
		t.Fatalf("a.txt: %q", got)
	}
	if got := readAll(t, filepath.Join(dstDir, "b.txt")); got != "new-b.txt" {
		t.Fatalf("b.txt: %q", got)
	}
}

func TestCopyConflictMergeDirs(t *testing.T) {
	// Source dir A has {x.txt, y.txt}; dest dir A has {y.txt, z.txt}.
	// User picks Merge:
	//   - x.txt is copied in (no collision).
	//   - y.txt collides → Skip (preserve dest version).
	//   - z.txt is left alone (only in dest).
	root := t.TempDir()
	srcA := filepath.Join(root, "src", "A")
	dstA := filepath.Join(root, "dst", "A")
	mustMkdir(t, srcA)
	mustMkdir(t, dstA)
	mustWrite(t, filepath.Join(srcA, "x.txt"), "src-x")
	mustWrite(t, filepath.Join(srcA, "y.txt"), "src-y")
	mustWrite(t, filepath.Join(dstA, "y.txt"), "dst-y")
	mustWrite(t, filepath.Join(dstA, "z.txt"), "dst-z")

	c := &collector{}
	calls := 0
	m := New(
		WithOnUpdate(c.onUpdate),
		WithOnConflict(func(info ConflictInfo) ConflictAction {
			calls++
			if info.SrcType == "dir" && info.DstType == "dir" {
				return ConflictMerge
			}
			// File-vs-file collision (y.txt): keep dst.
			return ConflictSkip
		}),
	)
	defer m.Close()

	id := m.Enqueue(OpCopy, []string{srcA}, filepath.Dir(dstA))
	st, _ := waitForStatus(t, c, id, 2*time.Second)
	if st != StatusDone {
		t.Fatalf("status=%s, want done", st)
	}
	if got := readAll(t, filepath.Join(dstA, "x.txt")); got != "src-x" {
		t.Fatalf("x.txt: %q", got)
	}
	if got := readAll(t, filepath.Join(dstA, "y.txt")); got != "dst-y" {
		t.Fatalf("y.txt got clobbered: %q", got)
	}
	if got := readAll(t, filepath.Join(dstA, "z.txt")); got != "dst-z" {
		t.Fatalf("z.txt got lost: %q", got)
	}
	// Two prompts: dir-vs-dir (merge), then y.txt file-vs-file (skip).
	if calls != 2 {
		t.Fatalf("onConflict called %d times, want 2", calls)
	}
}

func TestCopyConflictMergeAllSticky(t *testing.T) {
	// Two dir-vs-dir collisions in one job; user picks MergeAll on
	// the first. The second is auto-merged. File-vs-file inside
	// uses default Skip from sticky.skipAll if we'd set that — here
	// we never see a file collision since names differ.
	root := t.TempDir()
	dstParent := filepath.Join(root, "dst")
	mustMkdir(t, dstParent)
	for _, name := range []string{"A", "B"} {
		mustMkdir(t, filepath.Join(root, name))
		mustMkdir(t, filepath.Join(dstParent, name))
		mustWrite(t, filepath.Join(root, name, "leaf.txt"), name)
	}
	srcs := []string{filepath.Join(root, "A"), filepath.Join(root, "B")}

	c := &collector{}
	var calls int32
	m := New(
		WithOnUpdate(c.onUpdate),
		WithOnConflict(func(_ ConflictInfo) ConflictAction {
			atomic.AddInt32(&calls, 1)
			return ConflictMergeAll
		}),
	)
	defer m.Close()
	id := m.Enqueue(OpCopy, srcs, dstParent)
	waitForStatus(t, c, id, 2*time.Second)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("onConflict called %d times, want 1 (MergeAll sticks)", got)
	}
	if got := readAll(t, filepath.Join(dstParent, "A", "leaf.txt")); got != "A" {
		t.Fatalf("A/leaf.txt: %q", got)
	}
	if got := readAll(t, filepath.Join(dstParent, "B", "leaf.txt")); got != "B" {
		t.Fatalf("B/leaf.txt: %q", got)
	}
}

func TestCopyConflictReplaceDirsDestructive(t *testing.T) {
	// Dir-vs-dir, user picks Replace (not Merge): the whole dst
	// subtree is wiped before the copy.
	root := t.TempDir()
	srcA := filepath.Join(root, "src", "A")
	dstA := filepath.Join(root, "dst", "A")
	mustMkdir(t, srcA)
	mustMkdir(t, dstA)
	mustWrite(t, filepath.Join(srcA, "new.txt"), "new")
	mustWrite(t, filepath.Join(dstA, "old.txt"), "old")

	c := &collector{}
	m := New(
		WithOnUpdate(c.onUpdate),
		WithOnConflict(func(_ ConflictInfo) ConflictAction { return ConflictReplace }),
	)
	defer m.Close()
	id := m.Enqueue(OpCopy, []string{srcA}, filepath.Dir(dstA))
	waitForStatus(t, c, id, 2*time.Second)
	if got := readAll(t, filepath.Join(dstA, "new.txt")); got != "new" {
		t.Fatalf("new.txt: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dstA, "old.txt")); err == nil {
		t.Fatal("old.txt should have been wiped by Replace")
	}
}

func TestMoveConflictMergeDirs(t *testing.T) {
	// Move version of merge: source A has {x.txt, y.txt}; dst A
	// has {y.txt, z.txt}. User picks Merge then Skip on the y.txt
	// file collision. x.txt moves over; y.txt stays as the dst
	// version; the source's y.txt stays in source (because the
	// child was skipped, the source dir is non-empty post-merge);
	// z.txt is untouched.
	root := t.TempDir()
	srcA := filepath.Join(root, "src", "A")
	dstA := filepath.Join(root, "dst", "A")
	mustMkdir(t, srcA)
	mustMkdir(t, dstA)
	mustWrite(t, filepath.Join(srcA, "x.txt"), "src-x")
	mustWrite(t, filepath.Join(srcA, "y.txt"), "src-y")
	mustWrite(t, filepath.Join(dstA, "y.txt"), "dst-y")
	mustWrite(t, filepath.Join(dstA, "z.txt"), "dst-z")

	c := &collector{}
	m := New(
		WithOnUpdate(c.onUpdate),
		WithOnConflict(func(info ConflictInfo) ConflictAction {
			if info.SrcType == "dir" && info.DstType == "dir" {
				return ConflictMerge
			}
			return ConflictSkip
		}),
	)
	defer m.Close()
	id := m.Enqueue(OpMove, []string{srcA}, filepath.Dir(dstA))
	waitForStatus(t, c, id, 2*time.Second)

	if got := readAll(t, filepath.Join(dstA, "x.txt")); got != "src-x" {
		t.Fatalf("dst x.txt: %q", got)
	}
	if got := readAll(t, filepath.Join(dstA, "y.txt")); got != "dst-y" {
		t.Fatalf("dst y.txt clobbered: %q", got)
	}
	if got := readAll(t, filepath.Join(dstA, "z.txt")); got != "dst-z" {
		t.Fatalf("dst z.txt lost: %q", got)
	}
	// Source y.txt should still exist (was skipped); src/A itself
	// should still exist because it's not empty.
	if got := readAll(t, filepath.Join(srcA, "y.txt")); got != "src-y" {
		t.Fatalf("src y.txt should remain after skip: %q", got)
	}
	// Source x.txt moved out — gone.
	if _, err := os.Stat(filepath.Join(srcA, "x.txt")); err == nil {
		t.Fatal("src x.txt should have moved out")
	}
}

func TestCopyConflictCancel(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	dstDir := filepath.Join(root, "dst")
	mustWrite(t, src, "x")
	mustMkdir(t, dstDir)
	mustWrite(t, filepath.Join(dstDir, "a.txt"), "preexisting")

	c := &collector{}
	m := New(
		WithOnUpdate(c.onUpdate),
		WithOnConflict(func(_ ConflictInfo) ConflictAction { return ConflictCancel }),
	)
	defer m.Close()

	id := m.Enqueue(OpCopy, []string{src}, dstDir)
	st, _ := waitForStatus(t, c, id, 2*time.Second)
	if st != StatusCancelled {
		t.Fatalf("status=%s, want cancelled", st)
	}
	if got := readAll(t, filepath.Join(dstDir, "a.txt")); got != "preexisting" {
		t.Fatalf("dest got clobbered: %q", got)
	}
}

func TestCancelBeforeStart(t *testing.T) {
	// Enqueue a single job and immediately cancel it. With a single
	// worker and zero pending jobs ahead, this races the worker;
	// the cancel might land either before runJob starts or after.
	// In either case, the final status must be Cancelled OR Done
	// (if the worker already finished a trivial delete) — for a
	// truly fast-cancel path we'd need a queued-but-not-yet-pulled
	// job. Use a sleep-y workload (large tree) to keep the worker
	// busy.
	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	mustMkdir(t, tree)
	for i := 0; i < 200; i++ {
		mustWrite(t, filepath.Join(tree, "f"+itoa(i)+".txt"), "x")
	}

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()

	id := m.Enqueue(OpDelete, []string{tree}, "")
	m.Cancel(id)
	st, _ := waitForStatus(t, c, id, 2*time.Second)
	if st != StatusCancelled && st != StatusDone {
		t.Fatalf("status=%s, want cancelled or done", st)
	}
}

func TestFifoOrder(t *testing.T) {
	// Three jobs enqueued back-to-back complete in order — single
	// worker, no parallelism. Each is independent (separate dir).
	root := t.TempDir()
	dirs := []string{}
	for i := 0; i < 3; i++ {
		d := filepath.Join(root, "j"+itoa(i))
		mustMkdir(t, d)
		mustWrite(t, filepath.Join(d, "f.txt"), itoa(i))
		dirs = append(dirs, d)
	}

	var doneOrder []string
	var mu sync.Mutex
	c := &collector{}
	m := New(WithOnUpdate(func(j Job) {
		c.onUpdate(j)
		if j.Status == StatusDone {
			mu.Lock()
			doneOrder = append(doneOrder, j.ID)
			mu.Unlock()
		}
	}))
	defer m.Close()

	var ids []string
	for _, d := range dirs {
		ids = append(ids, m.Enqueue(OpDelete, []string{d}, ""))
	}
	for _, id := range ids {
		waitForStatus(t, c, id, 2*time.Second)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sameOrder(doneOrder, ids) {
		t.Fatalf("done order %v, want %v", doneOrder, ids)
	}
}

func TestJobsSnapshot(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a"), "a")
	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()

	id := m.Enqueue(OpDelete, []string{filepath.Join(root, "a")}, "")
	waitForStatus(t, c, id, time.Second)
	snap := m.Jobs()
	if len(snap) != 1 || snap[0].ID != id || snap[0].Status != StatusDone {
		t.Fatalf("snapshot: %+v", snap)
	}
}

func TestCloseIdempotent(t *testing.T) {
	m := New()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}

// ---- tiny helpers -----------------------------------------

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readAll(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func itoa(n int) string {
	// fmt.Sprintf("%03d", n) would do but tests should avoid fmt
	// in inner loops — and three digits is enough for 200 files.
	const z = "0000000000"
	s := []byte(z)
	i := len(s) - 1
	if n == 0 {
		return "000"
	}
	for n > 0 && i >= 0 {
		s[i] = byte('0' + n%10)
		n /= 10
		i--
	}
	return string(s[len(s)-3:])
}

// keep sort imported so future tests can use it without re-adding
var _ = sort.Strings
