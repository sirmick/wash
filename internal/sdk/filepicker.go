package sdk

import (
	"log"
	"strings"

	wfs "github.com/sirmick/wash/internal/fs"
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
	wc := NewWatchClient(c) // watches via the shared com.wash.fswatch service
	prev := c.def.OnAppMsg
	c.def.OnAppMsg = func(conn *Conn, win uint32, data any) {
		if handled := dispatchPicker(conn, fsa, wc, data); handled {
			return
		}
		if prev != nil {
			prev(conn, win, data)
		}
	}
}

// dispatchPicker returns true when the message had a recognized
// fs.* kind (handled or errored with a reply); false when the host
// should keep dispatching.
func dispatchPicker(c *Conn, fsa *wfs.FS, wc *WatchClient, data any) bool {
	m, ok := data.(map[string]any)
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
		// Unconfined picker boots with "/", which would dump the
		// user at the filesystem root. Resolve to $HOME instead so
		// the picker opens somewhere useful. Sandboxed apps already
		// have fsa.Root() set and the Confine path handles that.
		if path == "/" && fsa.Root() == "" {
			path = wfs.DefaultStart()
		}
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
		doWatch(c, fsa, wc, id, path)
	case "fs.unwatch":
		path, _ := m["path"].(string)
		doUnwatch(c, fsa, wc, id, path)
	default:
		// Unknown fs.* — leave for the host so a future extension
		// could be handled there. Returning false keeps the chain
		// going.
		return false
	}
	return true
}

// doWatch subscribes the connection to changes under path via the shared watch
// service, re-emitting each as an fs.watch_event app_msg to the FE. Repeated
// watches of a path refcount inside the WatchClient.
func doWatch(c *Conn, fsa *wfs.FS, wc *WatchClient, id, path string) {
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
	if err := wc.Watch(abs, func(ev WatchEvent) {
		if err := c.SendAppMsg(map[string]any{
			"kind": "fs.watch_event",
			"op":   ev.Op,
			"path": ev.Path,
		}); err != nil {
			log.Printf("sdk.EnableFilePicker: fs.watch_event: %v", err)
		}
	}); err != nil {
		pickerReply(c, "fs.watch_err", id, map[string]any{
			"path": abs, "code": "io", "msg": err.Error(),
		})
		return
	}
	pickerReply(c, "fs.watch_ok", id, map[string]any{"path": abs})
}

// doUnwatch releases one reference to path. Unwatching an untracked path is a
// no-op success.
func doUnwatch(c *Conn, fsa *wfs.FS, wc *WatchClient, id, path string) {
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
	wc.Unwatch(abs)
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
