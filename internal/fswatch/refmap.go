package fswatch

import "sync"

// RefMap is a refcounted directory of fswatch subscriptions. Multiple
// callers can ask the same path to be watched; only the last Unwatch
// tears down the underlying Sub.
//
// Use case: one app process may host several UI surfaces (a file
// manager pane and an embedded file picker, say) that independently
// want to know about changes under the same directory. Without
// refcounting the first Unwatch would silently stop the other
// subscriber's events.
//
// A RefMap wraps a Manager. The Manager's lifetime is the caller's;
// Close on the RefMap closes every still-open Sub but leaves the
// Manager alone.
type RefMap struct {
	mu   sync.Mutex
	mgr  *Manager
	subs map[string]*refSub
}

type refSub struct {
	sub  *Sub
	refs int
}

// NewRefMap returns a RefMap backed by mgr.
func NewRefMap(mgr *Manager) *RefMap {
	return &RefMap{mgr: mgr, subs: map[string]*refSub{}}
}

// Watch returns the Sub for path, incrementing its refcount. first
// is true when this is the first subscriber — the caller should
// start a forwarding goroutine on the returned Sub. Subsequent
// Watches for the same path return the same Sub with first=false,
// so the existing goroutine keeps fanning events to its consumers.
//
// path must already be cleaned/absolute; RefMap doesn't normalize.
func (r *RefMap) Watch(path string) (sub *Sub, first bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ref, ok := r.subs[path]; ok {
		ref.refs++
		return ref.sub, false, nil
	}
	s, err := r.mgr.Watch(path)
	if err != nil {
		return nil, false, err
	}
	r.subs[path] = &refSub{sub: s, refs: 1}
	return s, true, nil
}

// Unwatch decrements path's refcount. When the count reaches zero
// the underlying Sub is closed (its Events channel drains and
// closes, terminating the forwarding goroutine).
//
// Unwatching a path that isn't tracked is a no-op. This makes the
// FE protocol idempotent: a duplicate unwatch isn't an error.
func (r *RefMap) Unwatch(path string) {
	r.mu.Lock()
	ref, ok := r.subs[path]
	if !ok {
		r.mu.Unlock()
		return
	}
	ref.refs--
	if ref.refs > 0 {
		r.mu.Unlock()
		return
	}
	delete(r.subs, path)
	sub := ref.sub
	r.mu.Unlock()
	sub.Close()
}

// Close releases every still-open Sub. Safe to call multiple times.
// Does not close the underlying Manager — callers built it.
func (r *RefMap) Close() {
	r.mu.Lock()
	subs := r.subs
	r.subs = map[string]*refSub{}
	r.mu.Unlock()
	for _, ref := range subs {
		ref.sub.Close()
	}
}
