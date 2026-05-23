// wash-edit — a text editor app.
//
// Layout: sidebar (directory tree) | editor area (CodeMirror 6
// with tabs) | status bar at the bottom. instancing=multi so each
// editor window is an independent project context.
//
// The BE owns its own fs access via internal/fs — same in-process
// syscall path as wash-fm. Reads and writes don't go through
// wash-fs (which is only the picker's BE); the editor's BE serves
// its own list/read/write requests with zero router hops.
//
// Wire shape between this FE and its BE half:
//
//   FE → BE  : { kind: "list",  path }                     id-correlated
//                { kind: "read",  path }
//                { kind: "write", path, content }
//                fs.* messages handled by sdk.EnableFilePicker
//
//   BE → FE  : { kind: "list_ok", id?, path, entries, truncated }
//                { kind: "read_ok", id?, path, content, size, binary, truncated }
//                { kind: "write_ok", id?, path, bytes }
//                { kind: "<op>_err", id?, path?, code, msg }
package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"os"
	"strings"
	"sync"

	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/internal/pty"
	"github.com/sirmick/wash/internal/sdk"
)

//go:embed all:assets
var assetsFS embed.FS

const (
	version = "0.0.0"

	// Cap on read size. Bigger files are still listed; the editor
	// surfaces a "too large to open here" placeholder rather than
	// trying to hold them in memory. Generous enough for any source
	// file a human would actually edit.
	maxReadBytes = 4 * 1024 * 1024

	// Cap on write size. Mirrors maxReadBytes — a save that exceeds
	// the read cap shouldn't be allowed either.
	maxWriteBytes = 4 * 1024 * 1024

	// Cap on directory listing for the sidebar tree.
	maxListEntries = 5_000
)

// editFS is the BE's read/write accessor, sandboxed by the router-
// supplied Session.Root. Set once in onReady; package-level because
// onAppMsg dispatch happens from a separate read goroutine.
var (
	mu     sync.Mutex
	editFS *wfs.FS
	root   string
)

// Editor terminal tabs: keyed by raw-channel id. One Session per tab.
var (
	termMu       sync.Mutex
	termSessions = map[uint32]*pty.Session{}
)

func main() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("wash-edit: assets: %v", err)
	}
	sdk.Main(&sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.edit",
			Name:            "Editor",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-edit",
			Surface:         sdk.SurfaceWindow,
			Icon:            editIcon,
			Instancing:      sdk.InstancingMulti,
			Window:          &sdk.WindowHints{DefaultWidth: 900, DefaultHeight: 600},
			// Declared so the FE's "Open in fm" button can ask the
			// router to spawn fm via SpawnRequest. The router checks
			// this capability before honoring the request.
			Capabilities: []string{sdk.CapSpawn},
		},
		Assets:   sub,
		OnReady:  onReady,
		OnAppMsg: onAppMsg,
	})
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	mu.Lock()
	root = c.Session().Root
	editFS = wfs.New(root)
	mu.Unlock()
	// FilePicker bridge: one-liner; the picker FE addresses the
	// editor's own BE.
	sdk.EnableFilePicker(c)
	if root == "" {
		log.Printf("wash-edit ready instance=%s window=%d (unconfined)", instanceID, windowID)
	} else {
		log.Printf("wash-edit ready instance=%s window=%d root=%s", instanceID, windowID, root)
	}
}

