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
	"embed"
	"io/fs"
	"log"
	"os"
	"sync"

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
	}
}

func doList(c *sdk.Conn, id, path string) {
	if path == "" {
		sendErr(c, "list_err", id, path, "bad_request", "missing path")
		return
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
