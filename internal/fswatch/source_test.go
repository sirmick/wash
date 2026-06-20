package fswatch

import "testing"

func recvOp(t *testing.T, s Subscription) Event {
	t.Helper()
	select {
	case ev, ok := <-s.Events():
		if !ok {
			t.Fatal("events channel closed unexpectedly")
		}
		return ev
	default:
		t.Fatalf("expected an event on %s, got none", s.Path())
		return Event{}
	}
}

// TestFeedFanout proves a pushed event reaches both the directory watcher (the
// common case) and the exact-path watcher — the same delivery rule the local
// Manager uses, so a remote feed is indistinguishable from inotify.
func TestFeedFanout(t *testing.T) {
	f := NewFeed()
	defer f.Close()

	dir, err := f.Watch("/d")
	if err != nil {
		t.Fatalf("watch dir: %v", err)
	}
	file, err := f.Watch("/d/file")
	if err != nil {
		t.Fatalf("watch file: %v", err)
	}

	// A change to /d/file fans out to subscribers of /d/file and of its parent /d.
	f.Emit(Event{Op: OpModified, Path: "/d/file"})

	if ev := recvOp(t, dir); ev.Op != OpModified || ev.Path != "/d/file" {
		t.Fatalf("dir got %+v; want modified /d/file", ev)
	}
	if ev := recvOp(t, file); ev.Op != OpModified || ev.Path != "/d/file" {
		t.Fatalf("file got %+v; want modified /d/file", ev)
	}

	// An unrelated path delivers to nobody we hold.
	f.Emit(Event{Op: OpCreated, Path: "/other/x"})
	select {
	case ev := <-dir.Events():
		t.Fatalf("dir got unexpected %+v", ev)
	default:
	}
}

// TestFeedSubClose ensures a closed sub stops receiving and unregisters, and
// that closing is idempotent.
func TestFeedSubClose(t *testing.T) {
	f := NewFeed()
	defer f.Close()

	sub, _ := f.Watch("/d")
	sub.Close()
	sub.Close() // idempotent, must not panic

	// Emitting after close must not panic and must not deliver.
	f.Emit(Event{Op: OpCreated, Path: "/d/new"})

	if _, ok := <-sub.Events(); ok {
		t.Fatal("events channel should be closed after Close")
	}
	if n := len(f.paths); n != 0 {
		t.Fatalf("feed still tracks %d paths after sub close; want 0", n)
	}
}

// TestFeedClose closes the feed out from under a live sub.
func TestFeedClose(t *testing.T) {
	f := NewFeed()
	sub, _ := f.Watch("/d")

	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, ok := <-sub.Events(); ok {
		t.Fatal("sub events should be closed when the feed closes")
	}
	if _, err := f.Watch("/x"); err != ErrClosed {
		t.Fatalf("watch after close = %v; want ErrClosed", err)
	}
}

// TestLocalSourceConformance is a smoke check that the inotify Manager plugs
// into the same Source seam (construction only; no real watches needed).
func TestLocalSourceConformance(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Skipf("no fsnotify watcher in this env: %v", err)
	}
	defer m.Close()
	var src Source = LocalSource(m)
	if src == nil {
		t.Fatal("LocalSource returned nil")
	}
}