func onAppMsg(c *sdk.Conn, _ uint32, data any) {
	m := sdk.AsMap(data)
	if m == nil {
		return
	}
	kind, _ := m["kind"].(string)
	id, _ := m["id"].(string)
	// Headless control commands (cmd.*) are owned by the FE; the BE
	// just passes them through unchanged. Callers that have a
	// direct line to the FE can skip this hop; the BE-side forward
	// is here so test drivers and other apps can target either side.
	if strings.HasPrefix(kind, "cmd.") {
		_ = c.SendAppMsg(m)
		return
	}
	switch kind {
	case "list":
		path, _ := m["path"].(string)
		doList(c, id, path)
	case "read":
		path, _ := m["path"].(string)
		doRead(c, id, path)
	case "write":
		path, _ := m["path"].(string)
		content, _ := m["content"].(string)
		doWrite(c, id, path, content)
	case "rename":
		from, _ := m["from"].(string)
		to, _ := m["to"].(string)
		replace, _ := m["replace"].(bool)
		doRename(c, id, from, to, replace)
	case "delete":
		path, _ := m["path"].(string)
		doDelete(c, id, path)
	case "spawn":
		// FE-driven app spawn (e.g. the "Open in fm" button). The
		// router validates CapSpawn on the manifest; without it,
		// SpawnRequest returns ErrCodeForbidden.
		appID, _ := m["app_id"].(string)
		if appID == "" {
			return
		}
		if err := c.SpawnRequest(appID); err != nil {
			log.Printf("wash-edit spawn %s: %v", appID, err)
		}
	case "save_state":
		// Persist the FE's state blob via the SDK's SaveState. The
		// router stores it keyed by this instance's id and replays
		// to the FE on next mount as a `wash:state` event.
		state, ok := m["state"]
		if !ok {
			return
		}
		if err := c.SaveState(state); err != nil {
			log.Printf("wash-edit save_state: %v", err)
		}
	case "term.open":
		cols := sdk.ToUint64(m["cols"])
		rows := sdk.ToUint64(m["rows"])
		if cols == 0 {
			cols = 80
		}
		if rows == 0 {
			rows = 24
		}
		// OpenChannel must not run on the read goroutine; hand off.
		go openTerm(c, id, uint16(cols), uint16(rows))
	case "term.resize":
		chID := sdk.ToUint64(m["channel_id"])
		cols := sdk.ToUint64(m["cols"])
		rows := sdk.ToUint64(m["rows"])
		if chID == 0 || cols == 0 || rows == 0 {
			return
		}
		termMu.Lock()
		sess := termSessions[uint32(chID)]
		termMu.Unlock()
		if sess == nil {
			return
		}
		if err := sess.Resize(uint16(cols), uint16(rows)); err != nil {
			log.Printf("wash-edit term.resize ch=%d: %v", chID, err)
		}
	case "term.close":
		chID := sdk.ToUint64(m["channel_id"])
		if chID == 0 {
			return
		}
		termMu.Lock()
		sess := termSessions[uint32(chID)]
		termMu.Unlock()
		if sess != nil {
			sess.CloseWithReason("user requested")
		}
	}
}

// openTerm spawns a shell, opens a raw channel, and wires them via
// internal/pty. Once both directions are flowing the FE sees
// `term.opened` { id, channel_id }.
func openTerm(c *sdk.Conn, replyID string, cols, rows uint16) {
	sess, err := pty.Open(context.Background(), c, c.WindowID(), cols, rows, nil, pty.PinTerm, func(s *pty.Session, reason string) {
		// onClose fires from the PTY goroutine when the shell exits.
		termMu.Lock()
		_, found := termSessions[s.ID()]
		delete(termSessions, s.ID())
		termMu.Unlock()
		if !found {
			return
		}
		_ = c.SendAppMsg(map[string]any{
			"kind":       "term.closed",
			"channel_id": uint64(s.ID()),
			"reason":     reason,
		})
	})
	if err != nil {
		log.Printf("wash-edit term open: %v", err)
		_ = c.SendAppMsg(map[string]any{"kind": "term.open_err", "id": replyID, "msg": err.Error()})
		return
	}
	termMu.Lock()
	termSessions[sess.ID()] = sess
	termMu.Unlock()

	log.Printf("wash-edit term opened ch=%d shell=%s pid=%d", sess.ID(), sess.Shell, sess.Cmd().Process.Pid)
	_ = c.SendAppMsg(map[string]any{
		"kind":       "term.opened",
		"id":         replyID,
		"channel_id": uint64(sess.ID()),
		"shell":      sess.Shell,
	})
}

