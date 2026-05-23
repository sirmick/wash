// wash-fm — a single-pane file manager. Mutations + reads happen
// in-process (architecture's "BEs run as the user, may syscall"
// path); read primitives are factored into internal/fs so wash-fs
// — the singleton service for dialog consumers — shares them.
//
// Path confinement: the sandbox root is shipped by the router in
// the IdentityAck Session bag (Session.Root) and applied by every
// path-taking operation via internal/fs.Confine. Outside paths get
// an "outside_root" error. Empty root means unconfined. Set in dev
// by `wash-router --fs-root <path>`; unset in production (full
// filesystem access, no sandbox).
//
// Request/response correlation: every request may include an "id"
// string field; responses echo it. Tests use it to await matched
// replies via the control socket's `msg` op (see internal/router/
// control.go). The existing FE list/read/complete flows still work
// without an id — it's optional, additive, and a precedent for
// future ops that need it.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/internal/fswatch"
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

	// Cap on write size. The atomic-write path holds the whole
	// payload in memory; this keeps mistakes bounded.
	maxWriteBytes = 8 * 1024 * 1024
)

// fmRoot is the configured sandbox root, or "" if unconfined.
// Populated in onReady from the SDK Session bag the router ships
// at handshake time — fm never reads WASH_FM_ROOT itself.
var fmRoot string

// fmFS is the read-side filesystem accessor. Wraps internal/fs with
// the configured root. Created once in onReady so the rest of the
// BE doesn't need to thread fmRoot through every call.
var fmFS *wfs.FS

// fmWatch is the refcounted watcher backing fm's watch / unwatch
// app_msg protocol. Nil when fswatch is unavailable on this host
// (resource limits, unusual platform) — watching becomes a no-op
// in that case so fm keeps working without live refresh.
var fmWatch *fswatch.RefMap


// fsEvent is the watch-fired message the BE pushes to the FE
// whenever a fswatch.Sub reports a change. There's no id field:
// these are unsolicited, not request/response. Local to fm because
// the "watch" surface is fm-specific (the SDK's filepicker uses a
// different fs.watch_event kind).
type fsEvent struct {
	Kind string `json:"kind"`
	Op   string `json:"op"`   // "created" | "modified" | "deleted"
	Path string `json:"path"` // the file/dir that changed
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
		Assets:             sub,
		OnReady:            onReady,
		OnAppMsg:           onAppMsg,
		OnClipboardChanged: onClipboardChanged,
	})
}

// initialPath is the directory fm shows on first paint. With a
// sandbox root configured we list the root; otherwise $HOME (or "/"
// as a last-ditch fallback).
func initialPath() string {
	if fmRoot != "" {
		return fmRoot
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "/"
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-fm ready instance=%s window=%d", instanceID, windowID)
	if root := c.Session().Root; root != "" {
		fmRoot = root
		log.Printf("wash-fm: sandbox root=%s (from router session)", fmRoot)
	}
	fmFS = wfs.New(fmRoot)
	mgr, err := fswatch.New()
	if err != nil {
		// Watching is best-effort — if fsnotify is unavailable
		// (resource limits, unusual platform) fm keeps working
		// without live refresh.
		log.Printf("wash-fm: fswatch unavailable: %v", err)
	} else {
		fmWatch = fswatch.NewRefMap(mgr)
	}
	sendList(c, "", initialPath())
	// Seed the FE's file-clipboard view from whatever's already on
	// the router clipboard (covers the "another fm window did a
	// copy before this one opened" case). OnClipboardChanged
	// handles updates after startup.
	go pushFilesClipboardToFE(c)
}

// fileClipboardMime is the mime fm uses to round-trip a multi-path
// cut/copy state through the router clipboard service. Cross-fm-
// window sync comes free because every fm instance subscribes to
// clipboard.changed.
const fileClipboardMime = "application/x-wash-paths"

// filesClipboardPayload is the JSON shape we store at fileClipboardMime.
type filesClipboardPayload struct {
	Op    string   `json:"op"` // "copy" | "cut"
	Paths []string `json:"paths"`
}

// onClipboardChanged fires when ANOTHER app updates the clipboard.
// If the mime is ours, fetch + push to FE. If it's anything else
// (a text clipboard from another tab), still push a "cleared"
// state so the FE drops any stale cut/copy badge.
func onClipboardChanged(c *sdk.Conn, mime string) {
	if mime == fileClipboardMime {
		go pushFilesClipboardToFE(c)
		return
	}
	// A non-files clipboard set means our previous cut/copy is
	// no longer the active clipboard content. Tell the FE.
	_ = c.SendAppMsg(map[string]any{"kind": "clipboard_files_state", "op": "", "paths": []string{}})
}

// pushFilesClipboardToFE fetches the current router clipboard and,
// if it's our mime, pushes the parsed payload to the FE. Called on
// startup + on every clipboard.changed event matching our mime.
func pushFilesClipboardToFE(c *sdk.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	mime, data, err := c.ClipboardGet(ctx)
	if err != nil {
		return
	}
	if mime != fileClipboardMime {
		return
	}
	var p filesClipboardPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return
	}
	_ = c.SendAppMsg(map[string]any{
		"kind":  "clipboard_files_state",
		"op":    p.Op,
		"paths": p.Paths,
	})
}

