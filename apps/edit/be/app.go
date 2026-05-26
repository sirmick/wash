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
package edit

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"os"
	"sync"

	"github.com/sirmick/wash/internal/apps/registry"
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

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-edit: assets: " + err.Error())
	}
	def = &sdk.AppDef{
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
		Assets:  sub,
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-edit",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

var bus *sdk.Bus

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	mu.Lock()
	root = c.Session().Root
	editFS = wfs.New(root)
	mu.Unlock()
	// FilePicker bridge: one-liner; the picker FE addresses the
	// editor's own BE. EnableFilePicker installs its own OnAppMsg
	// chain on c; the bus then wraps that, so fs.* messages still
	// route to the filepicker handler while everything else flows
	// through the bus.
	sdk.EnableFilePicker(c)

	bus = sdk.NewBus(c)
	registerHandlers(bus)

	if root == "" {
		log.Printf("wash-edit ready instance=%s window=%d (unconfined)", instanceID, windowID)
	} else {
		log.Printf("wash-edit ready instance=%s window=%d root=%s", instanceID, windowID, root)
	}
}

// ----- request/response types -----

type listReq struct {
	Path string `json:"path"`
}

type readReq struct {
	Path string `json:"path"`
}

type writeReq struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type renameReq struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Replace bool   `json:"replace"`
}

type pathReq struct {
	Path string `json:"path"`
}

type spawnReq struct {
	AppID string `json:"app_id"`
}

type saveStateReq struct {
	State any `json:"state"`
}

type termOpenReq struct {
	Cols uint64 `json:"cols"`
	Rows uint64 `json:"rows"`
}

type termResizeReq struct {
	ChannelID uint64 `json:"channel_id"`
	Cols      uint64 `json:"cols"`
	Rows      uint64 `json:"rows"`
}

type termCloseReq struct {
	ChannelID uint64 `json:"channel_id"`
}

type termOpenedEvent struct {
	ChannelID uint64 `json:"channel_id"`
	Shell     string `json:"shell"`
}

type termClosedEvent struct {
	ChannelID uint64 `json:"channel_id"`
	Reason    string `json:"reason"`
}

// ----- handler registration -----

