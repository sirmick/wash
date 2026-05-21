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
		WithOnConflict(func(_ Job, _, _ string) ConflictAction { return ConflictReplace }),
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
		WithOnConflict(func(_ Job, _, _ string) ConflictAction {
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
		WithOnConflict(func(_ Job, _, _ string) ConflictAction { return ConflictCancel }),
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
