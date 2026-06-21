package sdk

import (
	"path"
	"sync"

	"github.com/sirmick/wash/internal/wire"
)

// FsWatchAppID is the shared filesystem-watch service every app watches through.
// Duplicated rather than imported — the contract is the app-id (same convention
// as wash-connect's reference to com.wash.remote).
const FsWatchAppID = "com.wash.fswatch"

// WatchEvent is a filesystem change delivered to a WatchClient callback.
type WatchEvent struct {
	Op   string
	Path string
}

// WatchCallback receives events for a watched path (and its direct children).
type WatchCallback func(WatchEvent)

// WatchClient is the consumer side of com.wash.fswatch. It relays watch/unwatch
// to the service and dispatches the service's fs_event pushes to per-path
// callbacks. One per app process — construct it once on the app's Bus in
// OnReady; it installs the single fs_event handler so multiple watchers in one
// app don't collide on that kind.
//
// Refcounting matches the old in-process RefMap: repeated Watch of a path sends
// one watch to the service (and the first caller's callback is the consumer);
// the underlying watch is released when the last Unwatch lands. The service
// already fans a directory's events to that directory, so a callback registered
// for a dir receives changes to its children.
type WatchClient struct {
	conn *Conn

	mu   sync.Mutex
	refs map[string]int
	cbs  map[string]WatchCallback
}

// NewWatchClient returns a client bound to c. It chains the conn's
// OnAppMsgFrom so it can intercept the watch service's fs_event pushes — which
// composes whether or not the app also runs a Bus (a Bus rewrites OnAppMsgFrom,
// and any non-fs_event message falls through to the previous handler), and
// works for the Bus-less filepicker too. Create it once in OnReady.
func NewWatchClient(c *Conn) *WatchClient {
	wc := &WatchClient{
		conn: c,
		refs: map[string]int{},
		cbs:  map[string]WatchCallback{},
	}
	prev := c.def.OnAppMsgFrom
	c.def.OnAppMsgFrom = func(conn *Conn, win uint32, data any, from wire.Sender) {
		if from.AppID == FsWatchAppID && wc.tryHandle(data) {
			return
		}
		if prev != nil {
			prev(conn, win, data, from)
		}
	}
	return wc
}

// tryHandle dispatches an fs_event payload from the service; returns false for
// anything else so the message continues down the OnAppMsgFrom chain.
func (wc *WatchClient) tryHandle(data any) bool {
	m, ok := data.(map[string]any)
	if !ok {
		return false
	}
	if kind, _ := m["kind"].(string); kind != "fs_event" {
		return false
	}
	op, _ := m["op"].(string)
	p, _ := m["path"].(string)
	wc.dispatch(WatchEvent{Op: op, Path: p})
	return true
}

// Watch subscribes to changes under path, invoking cb for each. Repeated Watch
// of the same path just refcounts (cb from the first call is retained).
func (wc *WatchClient) Watch(path string, cb WatchCallback) error {
	if path == "" {
		return Errf(ErrBadRequest, "missing path")
	}
	wc.mu.Lock()
	first := wc.refs[path] == 0
	wc.refs[path]++
	if first {
		wc.cbs[path] = cb
	}
	wc.mu.Unlock()
	if first {
		if err := wc.conn.SendAppMsgTo(wire.Recipient{AppID: FsWatchAppID}, map[string]any{
			"kind": "watch",
			"path": path,
		}); err != nil {
			wc.mu.Lock()
			wc.refs[path]--
			delete(wc.cbs, path)
			wc.mu.Unlock()
			return err
		}
	}
	return nil
}

// Unwatch drops one reference to path, releasing the service-side watch when the
// last reference goes. Unwatching an untracked path is a no-op.
func (wc *WatchClient) Unwatch(path string) {
	wc.mu.Lock()
	if wc.refs[path] > 0 {
		wc.refs[path]--
	}
	last := wc.refs[path] == 0
	if last {
		delete(wc.refs, path)
		delete(wc.cbs, path)
	}
	wc.mu.Unlock()
	if last {
		_ = wc.conn.SendAppMsgTo(wire.Recipient{AppID: FsWatchAppID}, map[string]any{
			"kind": "unwatch",
			"path": path,
		})
	}
}

// dispatch delivers an event to the callback watching its directory (the common
// case — the event path is a child of a watched dir) and to an exact-path
// watcher, mirroring the service's own fan-out.
func (wc *WatchClient) dispatch(ev WatchEvent) {
	wc.mu.Lock()
	cbs := make([]WatchCallback, 0, 2)
	if cb := wc.cbs[ev.Path]; cb != nil {
		cbs = append(cbs, cb)
	}
	if parent := path.Dir(ev.Path); parent != ev.Path {
		if cb := wc.cbs[parent]; cb != nil {
			cbs = append(cbs, cb)
		}
	}
	wc.mu.Unlock()
	for _, cb := range cbs {
		cb(ev)
	}
}
