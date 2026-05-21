package sdk

import (
	"strings"

	wfs "github.com/sirmick/wash/internal/fs"
)

// EnableFilePicker installs a handler chain that lets @wash/ui's
// <FilePicker> work without per-app plumbing. Call it once in
// OnReady. The picker FE addresses the host's own BE via
// sendAppMsg(instanceID, …) with kind "fs.list" / "fs.stat" /
// "fs.complete" / "fs.root"; this helper dispatches those into
// internal/fs (constructed with the router-supplied Session.Root
// for sandboxing) and replies via c.SendAppMsg.
//
// Non-fs.* messages fall through to the app's existing OnAppMsg —
// EnableFilePicker is additive, not exclusive.
//
// Why this isn't a separate process: the picker only needs to call
// read-side fs ops, and the host's BE already runs as the user with
// the same access. A dedicated service would just add two router
// hops per directory navigation for zero benefit. The sandbox the
// router ships in Session is honored either way.
func EnableFilePicker(c *Conn) {
	fsa := wfs.New(c.session.Root)
	prev := c.def.OnAppMsg
	c.def.OnAppMsg = func(conn *Conn, win uint32, data any) {
		if handled := dispatchPicker(conn, fsa, data); handled {
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
func dispatchPicker(c *Conn, fsa *wfs.FS, data any) bool {
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
	default:
		// Unknown fs.* — leave for the host so a future extension
		// could be handled there. Returning false keeps the chain
		// going.
		return false
	}
	return true
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
