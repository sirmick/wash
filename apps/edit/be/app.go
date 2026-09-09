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
//	FE → BE  : { kind: "list",  path }                     id-correlated
//	             { kind: "read",  path }
//	             { kind: "write", path, content }
//	             fs.* messages handled by sdk.EnableFilePicker
//	             prefs / prefs_set / recent_add / recent_drop — prefs.go
//	             find / find_cancel — find.go
//
//	BE → FE  : { kind: "list_ok", id?, path, entries, truncated }
//	             { kind: "read_ok", id?, path, content, size, binary, truncated,
//	                                writable }
//	             { kind: "write_ok", id?, path, bytes }
//	             { kind: "<op>_err", id?, path?, code, msg }
package edit

import (
	"context"
	"embed"
	"errors"
	"github.com/sirmick/wash/internal/version"
	"io"
	"io/fs"
	"log"
	"os"
	"sync"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/internal/pty"
	"github.com/sirmick/wash/pkg/apps/registry"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

//go:embed all:assets
var assetsFS embed.FS

const (
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
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-edit",
			Surface:         sdk.SurfaceWindow,
			Icon:            editIcon,
			Accent:          "#e0b060",
			Instancing:      sdk.InstancingMulti,
			Window:          &sdk.WindowHints{DefaultWidth: 900, DefaultHeight: 600},
			// Declared so the FE's "Reveal in Files" button can ask the
			// router to spawn fm via SpawnRequest. The router checks
			// this capability before honoring the request.
			Capabilities: []string{sdk.CapSpawn},
			// Text/code extensions the editor handles for open routing
			// (fm double-click → router → wash-edit --open <path>).
			Opens: []string{
				".txt", ".md", ".markdown", ".rst", ".log", ".csv", ".tsv",
				".go", ".ts", ".tsx", ".js", ".jsx", ".json", ".py", ".rb",
				".rs", ".c", ".h", ".cc", ".cpp", ".hpp", ".java", ".kt",
				".sh", ".bash", ".zsh", ".fish", ".html", ".css", ".scss",
				".xml", ".yaml", ".yml", ".toml", ".ini", ".conf", ".env",
				".sql", ".lua", ".pl", ".php", ".swift", ".gitignore",
			},
		},
		Assets:  sub,
		OnReady: onReady,
		// The FE owns the dirty state, so the close handshake is answered
		// there: see onCloseRequested.
		OnCloseRequested: onCloseRequested,
		// Installed BEFORE the bus, so NewBus captures it as the chain
		// target: agentd's replies (transcript_snapshot, transcript_event,
		// agent_started, state) have no bus handler of their own and fall
		// through to here, where the keyed relay routes them.
		OnAppMsgFrom: func(_ *sdk.Conn, _ uint32, data any, from wire.Sender) {
			onAgentMsgFrom(data, from)
		},
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

	// The relay must exist before the bus, since the bus chains unhandled
	// messages into it.
	initAgent(c)
	bus = sdk.NewBus(c)
	registerHandlers(bus)
	registerPrefsHandlers(bus)
	registerFindHandlers(bus)
	registerAgentHandlers(bus)

	if root == "" {
		log.Printf("wash-edit ready instance=%s window=%d (unconfined)", instanceID, windowID)
	} else {
		log.Printf("wash-edit ready instance=%s window=%d root=%s", instanceID, windowID, root)
	}

	// Launched via the router's open routing (fm double-click → wash-edit
	// --open <path>): drive the FE to that file. cmd.open_file is the same
	// hook external drivers already use, so the FE opens it in a tab.
	if p := c.LaunchOpenPath(); p != "" {
		_ = bus.Emit("cmd.open_file", map[string]any{"path": p})
	}
}

// ----- request/response types -----

// list/read/write/rename/path request shapes are shared with wash-fm and
// live in internal/fs (wfs.ListReq, ReadReq, WriteReq, RenameReq, PathReq).

