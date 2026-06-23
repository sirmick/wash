package fswatch

import "sync"

// This file adds the seam that lets a filesystem-change feed come from
// somewhere other than the local kernel's inotify.
//
// The local Manager observes changes via fsnotify, which only sees writes that
// pass through THIS machine's kernel. A remote mount (wash-mount over SFTP)
// breaks that assumption: changes made on the far machine never reach the local
// kernel, so local inotify is blind to them. The fix is to observe changes
// where they happen — on the remote — and push them in over a side channel:
//
//   - data/metadata path: SFTP (read/write/stat/readdir), on the FUSE mount.
//   - change-notification path: a wash watch agent runs the local Manager on
//     the REMOTE, serializes its Sub events over the SSH control channel, and a
//     decoder on this side calls Feed.Emit for each one.
//
// Where no wash agent is available (a plain sshd box), a poll loop over SFTP
// diffs the currently-viewed directory and calls Feed.Emit with synthetic
// events. Either way the consumer sees a Subscription and cannot tell the
// difference — that is the point of the interface.

// Subscription is the consumer's view of one path subscription. Both the local
// inotify-backed *Sub and the pushed *feedSub satisfy it, so consumers (fm,
// filepicker, edit) can be written against whatever Source they are handed
// rather than against inotify directly.
type Subscription interface {
	Events() <-chan Event
	Path() string
	Close()
}

// Source produces path subscriptions, abstracting WHERE changes are observed:
// the local Manager (via LocalSource), or a Feed fed from the wire/poll loop.
// A remote mount supplies a Feed in place of a Manager; consumers don't change.
type Source interface {
	Watch(path string) (Subscription, error)
	Close() error
}

// Compile-time conformance: the existing local types and the new remote feed
// all satisfy the seam, so they are interchangeable to a consumer.
var (
	_ Subscription = (*Sub)(nil)
	_ Subscription = (*feedSub)(nil)
	_ Source       = localSource{}
	_ Source       = (*Feed)(nil)
)

// LocalSource adapts the inotify Manager to the Source interface. Its Watch
// returns the Manager's *Sub widened to Subscription; Close is the Manager's.
func LocalSource(m *Manager) Source { return localSource{m} }

type localSource struct{ *Manager }

func (l localSource) Watch(path string) (Subscription, error) {
	return l.Manager.Watch(path)
}

// Feed is a Source whose events are pushed in from outside the local kernel.
// The remote watch-channel decoder (or the SFTP poll fallback) calls Emit for
// every change; Feed fans it out with exactly the same path+parent delivery
// rule the local Manager uses, so a feedSub is indistinguishable from a Sub.
type Feed struct {
	mu     sync.Mutex
	paths  map[string]map[*feedSub]struct{}
	closed bool
}

// NewFeed returns an empty Feed. Wire its Emit to a remote event stream.
func NewFeed() *Feed {
	return &Feed{paths: make(map[string]map[*feedSub]struct{})}
}

// Watch registers interest in path and returns its Subscription.
func (f *Feed) Watch(path string) (Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, ErrClosed
	}
	s := &feedSub{
		feed:   f,
		path:   path,
		events: make(chan Event, 32),
		closed: make(chan struct{}),
	}
	set := f.paths[path]
	if set == nil {
		set = make(map[*feedSub]struct{})
		f.paths[path] = set
	}
	set[s] = struct{}{}
	return s, nil
}

// Emit delivers one change to subscribers of the changed path and of its parent
// directory — mirroring Manager.dispatch, so directory watchers (the common
// case) and exact-path watchers both fire. Non-blocking: a full consumer
// channel drops the event (a dropped event is recoverable by re-stat; stalling
// the feed would affect every subscriber). This is the single entry point a
// remote producer calls.
func (f *Feed) Emit(ev Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliverLocked(ev.Path, ev)
	if parent := parentDir(ev.Path); parent != "" && parent != ev.Path {
		f.deliverLocked(parent, ev)
	}
}

func (f *Feed) deliverLocked(key string, ev Event) {
	for s := range f.paths[key] {
		select {
		case s.events <- ev:
		case <-s.closed:
		default: // channel full; drop
		}
	}
}

// Close tears down every subscription. Safe to call once; later calls return
// nil. The wire/poll producer driving Emit is the caller's to stop.
func (f *Feed) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	for _, set := range f.paths {
		for s := range set {
			s.teardownLocked()
		}
	}
	f.paths = nil
	return nil
}

// feedSub is a Manager-free subscription fed by Feed.Emit. Its whole lifecycle
// — the events/closed channels AND its membership in feed.paths — is owned by
// feed.mu. There is no second lock, so teardown can't deadlock against the
// feed: every close path goes through teardownLocked under that one mutex.
type feedSub struct {
	feed   *Feed
	path   string
	events chan Event
	closed chan struct{}
	done   bool // guarded by feed.mu; true once torn down
}

func (s *feedSub) Events() <-chan Event { return s.events }
func (s *feedSub) Path() string         { return s.path }

// Close releases this subscription. Idempotent; safe from any goroutine.
func (s *feedSub) Close() {
	s.feed.mu.Lock()
	defer s.feed.mu.Unlock()
	s.teardownLocked()
}

// teardownLocked closes the sub's channels exactly once and drops it from the
// feed's path index, all under feed.mu. Idempotent via s.done. Closing events
// under the same lock Emit holds is what makes the non-blocking send safe — a
// send and a close can never overlap. Caller holds feed.mu.
func (s *feedSub) teardownLocked() {
	if s.done {
		return
	}
	s.done = true
	close(s.closed)
	close(s.events)
	f := s.feed
	if set := f.paths[s.path]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(f.paths, s.path)
		}
	}
}