func onAppMsg(c *sdk.Conn, _ uint32, data any) {
	m := sdk.AsMap(data)
	if m == nil {
		return
	}
	kind, _ := m["kind"].(string)
	id, _ := m["id"].(string)
	switch kind {
	case "request_initial":
		sendList(c, id, initialPath())
	case "save_state":
		state, ok := m["state"]
		if !ok {
			return
		}
		if err := c.SaveState(state); err != nil {
			log.Printf("wash-fm save_state: %v", err)
		}
	case "list":
		path, _ := m["path"].(string)
		sendList(c, id, path)
	case "read":
		path, _ := m["path"].(string)
		sendRead(c, id, path)
	case "complete":
		partial, _ := m["partial"].(string)
		sendCompletions(c, id, partial)
	case "clipboard_copy_path":
		path, _ := m["path"].(string)
		if path == "" {
			return
		}
		if err := c.ClipboardSet("text/plain", []byte(path)); err != nil {
			log.Printf("wash-fm clipboard set: %v", err)
		}
	case "rename":
		from, _ := m["from"].(string)
		to, _ := m["to"].(string)
		replace, _ := m["replace"].(bool)
		doRename(c, id, from, to, replace)
	case "delete":
		path, _ := m["path"].(string)
		doDelete(c, id, path)
	case "create_file":
		path, _ := m["path"].(string)
		doCreateFile(c, id, path)
	case "create_dir":
		path, _ := m["path"].(string)
		doCreateDir(c, id, path)
	case "write":
		path, _ := m["path"].(string)
		content, _ := m["content"].(string)
		doWrite(c, id, path, content)
	case "watch":
		path, _ := m["path"].(string)
		doWatch(c, id, path)
	case "unwatch":
		path, _ := m["path"].(string)
		doUnwatch(c, id, path)
	case "chmod":
		path, _ := m["path"].(string)
		mode, _ := sdk.ToUint32(m["mode"])
		doChmod(c, id, path, mode)
	case "chown":
		path, _ := m["path"].(string)
		owner, _ := m["owner"].(string)
		group, _ := m["group"].(string)
		doChown(c, id, path, owner, group)
	case "symlink":
		target, _ := m["target"].(string)
		linkPath, _ := m["link_path"].(string)
		replace, _ := m["replace"].(bool)
		doSymlink(c, id, target, linkPath, replace)
	case "clipboard_files_set":
		op, _ := m["op"].(string)
		paths := sdk.ToStringSlice(m["paths"])
		doClipboardFilesSet(c, id, op, paths)
	case "clipboard_files_get":
		// On-demand fetch (FE asks at paste time without relying
		// on the cached state). Reuses the push path.
		go pushFilesClipboardToFE(c)
	}
}