type spawnReq struct {
	AppID string `json:"app_id"`
	// Open is a launch path for the target (`--open <path>`): "Reveal in
	// Files" spawns fm at the active file's folder.
	Open string `json:"open"`
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

	sdk.Handle(b, "list", func(_ *sdk.Conn, _ string, req wfs.ListReq) (wfs.ListReply, error) {
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

	// The reply is a map, not wfs.ReadReply, for one extra key: whether
	// the file can be written. internal/fs's wire types are shared with
	// wash-fm, and an editor-only fact does not belong in them.
	sdk.Handle(b, "read", func(_ *sdk.Conn, _ string, req wfs.ReadReq) (map[string]any, error) {
		reply, err := doRead(req.Path)
		if err != nil {
			return nil, err
		}
		out := map[string]any{
			"path":      reply.Path,
			"content":   reply.Content,
			"size":      reply.Size,
			"binary":    reply.Binary,
			"truncated": reply.Truncated,
			"writable":  writable(reply.Path),
		}
		if reply.Blocked != "" {
			out["blocked"] = reply.Blocked
		}
		return out, nil
	})

	sdk.Handle(b, "write", func(c *sdk.Conn, _ string, req wfs.WriteReq) (wfs.WriteReply, error) {
		abs, n, err := editFS.Write(req.Path, []byte(req.Content), maxWriteBytes)
		if err != nil {
			// A save that fails is the one thing an editor must never be
			// quiet about: the buffer looks saved and is not. The FE puts
			// it in the status bar; the toast reaches a user who has
			// already switched windows.
			log.Printf("wash-edit: write failed path=%q: %v", req.Path, err)
			c.Warn("Save failed", err.Error())
			return wfs.WriteReply{}, sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
		}
		return wfs.WriteReply{Path: abs, Bytes: n}, nil
	})

	// close_window_confirmed: the FE has established that nothing unsaved
	// remains (or the user chose to discard it). An unsolicited
	// confirm_close(allow=true) runs the same teardown as a confirmed
	// titlebar click — the term app's pattern (WIRE.md §10).
	sdk.HandleVoid(b, "close_window_confirmed", func(c *sdk.Conn, _ string, _ struct{}) error {
		return c.ConfirmClose(c.WindowID(), true)
	})

	sdk.Handle(b, "rename", func(_ *sdk.Conn, _ string, req wfs.RenameReq) (wfs.RenameReply, error) {
		src, dst, err := editFS.Rename(req.From, req.To, req.Replace)
		if err != nil {
			return wfs.RenameReply{}, sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
		}
		return wfs.RenameReply{From: src, To: dst}, nil
	})

	sdk.Handle(b, "delete", func(_ *sdk.Conn, _ string, req wfs.PathReq) (wfs.PathReply, error) {
		abs, err := editFS.Delete(req.Path)
		if err != nil {
			return wfs.PathReply{}, sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
		}
		return wfs.PathReply{Path: abs}, nil
	})

	sdk.HandleVoid(b, "spawn", func(c *sdk.Conn, _ string, req spawnReq) error {
		// FE-driven app spawn (e.g. the "Reveal in Files" button). The
		// router validates CapSpawn on the manifest. The launch path is
		// confined here so the editor never hands the router a path
		// outside its own root.
		if req.AppID == "" {
			return nil
		}
		if req.Open != "" {
			abs, err := editFS.Confine(req.Open)
			if err != nil {
				return sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
			}
			return c.SpawnRequestOpen(req.AppID, abs)
		}
		return c.SpawnRequest(req.AppID)
	})

	sdk.HandlePersist(b)

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
	// ReadFull rather than one Read: a FUSE or network file may return
	// short reads, and a short first read used to be mistaken for the
	// whole file.
	buf := make([]byte, maxReadBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return wfs.ReadReply{}, sdk.Err{Code: sdk.ErrIO, Msg: err.Error()}
	}
	buf = buf[:n]
	truncated := info.Size() > int64(n)
	reply := wfs.ReadReply{Path: abs, Size: info.Size(), Truncated: truncated}
	// Anything the editor cannot faithfully write back is delivered
	// without content and with the reason, so the FE shows a placeholder
	// instead of a buffer whose save would destroy the file: a binary
	// save used to write an empty document, a >cap save used to write
	// the truncated prefix, and a Latin-1 save used to persist U+FFFD.
	switch {
	case wfs.LooksBinary(buf):
		reply.Binary = true
		reply.Blocked = wfs.BlockedBinary
	case truncated:
		reply.Blocked = wfs.BlockedTooLarge
	case !utf8.Valid(buf):
		reply.Blocked = wfs.BlockedEncoding
	default:
		reply.Content = string(buf)
	}
	return reply, nil
}

// writable answers whether THIS process could write the file: access(2)
// with W_OK, so it accounts for the effective uid, the group, and a
// read-only mount — not just the mode bits, which say nothing about who
// is asking. Running as root it is true for everything, which is correct
// (root really can write it) and is why the read-only e2e skips there.
// A file that does not exist is reported writable: the editor's own
// "deleted on disk" path owns that case, and saying "read-only" about a
// file that is merely gone would be a lie.
func writable(path string) bool {
	if path == "" {
		return true
	}
	if err := unix.Access(path, unix.W_OK); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true
		}
		return false
	}
	return true
}

// onCloseRequested answers the router's close handshake (WIRE.md §10).
// Only the FE knows whether a tab is dirty, so the answer is always an
// immediate veto plus a question to the FE, which replies with
// close_window_confirmed once it has nothing unsaved (straight away when
// every tab is clean, after the Save / Don't save / Cancel dialog when
// not). Answering "no" first is what makes a dialog possible at all: the
// router force-kills an app that leaves the handshake open past its
// grace period, which is far too short to read a question in.
func onCloseRequested(c *sdk.Conn, win uint32) bool {
	if err := c.SendAppMsg(map[string]any{"kind": "close_blocked", "scope": "window"}); err != nil {
		// No FE to ask means nobody is typing into it either; honour the
		// click rather than leave a window that refuses to shut.
		log.Printf("wash-edit close prompt: %v", err)
		return true
	}
	return false
}

const editIcon = "file-pen"
