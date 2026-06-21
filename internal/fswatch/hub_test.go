package fswatch

import (
	"testing"
	"time"
)

type emitRec struct {
	id string
	ev Event
}

func drainN(t *testing.T, ch <-chan emitRec, n int) []emitRec {
	t.Helper()
	var out []emitRec
	deadline := time.After(2 * time.Second)
	for len(out) < n {
		select {
		case r := <-ch:
			out = append(out, r)
		case <-deadline:
			t.Fatalf("got %d emits, want %d", len(out), n)
		}
	}
	return out
}

func expectNoEmit(t *testing.T, ch <-chan emitRec) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("unexpected emit %+v", r)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestHubFanoutAndRefcount drives the shared-service engine: two subscribers on
// one path both receive a change (single underlying watch, fanned), an
// unsubscribed one stops, and removing the last tears the watch down.
func TestHubFanoutAndRefcount(t *testing.T) {
	feed := NewFeed() // Feed is a Source; stands in for the Router
	defer feed.Close()

	rec := make(chan emitRec, 16)
	hub := NewHub(feed, func(id string, ev Event) { rec <- emitRec{id, ev} })
	defer hub.Close()

	if err := hub.Subscribe("a", "/d"); err != nil {
		t.Fatalf("subscribe a: %v", err)
	}
	if err := hub.Subscribe("b", "/d"); err != nil {
		t.Fatalf("subscribe b: %v", err)
	}

	// One change fans to both subscribers from a single underlying watch.
	feed.Emit(Event{Op: OpModified, Path: "/d/x"})
	got := drainN(t, rec, 2)
	ids := map[string]bool{}
	for _, r := range got {
		if r.ev.Path != "/d/x" {
			t.Fatalf("event path = %q; want /d/x", r.ev.Path)
		}
		ids[r.id] = true
	}
	if !ids["a"] || !ids["b"] {
		t.Fatalf("want both a and b; got %v", ids)
	}

	// After a unsubscribes, only b receives.
	hub.Unsubscribe("a", "/d")
	feed.Emit(Event{Op: OpModified, Path: "/d/y"})
	got = drainN(t, rec, 1)
	if got[0].id != "b" {
		t.Fatalf("after unsubscribe a, got %s; want b", got[0].id)
	}
	expectNoEmit(t, rec) // a must not still receive

	// Removing the last subscriber tears the watch down; nobody gets events.
	hub.RemoveSubscriber("b")
	if n := len(hub.paths); n != 0 {
		t.Fatalf("hub still tracks %d paths after last subscriber removed", n)
	}
	feed.Emit(Event{Op: OpModified, Path: "/d/z"})
	expectNoEmit(t, rec)
}
