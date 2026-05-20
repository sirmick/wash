package fswatch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// drainOne waits up to timeout for one event on sub. Returns the
// event and true on success; zero event and false on timeout.
func drainOne(t *testing.T, sub *Sub, timeout time.Duration) (Event, bool) {
	t.Helper()
	select {
	case ev := <-sub.Events():
		return ev, true
	case <-time.After(timeout):
		return Event{}, false
	}
}

// expectOp waits for any event matching the wanted op for the
// expected path. Discards intervening events (e.g. CHMOD chatter on
// some platforms) up to a total timeout.
func expectOp(t *testing.T, sub *Sub, wantOp Op, wantPath string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ev, ok := drainOne(t, sub, time.Until(deadline))
		if !ok {
			t.Fatalf("timed out waiting for op=%s path=%s", wantOp, wantPath)
		}
		if ev.Op == wantOp && ev.Path == wantPath {
			return
		}
	}
	t.Fatalf("never saw op=%s path=%s within %v", wantOp, wantPath, timeout)
}

func TestWatchFileCreatedAndDeleted(t *testing.T) {
	dir := t.TempDir()
	m, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	sub, err := m.Watch(dir)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer sub.Close()

	target := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	expectOp(t, sub, OpCreated, target, time.Second)

	if err := os.Remove(target); err != nil {
		t.Fatalf("remove: %v", err)
	}
	expectOp(t, sub, OpDeleted, target, time.Second)
}

func TestRefcountedWatch(t *testing.T) {
	// Two subs to the same path share one underlying fsnotify watch.
	// Closing one of them must NOT stop events for the other.
	dir := t.TempDir()
	m, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	subA, err := m.Watch(dir)
	if err != nil {
		t.Fatalf("Watch A: %v", err)
	}
	subB, err := m.Watch(dir)
	if err != nil {
		t.Fatalf("Watch B: %v", err)
	}
	paths, subs := m.Stats()
	if paths != 1 || subs != 2 {
		t.Fatalf("after both Watch: paths=%d subs=%d, want 1/2", paths, subs)
	}

	subA.Close()
	paths, subs = m.Stats()
	if paths != 1 || subs != 1 {
		t.Fatalf("after subA.Close: paths=%d subs=%d, want 1/1 (refcount keeps watch alive)", paths, subs)
	}

	// subB should still receive events.
	target := filepath.Join(dir, "afterA.txt")
	if err := os.WriteFile(target, []byte("y"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	expectOp(t, subB, OpCreated, target, time.Second)

	subB.Close()
	paths, subs = m.Stats()
	if paths != 0 || subs != 0 {
		t.Fatalf("after both closed: paths=%d subs=%d, want 0/0", paths, subs)
	}
}

func TestSubCloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	m, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	sub, err := m.Watch(dir)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	sub.Close()
	sub.Close() // must not panic / double-close channels
	sub.Close()
}

func TestManagerCloseIsIdempotent(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}
}

func TestWatchAfterCloseFails(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.Close()
	if _, err := m.Watch(t.TempDir()); err != ErrClosed {
		t.Fatalf("Watch after Close: want ErrClosed, got %v", err)
	}
}

func TestManagerCloseDrainsSubs(t *testing.T) {
	dir := t.TempDir()
	m, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sub, err := m.Watch(dir)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Sub's channel must close so for-range readers terminate.
	select {
	case _, ok := <-sub.Events():
		if ok {
			t.Fatalf("Events not closed after Manager.Close")
		}
	case <-time.After(time.Second):
		t.Fatalf("Events channel still open 1s after Close")
	}
	paths, subs := m.Stats()
	if paths != 0 || subs != 0 {
		t.Fatalf("after Manager.Close: paths=%d subs=%d, want 0/0", paths, subs)
	}
}

func TestNoLeakAfterManyWatchUnwatch(t *testing.T) {
	// 100 cycles of subscribe→unsubscribe to the same dir must leave
	// zero outstanding state. Catches refcount bugs where Close fails
	// to remove the path from the map.
	dir := t.TempDir()
	m, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	for i := 0; i < 100; i++ {
		sub, err := m.Watch(dir)
		if err != nil {
			t.Fatalf("cycle %d Watch: %v", i, err)
		}
		sub.Close()
	}
	paths, subs := m.Stats()
	if paths != 0 || subs != 0 {
		t.Fatalf("leak after 100 cycles: paths=%d subs=%d", paths, subs)
	}
}

func TestWatchedDirDeletedFiresEventAndDropsSub(t *testing.T) {
	// When the watched directory itself is removed, the sub should
	// receive an OpDeleted for that path. The underlying fsnotify
	// watch auto-detaches; our refcount entry remains but consumers
	// can call Sub.Close() to release it (idempotent).
	parent := t.TempDir()
	target := filepath.Join(parent, "doomed")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	m, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	sub, err := m.Watch(target)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if err := os.Remove(target); err != nil {
		t.Fatalf("remove: %v", err)
	}
	expectOp(t, sub, OpDeleted, target, time.Second)
	sub.Close()
}
