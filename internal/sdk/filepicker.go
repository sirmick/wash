package sdk

import (
	"log"
	"strings"
	"sync"

	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/internal/fswatch"
)

// EnableFilePicker installs a handler chain that lets @wash/ui's
// <FilePicker> work without per-app plumbing. Call it once in
// OnReady. The picker FE addresses the host's own BE via
// sendAppMsg(instanceID, …) with kind "fs.list" / "fs.stat" /
// "fs.complete" / "fs.root" / "fs.watch" / "fs.unwatch"; this
// helper dispatches those into internal/fs + internal/fswatch and
// replies via c.SendAppMsg.
//
// Non-fs.* messages fall through to the app's existing OnAppMsg —
// EnableFilePicker is additive, not exclusive.
//
// Why this isn't a separate process: the picker only needs to call
// read-side fs ops, and the host's BE already runs as the user with
// the same access. A dedicated service would just add two router
// hops per directory navigation for zero benefit. The sandbox the
// router ships in Session is honored either way.
//
// Side benefit: any other FE in the host (like wash-edit's sidebar
// tree) can also speak fs.watch / fs.unwatch — the helper isn't
// picker-specific, it's "fs.* dispatch for this app".
func EnableFilePicker(c *Conn) {
	fsa := wfs.New(c.session.Root)
	ws := newWatchState()
	prev := c.def.OnAppMsg
	c.def.OnAppMsg = func(conn *Conn, win uint32, data any) {
		if handled := dispatchPicker(conn, fsa, ws, data); handled {
			return
		}
		if prev != nil {
			prev(conn, win, data)
		}
	}
}

// watchState owns the fswatch.Manager + per-path Sub map for one
// app's fs.watch surface. Manager is lazy: created on the first
// fs.watch so an app that never watches anything doesn't allocate
// an fsnotify watcher.
//
// Subs are refcounted because one app can have multiple subscribers
// to the same path (e.g. the editor's sidebar and the embedded
// FilePicker both watch the project root). Without refcounting the
// first unwatch would tear the sub down and the other subscriber
// would silently stop receiving events. Each fs.watch bumps refs;
// fs.unwatch decrements; the underlying sub only closes at zero.
type subRef struct {
	sub  *fswatch.Sub
	refs int
}

type watchState struct {
	mu   sync.Mutex
	mgr  *fswatch.Manager
	subs map[string]*subRef
}

func newWatchState() *watchState {
	return &watchState{subs: map[string]*subRef{}}
}

// ensureMgr lazily constructs the fswatch.Manager. Returns nil on
// failure (fswatch.New can fail on resource limits — inotify
// instance caps, etc); callers should reply "unavailable".
func (w *watchState) ensureMgr() *fswatch.Manager {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.mgr != nil {
		return w.mgr
	}
	mgr, err := fswatch.New()
	if err != nil {
		log.Printf("sdk.EnableFilePicker: fswatch unavailable: %v", err)
		return nil
	}
	w.mgr = mgr
	return mgr
}

// dispatchPicker returns true when the message had a recognized
// fs.* kind (handled or errored with a reply); false when the host
// should keep dispatching.
func dispatchPicker(c *Conn, fsa *wfs.FS, ws *watchState, data any) bool {
	m, ok := data.(map[any]any)
	if !ok {
		return false
	}
	kind, _ := m["kind"].(string)
	if !strings.HasPrefix(kind, "fs.") {
		return false
	}
	id, _ := m["id"].(string)
	switch kind {
	case "fs.root":
		pickerReply(c, "fs.root_ok", id, map[string]any{
			"root": fsa.Root(),
		})
	case "fs.list":
		path, _ := m["path"].(string)
		entries, abs, truncated, err := fsa.List(path, 0)
		if err != nil {
			pickerReply(c, "fs.list_err", id, map[string]any{
				"path": path,
				"code": wfs.ErrCode(err),
				"msg":  err.Error(),
			})
			return true
		}
		pickerReply(c, "fs.list_ok", id, map[string]any{
			"path":      abs,
			"entries":   entries,
			"truncated": truncated,
		})
	case "fs.stat":
		path, _ := m["path"].(string)
		entry, abs, err := fsa.Stat(path)
		if err != nil {
			pickerReply(c, "fs.stat_err", id, map[string]any{
				"path": path,
				"code": wfs.ErrCode(err),
				"msg":  err.Error(),
			})
			return true
		}
		pickerReply(c, "fs.stat_ok", id, map[string]any{
			"path":  abs,
			"entry": entry,
		})
	case "fs.complete":
		partial, _ := m["partial"].(string)
		matches := fsa.Complete(partial, 0)
		pickerReply(c, "fs.complete_ok", id, map[string]any{
			"partial": partial,
			"matches": matches,
		})
	case "fs.watch":
		path, _ := m["path"].(string)
		doWatch(c, fsa, ws, id, path)
	case "fs.unwatch":
		path, _ := m["path"].(string)
		doUnwatch(c, fsa, ws, id, path)
	default:
		// Unknown fs.* — leave for the host so a future extension
		// could be handled there. Returning false keeps the chain
		// going.
		return false
	}
	return true
}

