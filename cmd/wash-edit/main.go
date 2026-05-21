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
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
	wfs "github.com/sirmick/wash/internal/fs"
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

// termSession is one PTY+raw-channel pair. The editor opens a new
// one per terminal tab and tears it down on tab close. Channel id
// is the natural session id since the FE addresses raw bytes by
// channel.
type termSession struct {
	pty *os.File
	cmd *exec.Cmd
	ch  *sdk.RawChannel
}

var (
	termMu       sync.Mutex
	termSessions = map[uint32]*termSession{}
)

// isPtyTerm — same heuristic as wash-term. The pty's slave going
// away on Linux yields EIO; closing the master yields ErrClosed;
// EOF is the clean exit. None of those are real errors.
func isPtyTerm(err error) bool {
	return err == nil || errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed)
}

func userShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/bash"
}

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

// All response structs carry an optional ID echoing the request's
// id for correlation. Same shape as wash-fm's so any future SDK
// reply-correlation helper can drop in unchanged.
type listResult struct {
	Kind      string      `json:"kind"`
	ID        string      `json:"id,omitempty"`
	Path      string      `json:"path"`
	Entries   []wfs.Entry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

type readResult struct {
	Kind      string `json:"kind"`
	ID        string `json:"id,omitempty"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
}

type writeResult struct {
	Kind  string `json:"kind"`
	ID    string `json:"id,omitempty"`
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

type errResult struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Path string `json:"path,omitempty"`
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

func onAppMsg(c *sdk.Conn, _ uint32, data any) {
	m, ok := data.(map[any]any)
	if !ok {
		return
	}
	kind, _ := m["kind"].(string)
	id, _ := m["id"].(string)
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
		cols := toUint(m["cols"])
		rows := toUint(m["rows"])
		if cols == 0 {
			cols = 80
		}
		if rows == 0 {
			rows = 24
		}
		// OpenChannel must not run on the read goroutine; hand off.
		go openTerm(c, id, uint16(cols), uint16(rows))
	case "term.resize":
		chID := toUint(m["channel_id"])
		cols := toUint(m["cols"])
		rows := toUint(m["rows"])
		if chID == 0 || cols == 0 || rows == 0 {
			return
		}
		termMu.Lock()
		sess := termSessions[uint32(chID)]
		termMu.Unlock()
		if sess == nil {
			return
		}
		if err := pty.Setsize(sess.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
			log.Printf("wash-edit term.resize ch=%d: %v", chID, err)
		}
	case "term.close":
		chID := toUint(m["channel_id"])
		if chID == 0 {
			return
		}
		termMu.Lock()
		sess := termSessions[uint32(chID)]
		termMu.Unlock()
		if sess != nil {
			_ = sess.pty.Close()
			_ = sess.cmd.Process.Kill()
		}
	}
}

// toUint normalizes CBOR's mixed numeric shapes (int64 / uint64 /
// float64) into uint64 for size + channel id fields.
func toUint(v any) uint64 {
	switch x := v.(type) {
	case uint64:
		return x
	case int64:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case float64:
		return uint64(x)
	}
	return 0
}

// openTerm spawns a shell, opens a raw channel, and io.Copy's the
// two ends together. Once both directions are flowing the FE sees
// `term.opened` { id, channel_id } and can wire xterm into the
// channel. Mirrors wash-term's openTab almost exactly — when the
// pty/term primitives are extracted to internal/pty/ both apps
// will collapse onto the same code.
func openTerm(c *sdk.Conn, replyID string, cols, rows uint16) {
	ch, err := c.OpenChannel(context.Background(), c.WindowID())
	if err != nil {
		log.Printf("wash-edit term open channel: %v", err)
		_ = c.SendAppMsg(map[string]any{"kind": "term.open_err", "id": replyID, "msg": err.Error()})
		return
	}
	shell := userShell()
	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, startErr := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if startErr != nil {
		log.Printf("wash-edit term pty.Start %s: %v", shell, startErr)
		_ = ch.Close()
		_ = c.SendAppMsg(map[string]any{"kind": "term.open_err", "id": replyID, "msg": startErr.Error()})
		return
	}
	sess := &termSession{pty: f, cmd: cmd, ch: ch}
	termMu.Lock()
	termSessions[ch.ID()] = sess
	termMu.Unlock()

	log.Printf("wash-edit term opened ch=%d shell=%s pid=%d", ch.ID(), shell, cmd.Process.Pid)
	_ = c.SendAppMsg(map[string]any{
		"kind":       "term.opened",
		"id":         replyID,
		"channel_id": uint64(ch.ID()),
		"shell":      shell,
	})

	// pty → channel
	go func() {
		_, copyErr := io.Copy(ch, f)
		if !isPtyTerm(copyErr) {
			log.Printf("wash-edit term pty→ch ch=%d: %v", ch.ID(), copyErr)
		}
		cleanupTerm(c, ch.ID(), "pty eof")
	}()
	// channel → pty
	go func() {
		_, copyErr := io.Copy(f, ch)
		if !isPtyTerm(copyErr) {
			log.Printf("wash-edit term ch→pty ch=%d: %v", ch.ID(), copyErr)
		}
		_ = cmd.Process.Kill()
	}()
	go func() {
		_ = cmd.Wait()
	}()
}

func cleanupTerm(c *sdk.Conn, chID uint32, reason string) {
	termMu.Lock()
	sess, ok := termSessions[chID]
	delete(termSessions, chID)
	termMu.Unlock()
	if !ok {
		return
	}
	_ = sess.pty.Close()
	_ = sess.ch.Close()
	_ = c.SendAppMsg(map[string]any{
		"kind":       "term.closed",
		"channel_id": uint64(chID),
		"reason":     reason,
	})
}

func doList(c *sdk.Conn, id, path string) {
	if path == "" {
		sendErr(c, "list_err", id, path, "bad_request", "missing path")
		return
	}
	// When a sandbox is configured and the caller asks for "/", they
	// mean the sandbox root — same affordance the FilePicker gets
	// via fs.root recovery, just applied at the BE so the FE boot
	// doesn't have to chain calls.
	if path == "/" && root != "" {
		path = root
	}
	entries, abs, truncated, err := editFS.List(path, maxListEntries)
	if err != nil {
		sendErr(c, "list_err", id, path, wfs.ErrCode(err), err.Error())
		return
	}
	_ = c.SendAppMsg(listResult{Kind: "list_ok", ID: id, Path: abs, Entries: entries, Truncated: truncated})
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
	_ = c.SendAppMsg(readResult{
		Kind:      "read_ok",
		ID:        id,
		Path:      abs,
		Content:   content,
		Size:      info.Size(),
		Binary:    binary,
		Truncated: truncated,
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
	_ = c.SendAppMsg(writeResult{Kind: "write_ok", ID: id, Path: abs, Bytes: n})
}

func sendErr(c *sdk.Conn, kind, id, path, code, msg string) {
	_ = c.SendAppMsg(errResult{Kind: kind, ID: id, Path: path, Code: code, Msg: msg})
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
