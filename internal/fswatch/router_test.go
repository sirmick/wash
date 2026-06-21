package fswatch

import (
	"sync"
	"testing"
)

// recordSource is a Source that records the paths it was asked to watch and
// returns real (Feed-backed) Subscriptions, so routing can be asserted.
type recordSource struct {
	feed *Feed

	mu      sync.Mutex
	watched []string
	closed  bool
}

func newRecordSource() *recordSource { return &recordSource{feed: NewFeed()} }

func (r *recordSource) Watch(p string) (Subscription, error) {
	r.mu.Lock()
	r.watched = append(r.watched, p)
	r.mu.Unlock()
	return r.feed.Watch(p)
}

func (r *recordSource) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return r.feed.Close()
}

func (r *recordSource) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.watched)
}

func (r *recordSource) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.watched) == 0 {
		return ""
	}
	return r.watched[len(r.watched)-1]
}

func (r *recordSource) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func TestRouterRoutesByPrefix(t *testing.T) {
	def := newRecordSource()
	a := newRecordSource()
	rt := NewRouter(def)
	rt.Mount("/mnt/a", a)

	if _, err := rt.Watch("/mnt/a/x"); err != nil {
		t.Fatalf("watch under mount: %v", err)
	}
	if a.last() != "/mnt/a/x" || def.count() != 0 {
		t.Fatalf("mounted path not routed to mount (a=%q def=%d)", a.last(), def.count())
	}

	if _, err := rt.Watch("/other"); err != nil {
		t.Fatalf("watch outside mount: %v", err)
	}
	if def.last() != "/other" || a.count() != 1 {
		t.Fatalf("outside path not routed to default (def=%q a=%d)", def.last(), a.count())
	}
}

func TestRouterLongestPrefixWins(t *testing.T) {
	a := newRecordSource()
	b := newRecordSource()
	rt := NewRouter(newRecordSource())
	rt.Mount("/mnt/a", a)
	rt.Mount("/mnt/a/b", b) // nested

	if _, err := rt.Watch("/mnt/a/b/x"); err != nil {
		t.Fatal(err)
	}
	if b.count() != 1 || a.count() != 0 {
		t.Fatalf("nested path should route to innermost mount (a=%d b=%d)", a.count(), b.count())
	}

	if _, err := rt.Watch("/mnt/a/z"); err != nil {
		t.Fatal(err)
	}
	if a.last() != "/mnt/a/z" || b.count() != 1 {
		t.Fatalf("path under outer-only should route to outer (a=%q b=%d)", a.last(), b.count())
	}
}

func TestRouterUnmountFallsBack(t *testing.T) {
	def := newRecordSource()
	a := newRecordSource()
	rt := NewRouter(def)
	rt.Mount("/mnt/a", a)

	if !rt.Unmount("/mnt/a") {
		t.Fatal("Unmount reported no mount")
	}
	if !a.isClosed() {
		t.Fatal("Unmount did not close the mount source")
	}
	if rt.Unmount("/mnt/a") {
		t.Fatal("second Unmount should report false")
	}

	if _, err := rt.Watch("/mnt/a/x"); err != nil {
		t.Fatal(err)
	}
	if def.last() != "/mnt/a/x" {
		t.Fatalf("after unmount, path should fall back to default; def=%q", def.last())
	}
}

func TestRouterRemountReplaces(t *testing.T) {
	a := newRecordSource()
	a2 := newRecordSource()
	rt := NewRouter(nil)
	rt.Mount("/mnt/a", a)
	rt.Mount("/mnt/a", a2) // replace

	if !a.isClosed() {
		t.Fatal("re-mount should close the replaced source")
	}
	if _, err := rt.Watch("/mnt/a/x"); err != nil {
		t.Fatal(err)
	}
	if a2.count() != 1 || a.count() != 0 {
		t.Fatalf("watch should hit the replacement (a=%d a2=%d)", a.count(), a2.count())
	}
}

func TestRouterCloseClosesMountsNotDefault(t *testing.T) {
	def := newRecordSource()
	a := newRecordSource()
	b := newRecordSource()
	rt := NewRouter(def)
	rt.Mount("/mnt/a", a)
	rt.Mount("/mnt/b", b)

	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if !a.isClosed() || !b.isClosed() {
		t.Fatal("Close should close all mount sources")
	}
	if def.isClosed() {
		t.Fatal("Close must not close the default (borrowed) source")
	}
}

func TestRouterNoRouteWithoutDefault(t *testing.T) {
	rt := NewRouter(nil)
	if _, err := rt.Watch("/x"); err != ErrNoRoute {
		t.Fatalf("watch with no default and no mount = %v; want ErrNoRoute", err)
	}
}
