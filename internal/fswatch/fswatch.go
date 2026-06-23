// Package fswatch is a small wrapper over github.com/fsnotify/fsnotify
// that provides per-subscription cleanup and refcount-per-path so a
// single inotify (or kqueue / FSEvents) watch is shared by multiple
// in-process subscribers.
//
// Design notes
//
//   - One *Manager per app process. The Manager owns one underlying
//     fsnotify.Watcher and one fanout goroutine.
//   - Each Watch call returns a *Sub. Multiple Subs to the same path
//     share the underlying OS watch; the manager reference-counts and
//     only removes the watch from fsnotify when the last Sub closes.
//   - Sub.Close() is idempotent and safe to call from any goroutine.
//   - Manager.Close() tears down all subs and the fanout goroutine.
//
// What we DON'T do
//
//   - Recursive watch. Each path is exactly one directory (or file);
//     the caller subscribes to children itself as it descends. fm
//     does this naturally via "subscribe on expand". A future
//     recursive helper could be added in the same package without
//     touching consumers.
//   - Coalescing or debouncing. Consumers receive the raw event
//     stream; if a flurry of writes is noisy, the consumer batches.
//
// # Why a library, not a router service
//
// Each consumer (fm now, the session-app dialog later, a text editor
// after that) owns its own watches with its own lifecycle. They do
// not share state and do not need to coordinate across processes.
// fsnotify itself already abstracts the platform inotify/kqueue/
// FSEvents differences. A router service would just add wire layers
// for no functional gain.
package fswatch

import (
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Op identifies the type of filesystem change the consumer cares
// about. We intentionally collapse fsnotify's flag set to the three
// outcomes a file manager UI reacts to.
type Op int

const (
	// OpCreated covers fsnotify CREATE.
	OpCreated Op = iota
	// OpModified covers fsnotify WRITE and CHMOD (any mutation of
	// the existing entry that callers should re-stat).
	OpModified
	// OpDeleted covers fsnotify REMOVE and RENAME (rename is the
	// source side of a rename; the destination side surfaces as a
	// CREATE in its own watched directory if that's watched).
	OpDeleted
)

func (o Op) String() string {
	switch o {
	case OpCreated:
		return "created"
	case OpModified:
		return "modified"
	case OpDeleted:
		return "deleted"
	}
	return fmt.Sprintf("op(%d)", int(o))
}

// Event is the per-change payload delivered to subscribers.
type Event struct {
	Op   Op
	Path string
}

// Manager owns a single fsnotify watcher and dispatches events to
// any number of per-path subscribers.
type Manager struct {
	w *fsnotify.Watcher

	mu     sync.Mutex
	paths  map[string]*pathState // refcounted watches keyed by path
	closed bool

	stop chan struct{} // closed when Close() runs
	done chan struct{} // closed when the fanout goroutine exits
}

// pathState is the per-path bookkeeping: which subs want events for
// this path, and the count of how many are still open.
type pathState struct {
	subs map[*Sub]struct{}
}

// Sub is one subscription handle. Closing the Sub releases the
// underlying watch's refcount; the manager removes the OS watch
// when the count hits zero.
type Sub struct {
	mgr    *Manager
	path   string
	events chan Event
	closed chan struct{}
	done   bool // guarded by mgr.mu; true once torn down
}

// New returns a Manager with one fsnotify watcher and a fanout
// goroutine. Call Close() to tear down both. New returns an error
// only if the OS refuses to create a watcher at all (resource
// exhaustion is the realistic case).
func New() (*Manager, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify: %w", err)
	}
	m := &Manager{
		w:     w,
		paths: make(map[string]*pathState),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go m.run()
	return m, nil
}

// run is the fanout goroutine. It exits cleanly on Close.
func (m *Manager) run() {
	defer close(m.done)
	for {
		select {
		case <-m.stop:
			return
		case ev, ok := <-m.w.Events:
			if !ok {
				return
			}
			m.dispatch(ev)
		case err, ok := <-m.w.Errors:
			if !ok {
				return
			}
			// Errors from fsnotify are mostly "watch removed" sort
			// of things — log-worthy at the consumer level, but
			// not propagated as events. Drop silently here; the
			// affected Sub will simply stop receiving events.
			_ = err
		}
	}
}

// dispatch converts an fsnotify event to one or more Events and
// delivers them to every Sub watching the source path's parent dir
// (which is what was actually fsnotify-added).
func (m *Manager) dispatch(ev fsnotify.Event) {
	// fsnotify's event.Name is the changed file's path. The watched
	// entry was a directory, so subscribers keyed by that directory
	// want this event — we find the dir by stripping the basename.
	// But: when the watched dir itself is removed/renamed, the
	// event.Name IS the watched dir. In that case the dir-level
	// subscribers should also be told. So we deliver to subscribers
	// of both event.Name AND of dirname(event.Name).
	ops := opsFromFsnotify(ev.Op)
	if len(ops) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, op := range ops {
		evt := Event{Op: op, Path: ev.Name}
		// 1) subs watching the exact path (the watched dir itself
		//    was removed/renamed).
		if ps := m.paths[ev.Name]; ps != nil {
			for s := range ps.subs {
				deliver(s, evt)
			}
		}
		// 2) subs watching the parent directory (the usual case).
		parent := parentDir(ev.Name)
		if parent != "" && parent != ev.Name {
			if ps := m.paths[parent]; ps != nil {
				for s := range ps.subs {
					deliver(s, evt)
				}
			}
		}
	}
}

// opsFromFsnotify expands fsnotify's bit-flag op set to one or more
// of our coarser ops. We deliberately collapse RENAME and REMOVE
// into OpDeleted (see Op docs) and CHMOD into OpModified.
func opsFromFsnotify(op fsnotify.Op) []Op {
	var out []Op
	if op&fsnotify.Create != 0 {
		out = append(out, OpCreated)
	}
	if op&fsnotify.Write != 0 || op&fsnotify.Chmod != 0 {
		out = append(out, OpModified)
	}
	if op&fsnotify.Remove != 0 || op&fsnotify.Rename != 0 {
		out = append(out, OpDeleted)
	}
	return out
}

// deliver sends evt to s without blocking. If the consumer's
// channel is full, the event is dropped — consumers that need every
// event are expected to drain promptly. The Sub's buffer size (set
// at Watch time) is the knob.
func deliver(s *Sub, evt Event) {
	select {
	case s.events <- evt:
	case <-s.closed:
	default:
		// channel full; drop
	}
}

// parentDir returns the directory containing path, or "" if path is
// "/" or contains no separator. Mirrors filepath.Dir but explicit
// about the empty-on-root case so dispatch can skip it cheaply.
func parentDir(path string) string {
	if path == "" || path == "/" {
		return ""
	}
	i := len(path) - 1
	for i >= 0 && path[i] != '/' {
		i--
	}
	if i < 0 {
		return ""
	}
	if i == 0 {
		return "/"
	}
	return path[:i]
}

// ErrClosed is returned by Watch after the Manager has been closed.
var ErrClosed = errors.New("fswatch: manager closed")

// Watch starts watching path and returns a Sub whose Events channel
// receives changes. The returned channel has a small buffer; if the
// consumer falls behind, events are dropped (deliver is non-blocking)
// rather than backing pressure into the fsnotify pipe — losing an
// event is recoverable (re-stat the dir), stalling the fanout would
// affect every other Sub.
func (m *Manager) Watch(path string) (*Sub, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	ps, existing := m.paths[path]
	if !existing {
		if err := m.w.Add(path); err != nil {
			// Logged here as well as returned: callers historically
			// surface this only as an FE error, and the usual cause —
			// the inotify instance/watch limit, often from leaked
			// processes — is invisible without a server-side line.
			log.Printf("fswatch: watch %q failed: %v (hitting fs.inotify.max_user_instances/watches? check for leaked watcher processes)", path, err)
			return nil, fmt.Errorf("watch %q: %w", path, err)
		}
		ps = &pathState{subs: make(map[*Sub]struct{})}
		m.paths[path] = ps
	}
	s := &Sub{
		mgr:    m,
		path:   path,
		events: make(chan Event, 32),
		closed: make(chan struct{}),
	}
	ps.subs[s] = struct{}{}
	return s, nil
}

// Close tears down the manager: closes all subs, removes all OS
// watches, closes the fsnotify watcher, waits for the fanout
// goroutine. Safe to call once; subsequent calls return nil.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	// Close all subs while holding the lock so no late Watch races in.
	// teardownLocked is the single teardown primitive (also used by
	// Sub.Close) — one mutex owns the whole sub lifecycle, so there is no
	// second lock to invert against. The OS watches are torn down in bulk
	// by m.w.Close() below; the per-path Remove teardownLocked issues is
	// redundant-but-harmless here (error ignored).
	for _, ps := range m.paths {
		for s := range ps.subs {
			s.teardownLocked()
		}
	}
	m.paths = nil
	m.mu.Unlock()

	close(m.stop)
	err := m.w.Close()
	<-m.done
	return err
}