// doClipboardFilesSet stores op + paths on the router clipboard
// under our private mime. Every other fm window subscribed to
// clipboard.changed gets pushed a clipboard_files_state event.
// We don't confine paths here because the clipboard is a
// transport, not a write — confinement happens when bulk-ops
// actually moves/copies the files.
func doClipboardFilesSet(c *sdk.Conn, id, op string, paths []string) {
	if op != "copy" && op != "cut" {
		sendErr(c, "clipboard_files_err", id, "", "bad_request", "op must be copy or cut")
		return
	}
	if len(paths) == 0 {
		// Treat empty as "clear" — write a non-files clipboard so
		// fm windows drop their state. Use an empty payload at our
		// own mime; consumers parse and see Paths=[].
	}
	data, err := json.Marshal(filesClipboardPayload{Op: op, Paths: paths})
	if err != nil {
		sendErr(c, "clipboard_files_err", id, "", "internal", err.Error())
		return
	}
	if err := c.ClipboardSet(fileClipboardMime, data); err != nil {
		sendErr(c, "clipboard_files_err", id, "", "io", err.Error())
		return
	}
	// Echo state to THIS fm's FE immediately — the setter doesn't
	// receive its own OnClipboardChanged (router suppresses).
	_ = c.SendAppMsg(map[string]any{
		"kind":  "clipboard_files_state",
		"op":    op,
		"paths": paths,
	})
	_ = c.SendAppMsg(map[string]any{"kind": "clipboard_files_set_ok", "id": id, "op": op, "paths": paths})
}

// doChmod sets the permission bits on path. Only the low 12 bits
// (suid/sgid/sticky + rwx*3) are kept — apps that want to mess
// with mode_t flags can shell out. Refuses outside-sandbox paths.
func doChmod(c *sdk.Conn, id, path string, mode uint32) {
	if path == "" {
		sendErr(c, "chmod_err", id, path, "bad_request", "missing path")
		return
	}
	abs, err := fmFS.Confine(path)
	if err != nil {
		sendErr(c, "chmod_err", id, path, wfs.ErrCode(err), err.Error())
		return
	}
	if err := os.Chmod(abs, os.FileMode(mode&0o7777)); err != nil {
		sendErr(c, "chmod_err", id, abs, wfs.ErrCode(err), err.Error())
		return
	}
	if err := c.SendAppMsg(wfs.PathReply{Kind: "chmod_ok", ID: id, Path: abs}); err != nil {
		log.Printf("wash-fm send chmod_ok: %v", err)
	}
}

// doChown changes owner/group. Either may be empty (then unchanged
// — represented as uid/gid = -1 to os.Chown). Names or numeric ids
// are accepted; we resolve via os/user.Lookup.
//
// Permission notes: a non-root user can typically only change the
// group to one they belong to, and cannot change the owner at all.
// We surface the OS error rather than guess.
func doChown(c *sdk.Conn, id, path, owner, group string) {
	if path == "" {
		sendErr(c, "chown_err", id, path, "bad_request", "missing path")
		return
	}
	if owner == "" && group == "" {
		sendErr(c, "chown_err", id, path, "bad_request", "missing owner and group")
		return
	}
	abs, err := fmFS.Confine(path)
	if err != nil {
		sendErr(c, "chown_err", id, path, wfs.ErrCode(err), err.Error())
		return
	}
	uid := -1
	if owner != "" {
		n, lerr := resolveUID(owner)
		if lerr != nil {
			sendErr(c, "chown_err", id, abs, "bad_user", lerr.Error())
			return
		}
		uid = n
	}
	gid := -1
	if group != "" {
		n, lerr := resolveGID(group)
		if lerr != nil {
			sendErr(c, "chown_err", id, abs, "bad_group", lerr.Error())
			return
		}
		gid = n
	}
	if err := os.Chown(abs, uid, gid); err != nil {
		sendErr(c, "chown_err", id, abs, wfs.ErrCode(err), err.Error())
		return
	}
	if err := c.SendAppMsg(wfs.PathReply{Kind: "chown_ok", ID: id, Path: abs}); err != nil {
		log.Printf("wash-fm send chown_ok: %v", err)
	}
}