func doList(c *sdk.Conn, id, path string) {
	if path == "" {
		sendErr(c, "list_err", id, path, "bad_request", "missing path")
		return
	}
	// "/" gets resolved to a useful default so the FE boot doesn't
	// have to chain calls. With a sandbox configured it means the
	// sandbox root; without one it means $HOME (DefaultStart) — the
	// unconfined editor lands the user in their home dir instead of
	// the filesystem root. Path-bar navigation still works above it.
	if path == "/" {
		if root != "" {
			path = root
		} else {
			path = wfs.DefaultStart()
		}
	}
	entries, abs, truncated, err := editFS.List(path, maxListEntries)
	if err != nil {
		sendErr(c, "list_err", id, path, wfs.ErrCode(err), err.Error())
		return
	}
	_ = c.SendAppMsg(wfs.ListReply{Kind: "list_ok", ID: id, Path: abs, Entries: entries, Truncated: truncated})
}

func doRead(c *sdk.Conn, id, path string) {
	if path == "" {
		sendErr(c, "read_err", id, path, "bad_request", "missing path")
		return
	}
	abs, err := editFS.Confine(path)
	if err != nil {
		sendErr(c, "read_err", id, path, wfs.ErrCode(err), err.Error())
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		sendErr(c, "read_err", id, abs, wfs.ErrCode(err), err.Error())
		return
	}
	if info.IsDir() {
		sendErr(c, "read_err", id, abs, "is_dir", "path is a directory")
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		sendErr(c, "read_err", id, abs, wfs.ErrCode(err), err.Error())
		return
	}
	defer f.Close()
	buf := make([]byte, maxReadBytes)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		// EOF on an empty file is fine; only error if read truly failed.
		if info.Size() != 0 {
			sendErr(c, "read_err", id, abs, "io", err.Error())
			return
		}
	}
	buf = buf[:n]
	truncated := info.Size() > int64(n)
	binary := looksBinary(buf)
	content := ""
	if !binary {
		content = string(buf)
	}
	_ = c.SendAppMsg(wfs.ReadReply{
		Kind:      "read_ok",
		ID:        id,
		Path:      abs,
		Content:   content,
		Size:      info.Size(),
		Binary:    binary,
		Truncated: truncated,
	})
}

// doRename wraps internal/fs.Rename. Mirrors wash-fm's protocol:
// reply with rename_ok { from, to } on success, rename_err on
// failure. `replace=true` removes a clobber-able destination
// first; non-empty dirs return code=not_empty_dir.
func doRename(c *sdk.Conn, id, from, to string, replace bool) {
	src, dst, err := editFS.Rename(from, to, replace)
	if err != nil {
		path := src
		if path == "" {
			path = from
		}
		sendErr(c, "rename_err", id, path, wfs.ErrCode(err), err.Error())
		return
	}
	_ = c.SendAppMsg(map[string]any{
		"kind": "rename_ok", "id": id, "from": src, "to": dst,
	})
}

// doDelete wraps internal/fs.Delete for single-path deletes. The
// editor's FE routes recursive deletes (non-empty dirs, multi)
// through wash-bulk instead — fm-direct is the synchronous fast
// path for the easy case.
func doDelete(c *sdk.Conn, id, path string) {
	abs, err := editFS.Delete(path)
	if err != nil {
		p := abs
		if p == "" {
			p = path
		}
		sendErr(c, "delete_err", id, p, wfs.ErrCode(err), err.Error())
		return
	}
	_ = c.SendAppMsg(map[string]any{
		"kind": "delete_ok", "id": id, "path": abs,
	})
}

func doWrite(c *sdk.Conn, id, path, content string) {
	abs, n, err := editFS.Write(path, []byte(content), maxWriteBytes)
	if err != nil {
		p := abs
		if p == "" {
			p = path
		}
		sendErr(c, "write_err", id, p, wfs.ErrCode(err), err.Error())
		return
	}
	_ = c.SendAppMsg(wfs.WriteReply{Kind: "write_ok", ID: id, Path: abs, Bytes: n})
}

func sendErr(c *sdk.Conn, kind, id, path, code, msg string) {
	_ = c.SendAppMsg(wfs.ErrReply{Kind: kind, ID: id, Path: path, Code: code, Msg: msg})
}

// looksBinary inspects bytes for NUL — wash-fm's heuristic. Good
// enough for the open-or-bail decision.
func looksBinary(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

const editIcon = "file-pen"