// Events is the per-sub event channel. Closed when the Sub is
// closed (so a for-range loop exits naturally).
func (s *Sub) Events() <-chan Event { return s.events }

// Path is the path this Sub was created for. Useful when one
// dispatcher fans many subs and needs to label which one a
// channel-receive came from.
func (s *Sub) Path() string { return s.path }

// Close releases this subscription. The Sub's Events channel is
// closed. If this was the last Sub for the path, the underlying
// fsnotify watch is removed from the Manager. Idempotent; safe from
// any goroutine.
func (s *Sub) Close() {
	s.mgr.mu.Lock()
	defer s.mgr.mu.Unlock()
	s.teardownLocked()
}

// teardownLocked closes the sub's channels exactly once and drops it from the
// path index, removing the OS watch when the path's last sub goes. Everything
// runs under mgr.mu — the single owner of the sub lifecycle — so Close and
// Sub.Close can't deadlock against each other (no closeOnce↔mu inversion).
// Closing events under the lock Manager.dispatch holds is also what keeps the
// non-blocking send safe: a send and a close can never overlap. Caller holds
// mgr.mu.
func (s *Sub) teardownLocked() {
	if s.done {
		return
	}
	s.done = true
	close(s.closed)
	close(s.events)
	m := s.mgr
	ps := m.paths[s.path]
	if ps == nil {
		return
	}
	delete(ps.subs, s)
	if len(ps.subs) == 0 {
		_ = m.w.Remove(s.path)
		delete(m.paths, s.path)
	}
}

// Stats is internal-test-only insight into the Manager's state. It
// reports the number of currently-watched paths and the total live
// subscription count. Used by leak tests in fswatch_test.go.
func (m *Manager) Stats() (paths, subs int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	paths = len(m.paths)
	for _, ps := range m.paths {
		subs += len(ps.subs)
	}
	return
}