// resolveUID accepts a username or numeric uid and returns the int.
func resolveUID(spec string) (int, error) {
	if n, err := strconv.Atoi(spec); err == nil {
		return n, nil
	}
	u, err := user.Lookup(spec)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Uid)
}

// resolveGID accepts a group name or numeric gid and returns the int.
func resolveGID(spec string) (int, error) {
	if n, err := strconv.Atoi(spec); err == nil {
		return n, nil
	}
	g, err := user.LookupGroup(spec)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(g.Gid)
}

// doSymlink creates a symlink at link_path pointing at target. Only
// link_path is sandboxed — target is stored verbatim (symlinks
// can legitimately point outside the browsable area, and the
// target string is not dereferenced at creation time). With
// `replace`, an existing simple dst (file / symlink / empty dir)
// is removed first; populated dirs are refused with not_empty_dir.
func doSymlink(c *sdk.Conn, id, target, linkPath string, replace bool) {
	if target == "" || linkPath == "" {
		sendErr(c, "symlink_err", id, linkPath, "bad_request", "missing target or link_path")
		return
	}
	link, err := fmFS.Confine(linkPath)
	if err != nil {
		sendErr(c, "symlink_err", id, linkPath, wfs.ErrCode(err), err.Error())
		return
	}
	if replace {
		if dstInfo, err := os.Lstat(link); err == nil {
			if err := os.Remove(link); err != nil {
				if dstInfo.IsDir() && strings.Contains(err.Error(), "directory not empty") {
					sendErr(c, "symlink_err", id, link, "not_empty_dir",
						"target is a non-empty directory; delete it via the queue first")
					return
				}
				sendErr(c, "symlink_err", id, link, wfs.ErrCode(err), err.Error())
				return
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			sendErr(c, "symlink_err", id, link, wfs.ErrCode(err), err.Error())
			return
		}
	}
	if err := os.Symlink(target, link); err != nil {
		code := wfs.ErrCode(err)
		if errors.Is(err, os.ErrExist) {
			code = "exists"
		}
		sendErr(c, "symlink_err", id, link, code, err.Error())
		return
	}
	if err := c.SendAppMsg(wfs.SymlinkReply{Kind: "symlink_ok", ID: id, Target: target, LinkPath: link}); err != nil {
		log.Printf("wash-fm send symlink_ok: %v", err)
	}
}

// doWatch subscribes to fs events under path and starts (lazily, on
// first watch) a goroutine that forwards Sub events as fs_event
// app_msgs. Sandbox is enforced via fmFS.Confine(); the FE can only ever
// watch dirs it's allowed to see anyway.
func doWatch(c *sdk.Conn, id, path string) {
	if fmWatch == nil {
		sendErr(c, "watch_err", id, path, "unavailable", "fswatch unavailable")
		return
	}
	if path == "" {
		sendErr(c, "watch_err", id, path, "bad_request", "missing path")
		return
	}
	abs, err := fmFS.Confine(path)
	if err != nil {
		sendErr(c, "watch_err", id, path, wfs.ErrCode(err), err.Error())
		return
	}
	sub, first, err := fmWatch.Watch(abs)
	if err != nil {
		sendErr(c, "watch_err", id, abs, "io", err.Error())
		return
	}
	if first {
		// One forwarder goroutine per Sub. The RefMap keeps the Sub
		// alive across multiple subscribers; the goroutine exits
		// when the last Unwatch closes the events channel.
		go func(s *fswatch.Sub) {
			for ev := range s.Events() {
				payload := fsEvent{Kind: "fs_event", Op: ev.Op.String(), Path: ev.Path}
				if err := c.SendAppMsg(payload); err != nil {
					log.Printf("wash-fm send fs_event: %v", err)
					return
				}
			}
		}(sub)
	}
	_ = c.SendAppMsg(wfs.PathReply{Kind: "watch_ok", ID: id, Path: abs})
}

// doUnwatch decrements the refcount for path. The underlying Sub
// closes when nothing references it. Idempotent: an unwatch on a
// path that wasn't being watched returns unwatch_ok.
func doUnwatch(c *sdk.Conn, id, path string) {
	if fmWatch == nil {
		_ = c.SendAppMsg(wfs.PathReply{Kind: "unwatch_ok", ID: id, Path: path})
		return
	}
	if path == "" {
		sendErr(c, "unwatch_err", id, path, "bad_request", "missing path")
		return
	}
	abs, err := fmFS.Confine(path)
	if err != nil {
		sendErr(c, "unwatch_err", id, path, wfs.ErrCode(err), err.Error())
		return
	}
	fmWatch.Unwatch(abs)
	_ = c.SendAppMsg(wfs.PathReply{Kind: "unwatch_ok", ID: id, Path: abs})
}

// sendList lists the directory at path and sends list_ok / list_err.
// Symlinks are reported but not followed; the FE can re-list the
// target on demand.
func sendList(c *sdk.Conn, id, path string) {
	if path == "" {
		sendErr(c, "list_err", id, path, "bad_request", "missing path")
		return
	}
	entries, abs, truncated, err := fmFS.List(path, maxListEntries)
	if err != nil {
		sendErr(c, "list_err", id, path, wfs.ErrCode(err), err.Error())
		return
	}
	res := wfs.ListReply{Kind: "list_ok", ID: id, Path: abs, Entries: entries, Truncated: truncated}
	if err := c.SendAppMsg(res); err != nil {
		log.Printf("wash-fm send list_ok: %v", err)
	}
}

// sendRead reads up to maxReadBytes of path. If the bytes look
// binary, content is empty and Binary=true.
func sendRead(c *sdk.Conn, id, path string) {
	if path == "" {
		sendErr(c, "read_err", id, path, "bad_request", "missing path")
		return
	}
	abs, err := fmFS.Confine(path)
	if err != nil {
		sendErr(c, "read_err", id, path, wfs.ErrCode(err), err.Error())
		return
	}
	st, err := os.Stat(abs)
	if err != nil {
		sendErr(c, "read_err", id, abs, wfs.ErrCode(err), err.Error())
		return
	}
	if st.IsDir() {
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
	if err != nil && err.Error() != "EOF" {
		// io.EOF on empty file is fine; other read errors propagate.
		if n == 0 {
			sendErr(c, "read_err", id, abs, "io", err.Error())
			return
		}
	}
	chunk := buf[:n]
	binary := looksBinary(chunk)
	res := wfs.ReadReply{
		Kind:      "read_ok",
		ID:        id,
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

// doRename moves from→to. Without `replace`, refuses to overwrite
// an existing target (silent data loss is bad). With `replace`,
// removes a simple dst (file / symlink / empty dir) first via
// os.Remove and then renames; refuses with not_empty_dir if dst is
// a populated directory (those go through bulk-ops). Always
// refuses from == to (would otherwise delete the source under
// `replace` — see [[wash-fm-dnd-plan]] guardrails).
func doRename(c *sdk.Conn, id, from, to string, replace bool) {
	src, dst, err := fmFS.Rename(from, to, replace)
	if err != nil {
		path := src
		if path == "" {
			path = from
		}
		sendErr(c, "rename_err", id, path, wfs.ErrCode(err), err.Error())
		return
	}
	if err := c.SendAppMsg(wfs.RenameReply{Kind: "rename_ok", ID: id, From: src, To: dst}); err != nil {
		log.Printf("wash-fm send rename_ok: %v", err)
	}
}

// doDelete removes a single file or empty directory. Non-empty
// directories error with code=not_empty; recursive delete is a
// future op (intentional — easier to add power than take it back).
func doDelete(c *sdk.Conn, id, path string) {
	abs, err := fmFS.Delete(path)
	if err != nil {
		p := abs
		if p == "" {
			p = path
		}
		sendErr(c, "delete_err", id, p, wfs.ErrCode(err), err.Error())
		return
	}
	if err := c.SendAppMsg(wfs.PathReply{Kind: "delete_ok", ID: id, Path: abs}); err != nil {
		log.Printf("wash-fm send delete_ok: %v", err)
	}
}

// doCreateFile creates an empty file. fmFS.CreateFile uses O_EXCL
// so it refuses to clobber an existing path — overwrites must go
// through `write`.
func doCreateFile(c *sdk.Conn, id, path string) {
	abs, err := fmFS.CreateFile(path)
	if err != nil {
		p := abs
		if p == "" {
			p = path
		}
		sendErr(c, "create_file_err", id, p, wfs.ErrCode(err), err.Error())
		return
	}
	if err := c.SendAppMsg(wfs.PathReply{Kind: "create_file_ok", ID: id, Path: abs}); err != nil {
		log.Printf("wash-fm send create_file_ok: %v", err)
	}
}

// doCreateDir creates a single directory level. Use MkdirAll-equiv
// later if there's demand; v1 is one-level to match the principle of
// least power.
func doCreateDir(c *sdk.Conn, id, path string) {
	abs, err := fmFS.CreateDir(path)
	if err != nil {
		p := abs
		if p == "" {
			p = path
		}
		sendErr(c, "create_dir_err", id, p, wfs.ErrCode(err), err.Error())
		return
	}
	if err := c.SendAppMsg(wfs.PathReply{Kind: "create_dir_ok", ID: id, Path: abs}); err != nil {
		log.Printf("wash-fm send create_dir_ok: %v", err)
	}
}

// doWrite writes content to path atomically (tmp file + fsync +
// rename). Overwrites an existing target by design — this is the
// save path. The editor uses the same internal/fs.Write from its
// own BE.
func doWrite(c *sdk.Conn, id, path, content string) {
	abs, n, err := fmFS.Write(path, []byte(content), maxWriteBytes)
	if err != nil {
		p := abs
		if p == "" {
			p = path
		}
		sendErr(c, "write_err", id, p, wfs.ErrCode(err), err.Error())
		return
	}
	if err := c.SendAppMsg(wfs.WriteReply{Kind: "write_ok", ID: id, Path: abs, Bytes: n}); err != nil {
		log.Printf("wash-fm send write_ok: %v", err)
	}
}

func sendErr(c *sdk.Conn, kind, id, path, code, msg string) {
	if err := c.SendAppMsg(wfs.ErrReply{Kind: kind, ID: id, Path: path, Code: code, Msg: msg}); err != nil {
		log.Printf("wash-fm send %s: %v", kind, err)
	}
}

// sendCompletions returns paths matching `partial` via the shared
// internal/fs Complete helper. Out-of-sandbox or unreadable dirs
// silently surface as zero matches — autocomplete is a UX path, not
// an error surface.
func sendCompletions(c *sdk.Conn, id, partial string) {
	matches := fmFS.Complete(partial, 0)
	if matches == nil {
		matches = []string{}
	}
	if err := c.SendAppMsg(map[string]any{
		"kind":    "complete_ok",
		"id":      id,
		"partial": partial,
		"matches": matches,
	}); err != nil {
		log.Printf("wash-fm send complete_ok: %v", err)
	}
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

// fmIcon — Lucide sprite symbol name. The shell renders this via
// <svg><use href="/icons.svg#folder"/></svg>; the sprite is built
// from lucide-static at shell build time. See web/shell/build-icons.mjs.
const fmIcon = "folder"