// doWatch subscribes the connection to fsnotify events under path
// and (on first subscribe) starts a goroutine that re-emits them
// as fs.watch_event app_msgs. Calls for an already-watched path
// just bump the refcount — same path, same sub, same goroutine;
// the FE gets a fresh fs.watch_ok either way.
func doWatch(c *Conn, fsa *wfs.FS, ws *watchState, id, path string) {
	if path == "" {
		pickerReply(c, "fs.watch_err", id, map[string]any{
			"path": path, "code": "bad_request", "msg": "missing path",
		})
		return
	}
	abs, err := fsa.Confine(path)
	if err != nil {
		pickerReply(c, "fs.watch_err", id, map[string]any{
			"path": path, "code": wfs.ErrCode(err), "msg": err.Error(),
		})
		return
	}
	mgr := ws.ensureMgr()
	if mgr == nil {
		pickerReply(c, "fs.watch_err", id, map[string]any{
			"path": abs, "code": "unavailable", "msg": "fswatch not available",
		})
		return
	}
	ws.mu.Lock()
	if ref, ok := ws.subs[abs]; ok {
		ref.refs++
		ws.mu.Unlock()
		pickerReply(c, "fs.watch_ok", id, map[string]any{"path": abs})
		return
	}
	sub, err := mgr.Watch(abs)
	if err != nil {
		ws.mu.Unlock()
		pickerReply(c, "fs.watch_err", id, map[string]any{
			"path": abs, "code": "io", "msg": err.Error(),
		})
		return
	}
	ws.subs[abs] = &subRef{sub: sub, refs: 1}
	ws.mu.Unlock()

	// One goroutine per fswatch.Sub. Survives every subscriber's
	// lifetime — only exits when the Sub's events channel closes,
	// which doUnwatch does only at refcount=0.
	go func(s *fswatch.Sub) {
		for ev := range s.Events() {
			err := c.SendAppMsg(map[string]any{
				"kind": "fs.watch_event",
				"op":   ev.Op.String(),
				"path": ev.Path,
			})
			if err != nil {
				log.Printf("sdk.EnableFilePicker: fs.watch_event: %v", err)
				return
			}
		}
	}(sub)
	pickerReply(c, "fs.watch_ok", id, map[string]any{"path": abs})
}

// doUnwatch decrements the refcount for path. The underlying Sub
// is only closed when the count reaches zero — any other
// subscriber for the same path keeps receiving events. Unwatching
// a path that isn't being watched is a no-op success (the FE asked
// for it to stop; it isn't; mission accomplished).
func doUnwatch(c *Conn, fsa *wfs.FS, ws *watchState, id, path string) {
	if path == "" {
		pickerReply(c, "fs.unwatch_err", id, map[string]any{
			"path": path, "code": "bad_request", "msg": "missing path",
		})
		return
	}
	abs, err := fsa.Confine(path)
	if err != nil {
		// On confine error, still echo a benign unwatch_ok — the
		// FE can't reach that dir anyway, so it isn't watching it.
		pickerReply(c, "fs.unwatch_ok", id, map[string]any{"path": path})
		return
	}
	var closeSub *fswatch.Sub
	ws.mu.Lock()
	if ref, ok := ws.subs[abs]; ok {
		ref.refs--
		if ref.refs <= 0 {
			closeSub = ref.sub
			delete(ws.subs, abs)
		}
	}
	ws.mu.Unlock()
	if closeSub != nil {
		closeSub.Close()
	}
	pickerReply(c, "fs.unwatch_ok", id, map[string]any{"path": abs})
}

func pickerReply(c *Conn, kind, id string, extra map[string]any) {
	out := map[string]any{"kind": kind}
	if id != "" {
		out["id"] = id
	}
	for k, v := range extra {
		out[k] = v
	}
	_ = c.SendAppMsg(out)
}