func registerHandlers(b *sdk.Bus) {
	// cmd.* — FE-owned passthrough. The bus pattern routes any kind
	// starting with "cmd." into this single handler, which echoes the
	// message back unchanged so test drivers and other apps targeting
	// either side see the same wire.
	b.HandlePattern("cmd.", func(c *sdk.Conn, _ string, data map[string]any) {
		_ = c.SendAppMsg(data)
	})

	sdk.Handle(b, "list", func(_ *sdk.Conn, _ string, req listReq) (wfs.ListReply, error) {
		path := req.Path
		// "/" gets resolved to a useful default so the FE boot doesn't
		// have to chain calls.
		if path == "/" {
			if root != "" {
				path = root
			} else {
				path = wfs.DefaultStart()
			}
		}
		if path == "" {
			return wfs.ListReply{}, sdk.Errf(sdk.ErrBadRequest, "missing path")
		}
		entries, abs, truncated, err := editFS.List(path, maxListEntries)
		if err != nil {
			return wfs.ListReply{}, sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
		}
		return wfs.ListReply{Path: abs, Entries: entries, Truncated: truncated}, nil
	})

	sdk.Handle(b, "read", func(_ *sdk.Conn, _ string, req readReq) (wfs.ReadReply, error) {
		return doRead(req.Path)
	})

	sdk.Handle(b, "write", func(_ *sdk.Conn, _ string, req writeReq) (wfs.WriteReply, error) {
		abs, n, err := editFS.Write(req.Path, []byte(req.Content), maxWriteBytes)
		if err != nil {
			return wfs.WriteReply{}, sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
		}
		return wfs.WriteReply{Path: abs, Bytes: n}, nil
	})

	sdk.Handle(b, "rename", func(_ *sdk.Conn, _ string, req renameReq) (wfs.RenameReply, error) {
		src, dst, err := editFS.Rename(req.From, req.To, req.Replace)
		if err != nil {
			return wfs.RenameReply{}, sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
		}
		return wfs.RenameReply{From: src, To: dst}, nil
	})

	sdk.Handle(b, "delete", func(_ *sdk.Conn, _ string, req pathReq) (wfs.PathReply, error) {
		abs, err := editFS.Delete(req.Path)
		if err != nil {
			return wfs.PathReply{}, sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
		}
		return wfs.PathReply{Path: abs}, nil
	})

	sdk.HandleVoid(b, "spawn", func(c *sdk.Conn, _ string, req spawnReq) error {
		// FE-driven app spawn (e.g. the "Open in fm" button). The
		// router validates CapSpawn on the manifest.
		if req.AppID == "" {
			return nil
		}
		return c.SpawnRequest(req.AppID)
	})

	sdk.HandleVoid(b, "save_state", func(c *sdk.Conn, _ string, req saveStateReq) error {
		return c.SaveState(req.State)
	})

	sdk.HandleVoid(b, "term.open", func(c *sdk.Conn, id string, req termOpenReq) error {
		cols := req.Cols
		rows := req.Rows
		if cols == 0 {
			cols = 80
		}
		if rows == 0 {
			rows = 24
		}
		// OpenChannel must not run on the read goroutine; hand off.
		go openTerm(c, id, uint16(cols), uint16(rows))
		return nil
	})

	sdk.HandleVoid(b, "term.resize", func(_ *sdk.Conn, _ string, req termResizeReq) error {
		if req.ChannelID == 0 || req.Cols == 0 || req.Rows == 0 {
			return nil
		}
		termMu.Lock()
		sess := termSessions[uint32(req.ChannelID)]
		termMu.Unlock()
		if sess == nil {
			return nil
		}
		return sess.Resize(uint16(req.Cols), uint16(req.Rows))
	})

	sdk.HandleVoid(b, "term.close", func(_ *sdk.Conn, _ string, req termCloseReq) error {
		if req.ChannelID == 0 {
			return nil
		}
		termMu.Lock()
		sess := termSessions[uint32(req.ChannelID)]
		termMu.Unlock()
		if sess != nil {
			sess.CloseWithReason("user requested")
		}
		return nil
	})
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
		_ = bus.Emit("term.closed", termClosedEvent{ChannelID: uint64(s.ID()), Reason: reason})
	})
	if err != nil {
		log.Printf("wash-edit term open: %v", err)
		_ = bus.Emit("term.open_err", map[string]any{"id": replyID, "msg": err.Error()})
		return
	}
	termMu.Lock()
	termSessions[sess.ID()] = sess
	termMu.Unlock()

	log.Printf("wash-edit term opened ch=%d shell=%s pid=%d", sess.ID(), sess.Shell, sess.Cmd().Process.Pid)
	// term.opened carries the FE's reply id so the FE can match the
	// open request to its initiator. Emit-with-id achieves this by
	// going via the bus's envelope path.
	out := map[string]any{
		"channel_id": uint64(sess.ID()),
		"shell":      sess.Shell,
	}
	if replyID != "" {
		out["id"] = replyID
	}
	_ = bus.Emit("term.opened", out)
}

// doRead loads up to maxReadBytes of path. Returns sdk.Err with the
// canonical code on failure; the bus wraps it as read_err.
func doRead(path string) (wfs.ReadReply, error) {
	if path == "" {
		return wfs.ReadReply{}, sdk.Errf(sdk.ErrBadRequest, "missing path")
	}
	abs, err := editFS.Confine(path)
	if err != nil {
		return wfs.ReadReply{}, sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
	}
	info, err := os.Stat(abs)
	if err != nil {
		return wfs.ReadReply{}, sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
	}
	if info.IsDir() {
		return wfs.ReadReply{}, sdk.Errf("is_dir", "path is a directory")
	}
	f, err := os.Open(abs)
	if err != nil {
		return wfs.ReadReply{}, sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
	}
	defer f.Close()
	buf := make([]byte, maxReadBytes)
	n, err := f.Read(buf)
	if err != nil && n == 0 && info.Size() != 0 {
		return wfs.ReadReply{}, sdk.Err{Code: sdk.ErrIO, Msg: err.Error()}
	}
	buf = buf[:n]
	truncated := info.Size() > int64(n)
	binary := looksBinary(buf)
	content := ""
	if !binary {
		content = string(buf)
	}
	return wfs.ReadReply{
		Path:      abs,
		Content:   content,
		Size:      info.Size(),
		Binary:    binary,
		Truncated: truncated,
	}, nil
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
