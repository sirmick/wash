// wash-fm — a single-pane file manager. The BE syscalls directly
// (architecture's "BEs run as the user, may syscall" path) rather
// than going through a router-side fs service. When other apps
// (text editor, image viewer) need fs access we'll add the dialog
// provider pattern; for v0.1 wash-fm owns its access.
package main

import (
	"embed"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sirmick/wash/internal/sdk"
)

//go:embed all:assets
var assetsFS embed.FS

const (
	version = "0.0.0"

	// Cap on preview/read size to keep memory bounded. Larger files
	// are still listed, just not previewable here yet.
	maxReadBytes = 256 * 1024

	// Cap on directory listing size — huge dirs (e.g. /usr/bin) get
	// truncated with a flag so the FE doesn't lock up rendering.
	maxListEntries = 5_000
)

type entry struct {
	Name    string `json:"name" cbor:"name"`
	Type    string `json:"type" cbor:"type"` // "dir" | "file" | "symlink" | "other"
	Size    int64  `json:"size" cbor:"size"`
	ModUnix int64  `json:"mod_unix" cbor:"mod_unix"`
	Perm    string `json:"perm" cbor:"perm"`              // "rwxr-xr--" 9-char human form
	Mode    uint32 `json:"mode" cbor:"mode"`              // raw octal-style bits
	LinkTo  string `json:"link_to,omitempty" cbor:"link_to,omitempty"`
	LinkErr string `json:"link_err,omitempty" cbor:"link_err,omitempty"`
}

type listResult struct {
	Kind      string  `json:"kind"`
	Path      string  `json:"path"`
	Entries   []entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

type readResult struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
	Size      int64  `json:"size"`
}

type errResult struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

func main() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("wash-fm: assets: %v", err)
	}
	sdk.Main(&sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.fm",
			Name:            "Files",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-fm",
			Surface:         sdk.SurfaceWindow,
			Icon:            fmIcon,
			Instancing:      sdk.InstancingMulti,
			Window:          &sdk.WindowHints{DefaultWidth: 760, DefaultHeight: 520},
		},
		Assets:   sub,
		OnReady:  onReady,
		OnAppMsg: onAppMsg,
	})
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-fm ready instance=%s window=%d", instanceID, windowID)
	// Send initial home-directory listing so the FE has something
	// to show on first paint.
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/"
	}
	sendList(c, home)
}

func onAppMsg(c *sdk.Conn, _ uint32, data any) {
	m, ok := data.(map[any]any)
	if !ok {
		return
	}
	switch kind, _ := m["kind"].(string); kind {
	case "list":
		path, _ := m["path"].(string)
		sendList(c, path)
	case "read":
		path, _ := m["path"].(string)
		sendRead(c, path)
	}
}

// sendList lists the directory at path and sends list_ok / list_err.
// Symlinks are reported but not followed; the FE can re-list the
// target on demand.
func sendList(c *sdk.Conn, path string) {
	if path == "" {
		sendErr(c, "list_err", path, "bad_request", "missing path")
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		sendErr(c, "list_err", path, "bad_path", err.Error())
		return
	}
	dir, err := os.Open(abs)
	if err != nil {
		sendErr(c, "list_err", abs, fsErrCode(err), err.Error())
		return
	}
	defer dir.Close()
	infos, err := dir.Readdir(-1)
	if err != nil {
		sendErr(c, "list_err", abs, "io", err.Error())
		return
	}
	truncated := false
	if len(infos) > maxListEntries {
		infos = infos[:maxListEntries]
		truncated = true
	}
	out := make([]entry, 0, len(infos))
	for _, fi := range infos {
		e := entry{
			Name:    fi.Name(),
			Type:    typeOf(fi),
			Size:    fi.Size(),
			ModUnix: fi.ModTime().Unix(),
			Perm:    formatPerm(fi.Mode()),
			Mode:    uint32(fi.Mode().Perm()),
		}
		if e.Type == "symlink" {
			full := filepath.Join(abs, fi.Name())
			if target, err := os.Readlink(full); err == nil {
				e.LinkTo = target
			} else {
				e.LinkErr = err.Error()
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		// Directories first, then alphabetical.
		di := out[i].Type == "dir"
		dj := out[j].Type == "dir"
		if di != dj {
			return di
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	res := listResult{Kind: "list_ok", Path: abs, Entries: out, Truncated: truncated}
	if err := c.SendAppMsg(res); err != nil {
		log.Printf("wash-fm send list_ok: %v", err)
	}
}

// sendRead reads up to maxReadBytes of path. If the bytes look
// binary, content is empty and Binary=true.
func sendRead(c *sdk.Conn, path string) {
	if path == "" {
		sendErr(c, "read_err", path, "bad_request", "missing path")
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		sendErr(c, "read_err", path, "bad_path", err.Error())
		return
	}
	st, err := os.Stat(abs)
	if err != nil {
		sendErr(c, "read_err", abs, fsErrCode(err), err.Error())
		return
	}
	if st.IsDir() {
		sendErr(c, "read_err", abs, "is_dir", "path is a directory")
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		sendErr(c, "read_err", abs, fsErrCode(err), err.Error())
		return
	}
	defer f.Close()
	buf := make([]byte, maxReadBytes)
	n, err := f.Read(buf)
	if err != nil && err.Error() != "EOF" {
		// io.EOF on empty file is fine; other read errors propagate.
		if n == 0 {
			sendErr(c, "read_err", abs, "io", err.Error())
			return
		}
	}
	chunk := buf[:n]
	binary := looksBinary(chunk)
	res := readResult{
		Kind:      "read_ok",
		Path:      abs,
		Size:      st.Size(),
		Truncated: int64(n) < st.Size(),
		Binary:    binary,
	}
	if !binary {
		res.Content = string(chunk)
	}
	if err := c.SendAppMsg(res); err != nil {
		log.Printf("wash-fm send read_ok: %v", err)
	}
}

func sendErr(c *sdk.Conn, kind, path, code, msg string) {
	if err := c.SendAppMsg(errResult{Kind: kind, Path: path, Code: code, Msg: msg}); err != nil {
		log.Printf("wash-fm send %s: %v", kind, err)
	}
}

// typeOf returns the entry type tag for a fs.FileInfo.
func typeOf(fi os.FileInfo) string {
	m := fi.Mode()
	switch {
	case m.IsDir():
		return "dir"
	case m&os.ModeSymlink != 0:
		return "symlink"
	case m.IsRegular():
		return "file"
	default:
		return "other"
	}
}

// fsErrCode maps common os errors to short codes the FE can render.
func fsErrCode(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	case errors.Is(err, os.ErrPermission):
		return "denied"
	}
	return "io"
}

// looksBinary inspects the first chunk for NUL bytes. Cheap and
// good enough for the preview-or-don't decision.
func looksBinary(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// formatPerm renders a 9-char rwx string for a Unix file mode.
func formatPerm(m os.FileMode) string {
	const set = "rwxrwxrwx"
	out := []byte("---------")
	for i := 0; i < 9; i++ {
		if m&(1<<uint(8-i)) != 0 {
			out[i] = set[i]
		}
	}
	return string(out)
}

const fmIcon = "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16' fill='none' stroke='%23eee' stroke-width='1.4' stroke-linejoin='round'><path d='M2 4 H6 L7.5 5.5 H14 V12 H2 Z'/></svg>"
