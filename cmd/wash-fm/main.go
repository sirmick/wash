// wash-fm — a single-pane file manager. The BE syscalls directly
// (architecture's "BEs run as the user, may syscall" path) rather
// than going through a router-side fs service. When other apps
// (text editor, image viewer) need fs access we'll add the dialog
// provider pattern; for v0.1 wash-fm owns its access.
//
// Path confinement (WASH_FM_ROOT): if set, every path argument is
// resolved (Clean+Abs) and required to be inside the root by
// lexical containment. Outside paths get an "outside_root" error.
// Set by the e2e harness to a per-test tmpdir; unset in production
// (full filesystem access, no sandbox). It's a safety net for
// tests, not a security boundary.
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
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

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

// fmRoot is the configured sandbox root, or "" if unconfined. Set
// once at startup from WASH_FM_ROOT.
var fmRoot string

// watchState owns the fswatch.Manager and the per-path subscription
// table. fm's FE asks the BE to watch a directory while it's
// expanded in the tree; the BE keeps one Sub per path and tears it
// down when the FE sends unwatch (or when the BE exits). The map
// makes the protocol idempotent — repeated watch requests for the
// same path keep one Sub, and an unwatch on an unknown path is a
// no-op rather than an error.
type watchState struct {
	mu      sync.Mutex
	mgr     *fswatch.Manager
	subs    map[string]*fswatch.Sub
	conn    *sdk.Conn
}

var fmWatch *watchState

type entry struct {
	Name    string `json:"name" cbor:"name"`
	Type    string `json:"type" cbor:"type"` // "dir" | "file" | "symlink" | "other"
	Size    int64  `json:"size" cbor:"size"`
	ModUnix int64  `json:"mod_unix" cbor:"mod_unix"`
	Perm    string `json:"perm" cbor:"perm"` // "rwxr-xr--" 9-char human form
	Mode    uint32 `json:"mode" cbor:"mode"` // raw octal-style bits
	UID     uint32 `json:"uid" cbor:"uid"`
	GID     uint32 `json:"gid" cbor:"gid"`
	Owner   string `json:"owner,omitempty" cbor:"owner,omitempty"`
	Group   string `json:"group,omitempty" cbor:"group,omitempty"`
	LinkTo  string `json:"link_to,omitempty" cbor:"link_to,omitempty"`
	LinkErr string `json:"link_err,omitempty" cbor:"link_err,omitempty"`
}

// All response structs carry an optional ID that echoes the
// request's id when present. CBOR omitempty on the request side
// keeps the wire small for FE list/read traffic where id is unused.
type listResult struct {
	Kind      string  `json:"kind"`
	ID        string  `json:"id,omitempty"`
	Path      string  `json:"path"`
	Entries   []entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

type readResult struct {
	Kind      string `json:"kind"`
	ID        string `json:"id,omitempty"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
	Size      int64  `json:"size"`
}

type pathOK struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Path string `json:"path"`
}

type renameOK struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	From string `json:"from"`
	To   string `json:"to"`
}

type writeOK struct {
	Kind  string `json:"kind"`
	ID    string `json:"id,omitempty"`
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

type symlinkOK struct {
	Kind     string `json:"kind"`
	ID       string `json:"id,omitempty"`
	Target   string `json:"target"`
	LinkPath string `json:"link_path"`
}

type errResult struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Path string `json:"path,omitempty"`
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

// fsEvent is the watch-fired message the BE pushes to the FE
// whenever a fswatch.Sub reports a change. There's no id field:
// these are unsolicited, not request/response.
type fsEvent struct {
	Kind string `json:"kind"`
	Op   string `json:"op"`   // "created" | "modified" | "deleted"
	Path string `json:"path"` // the file/dir that changed
}

// watchOK is the reply to a successful watch request. ID echoes the
// FE's request id so the FE can correlate; Path is the (cleaned)
// path the BE is now watching.
type watchOK struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Path string `json:"path"`
}

func main() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("wash-fm: assets: %v", err)
	}
	if root := os.Getenv("WASH_FM_ROOT"); root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			log.Fatalf("wash-fm: WASH_FM_ROOT=%q: %v", root, err)
		}
		fmRoot = filepath.Clean(abs)
		log.Printf("wash-fm: sandbox WASH_FM_ROOT=%s", fmRoot)
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
	mgr, err := fswatch.New()
	if err != nil {
		// Watching is best-effort — if fsnotify is unavailable
		// (resource limits, unusual platform) fm keeps working
		// without live refresh.
		log.Printf("wash-fm: fswatch unavailable: %v", err)
	} else {
		fmWatch = &watchState{mgr: mgr, subs: make(map[string]*fswatch.Sub), conn: c}
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
	m, ok := data.(map[any]any)
	if !ok {
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
		mode, _ := toUint32(m["mode"])
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
		paths := toPathSlice(m["paths"])
		doClipboardFilesSet(c, id, op, paths)
	case "clipboard_files_get":
		// On-demand fetch (FE asks at paste time without relying
		// on the cached state). Reuses the push path.
		go pushFilesClipboardToFE(c)
	}
}

// toPathSlice converts a CBOR []any of strings into []string.
// Skips non-string entries silently — defensive against malformed
// inputs without strict validation.
func toPathSlice(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	}
	return nil
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

// toUint32 normalizes whatever the FE sent us (number, string, or
// other) into a permission-bit mode. JSON over the wire ends up as
// float64 once decoded; CBOR can deliver a uint64 directly. We
// accept both plus a string (octal or decimal) so an FE that wants
// to send "0755" verbatim from the input field works too.
func toUint32(v any) (uint32, bool) {
	switch x := v.(type) {
	case uint64:
		return uint32(x), true
	case int64:
		return uint32(x), true
	case float64:
		return uint32(x), true
	case string:
		// Allow "0755", "0o755", or "755". Strip a leading "0o"
		// and let strconv pick base from the leading 0.
		s := strings.TrimPrefix(x, "0o")
		n, err := strconv.ParseUint(s, 0, 32)
		if err != nil {
			// Try as decimal.
			n, err = strconv.ParseUint(x, 10, 32)
			if err != nil {
				return 0, false
			}
		}
		return uint32(n), true
	}
	return 0, false
}

// lookupOwner / lookupGroup resolve numeric ids to names with a
// per-list-call cache. We use os/user.LookupId which reads
// /etc/passwd directly on glibc-less builds (matches the wash
// CGO_ENABLED=0 posture). A lookup miss caches the empty string
// so we don't retry the same failure thousands of times.
func lookupOwner(cache map[uint32]string, uid uint32) string {
	if name, ok := cache[uid]; ok {
		return name
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		cache[uid] = ""
		return ""
	}
	cache[uid] = u.Username
	return u.Username
}

func lookupGroup(cache map[uint32]string, gid uint32) string {
	if name, ok := cache[gid]; ok {
		return name
	}
	g, err := user.LookupGroupId(strconv.FormatUint(uint64(gid), 10))
	if err != nil {
		cache[gid] = ""
		return ""
	}
	cache[gid] = g.Name
	return g.Name
}

// doChmod sets the permission bits on path. Only the low 12 bits
// (suid/sgid/sticky + rwx*3) are kept — apps that want to mess
// with mode_t flags can shell out. Refuses outside-sandbox paths.
func doChmod(c *sdk.Conn, id, path string, mode uint32) {
	if path == "" {
		sendErr(c, "chmod_err", id, path, "bad_request", "missing path")
		return
	}
	abs, err := confine(path)
	if err != nil {
		sendErr(c, "chmod_err", id, path, confineErrCode(err), err.Error())
		return
	}
	if err := os.Chmod(abs, os.FileMode(mode&0o7777)); err != nil {
		sendErr(c, "chmod_err", id, abs, fsErrCode(err), err.Error())
		return
	}
	if err := c.SendAppMsg(pathOK{Kind: "chmod_ok", ID: id, Path: abs}); err != nil {
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
	abs, err := confine(path)
	if err != nil {
		sendErr(c, "chown_err", id, path, confineErrCode(err), err.Error())
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
		sendErr(c, "chown_err", id, abs, fsErrCode(err), err.Error())
		return
	}
	if err := c.SendAppMsg(pathOK{Kind: "chown_ok", ID: id, Path: abs}); err != nil {
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
	link, err := confine(linkPath)
	if err != nil {
		sendErr(c, "symlink_err", id, linkPath, confineErrCode(err), err.Error())
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
				sendErr(c, "symlink_err", id, link, fsErrCode(err), err.Error())
				return
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			sendErr(c, "symlink_err", id, link, fsErrCode(err), err.Error())
			return
		}
	}
	if err := os.Symlink(target, link); err != nil {
		code := fsErrCode(err)
		if errors.Is(err, os.ErrExist) {
			code = "exists"
		}
		sendErr(c, "symlink_err", id, link, code, err.Error())
		return
	}
	if err := c.SendAppMsg(symlinkOK{Kind: "symlink_ok", ID: id, Target: target, LinkPath: link}); err != nil {
		log.Printf("wash-fm send symlink_ok: %v", err)
	}
}

// doWatch subscribes to fs events under path and starts (lazily, on
// first watch) a goroutine that forwards Sub events as fs_event
// app_msgs. Sandbox is enforced via confine(); the FE can only ever
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
	abs, err := confine(path)
	if err != nil {
		sendErr(c, "watch_err", id, path, confineErrCode(err), err.Error())
		return
	}
	fmWatch.mu.Lock()
	if _, exists := fmWatch.subs[abs]; exists {
		// Idempotent: a second watch for the same path is a no-op.
		fmWatch.mu.Unlock()
		_ = c.SendAppMsg(watchOK{Kind: "watch_ok", ID: id, Path: abs})
		return
	}
	sub, err := fmWatch.mgr.Watch(abs)
	if err != nil {
		fmWatch.mu.Unlock()
		sendErr(c, "watch_err", id, abs, "io", err.Error())
		return
	}
	fmWatch.subs[abs] = sub
	fmWatch.mu.Unlock()

	// One goroutine per Sub. Exits when the Sub's events channel
	// closes — which happens on unwatch (Sub.Close) or on manager
	// shutdown (Manager.Close). Either way the goroutine is bound
	// to the Sub's lifetime, no manual coordination needed.
	go func(s *fswatch.Sub) {
		for ev := range s.Events() {
			payload := fsEvent{Kind: "fs_event", Op: ev.Op.String(), Path: ev.Path}
			if err := c.SendAppMsg(payload); err != nil {
				log.Printf("wash-fm send fs_event: %v", err)
				return
			}
		}
	}(sub)

	_ = c.SendAppMsg(watchOK{Kind: "watch_ok", ID: id, Path: abs})
}

// doUnwatch releases the Sub for path. Idempotent: an unwatch on a
// path that wasn't being watched returns watch_ok (it's already in
// the desired state) — same UX-shape as the FE asking the BE to
// "stop watching", and the BE confirming it's not.
func doUnwatch(c *sdk.Conn, id, path string) {
	if fmWatch == nil {
		_ = c.SendAppMsg(watchOK{Kind: "unwatch_ok", ID: id, Path: path})
		return
	}
	if path == "" {
		sendErr(c, "unwatch_err", id, path, "bad_request", "missing path")
		return
	}
	abs, err := confine(path)
	if err != nil {
		sendErr(c, "unwatch_err", id, path, confineErrCode(err), err.Error())
		return
	}
	fmWatch.mu.Lock()
	sub, ok := fmWatch.subs[abs]
	if ok {
		delete(fmWatch.subs, abs)
	}
	fmWatch.mu.Unlock()
	if sub != nil {
		sub.Close()
	}
	_ = c.SendAppMsg(watchOK{Kind: "unwatch_ok", ID: id, Path: abs})
}

// confine resolves p to an absolute, cleaned path and verifies it is
// inside fmRoot when the sandbox is active. The lexical check is
// sufficient for the threat model (test bug passes wrong path);
// symlink-escape is a known v1 limitation — we don't create
// symlinks and tests own the fixture tree.
func confine(p string) (string, error) {
	if p == "" {
		return "", errors.New("missing path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	cleaned := filepath.Clean(abs)
	if fmRoot == "" {
		return cleaned, nil
	}
	if cleaned != fmRoot && !strings.HasPrefix(cleaned, fmRoot+string(filepath.Separator)) {
		return "", errOutsideRoot
	}
	return cleaned, nil
}

var errOutsideRoot = errors.New("path is outside the configured WASH_FM_ROOT")

// sendList lists the directory at path and sends list_ok / list_err.
// Symlinks are reported but not followed; the FE can re-list the
// target on demand.
func sendList(c *sdk.Conn, id, path string) {
	if path == "" {
		sendErr(c, "list_err", id, path, "bad_request", "missing path")
		return
	}
	abs, err := confine(path)
	if err != nil {
		sendErr(c, "list_err", id, path, confineErrCode(err), err.Error())
		return
	}
	dir, err := os.Open(abs)
	if err != nil {
		sendErr(c, "list_err", id, abs, fsErrCode(err), err.Error())
		return
	}
	defer dir.Close()
	infos, err := dir.Readdir(-1)
	if err != nil {
		sendErr(c, "list_err", id, abs, "io", err.Error())
		return
	}
	truncated := false
	if len(infos) > maxListEntries {
		infos = infos[:maxListEntries]
		truncated = true
	}
	out := make([]entry, 0, len(infos))
	// Per-call cache so a directory of many files owned by the same
	// user/group only resolves each name once. Empty string in the
	// cache means "lookup failed, don't retry."
	owners := map[uint32]string{}
	groups := map[uint32]string{}
	for _, fi := range infos {
		e := entry{
			Name:    fi.Name(),
			Type:    typeOf(fi),
			Size:    fi.Size(),
			ModUnix: fi.ModTime().Unix(),
			Perm:    formatPerm(fi.Mode()),
			Mode:    uint32(fi.Mode().Perm()),
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			e.UID = st.Uid
			e.GID = st.Gid
			e.Owner = lookupOwner(owners, st.Uid)
			e.Group = lookupGroup(groups, st.Gid)
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
	res := listResult{Kind: "list_ok", ID: id, Path: abs, Entries: out, Truncated: truncated}
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
	abs, err := confine(path)
	if err != nil {
		sendErr(c, "read_err", id, path, confineErrCode(err), err.Error())
		return
	}
	st, err := os.Stat(abs)
	if err != nil {
		sendErr(c, "read_err", id, abs, fsErrCode(err), err.Error())
		return
	}
	if st.IsDir() {
		sendErr(c, "read_err", id, abs, "is_dir", "path is a directory")
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		sendErr(c, "read_err", id, abs, fsErrCode(err), err.Error())
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
	res := readResult{
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
	if from == "" || to == "" {
		sendErr(c, "rename_err", id, from, "bad_request", "missing from or to")
		return
	}
	src, err := confine(from)
	if err != nil {
		sendErr(c, "rename_err", id, from, confineErrCode(err), err.Error())
		return
	}
	dst, err := confine(to)
	if err != nil {
		sendErr(c, "rename_err", id, to, confineErrCode(err), err.Error())
		return
	}
	if src == dst {
		sendErr(c, "rename_err", id, src, "same_path", "from and to resolve to the same path")
		return
	}
	if _, err := os.Lstat(src); err != nil {
		sendErr(c, "rename_err", id, src, fsErrCode(err), err.Error())
		return
	}
	if dstInfo, err := os.Lstat(dst); err == nil {
		if !replace {
			sendErr(c, "rename_err", id, dst, "exists", "destination already exists")
			return
		}
		// Replace: remove dst first. We refuse populated dirs
		// (Remove fails on those with "directory not empty") so
		// fm-direct stays a fast-path; recursive replace lives in
		// bulk-ops.
		if err := os.Remove(dst); err != nil {
			if dstInfo.IsDir() && strings.Contains(err.Error(), "directory not empty") {
				sendErr(c, "rename_err", id, dst, "not_empty_dir",
					"target is a non-empty directory; delete it via the queue first")
				return
			}
			sendErr(c, "rename_err", id, dst, fsErrCode(err), err.Error())
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		sendErr(c, "rename_err", id, dst, fsErrCode(err), err.Error())
		return
	}
	if err := os.Rename(src, dst); err != nil {
		code := fsErrCode(err)
		if strings.Contains(err.Error(), "cross-device") {
			code = "cross_device"
		}
		sendErr(c, "rename_err", id, src, code, err.Error())
		return
	}
	if err := c.SendAppMsg(renameOK{Kind: "rename_ok", ID: id, From: src, To: dst}); err != nil {
		log.Printf("wash-fm send rename_ok: %v", err)
	}
}

// doDelete removes a single file or empty directory. Non-empty
// directories error with code=not_empty; recursive delete is a
// future op (intentional — easier to add power than take it back).
func doDelete(c *sdk.Conn, id, path string) {
	if path == "" {
		sendErr(c, "delete_err", id, path, "bad_request", "missing path")
		return
	}
	abs, err := confine(path)
	if err != nil {
		sendErr(c, "delete_err", id, path, confineErrCode(err), err.Error())
		return
	}
	// Refuse to delete the configured sandbox root itself. Outside
	// the sandbox this guard is inactive; a user with a destructive
	// click is on their own (matches `rm` and existing fm scope).
	if fmRoot != "" && abs == fmRoot {
		sendErr(c, "delete_err", id, abs, "forbidden", "cannot delete the sandbox root")
		return
	}
	if err := os.Remove(abs); err != nil {
		code := fsErrCode(err)
		if strings.Contains(err.Error(), "directory not empty") {
			code = "not_empty"
		}
		sendErr(c, "delete_err", id, abs, code, err.Error())
		return
	}
	if err := c.SendAppMsg(pathOK{Kind: "delete_ok", ID: id, Path: abs}); err != nil {
		log.Printf("wash-fm send delete_ok: %v", err)
	}
}

// doCreateFile creates an empty file. O_EXCL avoids silently
// truncating an existing path — overwrites must go through `write`.
func doCreateFile(c *sdk.Conn, id, path string) {
	if path == "" {
		sendErr(c, "create_file_err", id, path, "bad_request", "missing path")
		return
	}
	abs, err := confine(path)
	if err != nil {
		sendErr(c, "create_file_err", id, path, confineErrCode(err), err.Error())
		return
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		code := fsErrCode(err)
		if errors.Is(err, os.ErrExist) {
			code = "exists"
		}
		sendErr(c, "create_file_err", id, abs, code, err.Error())
		return
	}
	_ = f.Close()
	if err := c.SendAppMsg(pathOK{Kind: "create_file_ok", ID: id, Path: abs}); err != nil {
		log.Printf("wash-fm send create_file_ok: %v", err)
	}
}

// doCreateDir creates a single directory level. Use MkdirAll-equiv
// later if there's demand; v1 is one-level to match the principle of
// least power.
func doCreateDir(c *sdk.Conn, id, path string) {
	if path == "" {
		sendErr(c, "create_dir_err", id, path, "bad_request", "missing path")
		return
	}
	abs, err := confine(path)
	if err != nil {
		sendErr(c, "create_dir_err", id, path, confineErrCode(err), err.Error())
		return
	}
	if err := os.Mkdir(abs, 0o755); err != nil {
		code := fsErrCode(err)
		if errors.Is(err, os.ErrExist) {
			code = "exists"
		}
		sendErr(c, "create_dir_err", id, abs, code, err.Error())
		return
	}
	if err := c.SendAppMsg(pathOK{Kind: "create_dir_ok", ID: id, Path: abs}); err != nil {
		log.Printf("wash-fm send create_dir_ok: %v", err)
	}
}

// doWrite writes content to path atomically: write to a sibling
// temp file, fsync, rename into place. Overwrites an existing
// target by design — this IS the save path. No FE call site yet;
// exposed for BE-driven tests and future editor integration.
func doWrite(c *sdk.Conn, id, path, content string) {
	if path == "" {
		sendErr(c, "write_err", id, path, "bad_request", "missing path")
		return
	}
	if len(content) > maxWriteBytes {
		sendErr(c, "write_err", id, path, "too_large", "content exceeds maxWriteBytes")
		return
	}
	abs, err := confine(path)
	if err != nil {
		sendErr(c, "write_err", id, path, confineErrCode(err), err.Error())
		return
	}
	dir := filepath.Dir(abs)
	suffixBytes := make([]byte, 6)
	if _, err := rand.Read(suffixBytes); err != nil {
		sendErr(c, "write_err", id, abs, "io", err.Error())
		return
	}
	tmp := filepath.Join(dir, ".wash-fm.tmp."+hex.EncodeToString(suffixBytes))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		sendErr(c, "write_err", id, abs, fsErrCode(err), err.Error())
		return
	}
	n, werr := f.Write([]byte(content))
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		sendErr(c, "write_err", id, abs, "io", werr.Error())
		return
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		sendErr(c, "write_err", id, abs, fsErrCode(err), err.Error())
		return
	}
	if err := c.SendAppMsg(writeOK{Kind: "write_ok", ID: id, Path: abs, Bytes: n}); err != nil {
		log.Printf("wash-fm send write_ok: %v", err)
	}
}

func sendErr(c *sdk.Conn, kind, id, path, code, msg string) {
	if err := c.SendAppMsg(errResult{Kind: kind, ID: id, Path: path, Code: code, Msg: msg}); err != nil {
		log.Printf("wash-fm send %s: %v", kind, err)
	}
}

// maxCompletions caps the autocomplete suggestion count.
const maxCompletions = 50

// sendCompletions returns paths matching `partial`. Rules:
//   - empty / "/"          → entries in /
//   - trailing "/"         → entries in the directory
//   - otherwise            → entries in dirname(partial) starting with basename(partial)
//
// Directory matches get a trailing "/" so subsequent typing extends
// naturally. With the sandbox active, the searched directory must
// be inside the root; outside-root partials silently return no
// matches (autocomplete is a UX path, not a place for errors).
func sendCompletions(c *sdk.Conn, id, partial string) {
	var dir, prefix string
	switch {
	case partial == "":
		dir, prefix = "/", ""
	case strings.HasSuffix(partial, "/"):
		dir, prefix = partial, ""
	default:
		dir = filepath.Dir(partial)
		if dir == "" {
			dir = "/"
		}
		prefix = filepath.Base(partial)
	}
	empty := map[string]any{"kind": "complete_ok", "id": id, "partial": partial, "matches": []string{}}
	abs, err := confine(dir)
	if err != nil {
		_ = c.SendAppMsg(empty)
		return
	}
	d, err := os.Open(abs)
	if err != nil {
		_ = c.SendAppMsg(empty)
		return
	}
	defer d.Close()
	infos, err := d.Readdir(-1)
	if err != nil {
		_ = c.SendAppMsg(empty)
		return
	}
	matches := make([]string, 0, 16)
	for _, fi := range infos {
		name := fi.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		full := filepath.Join(abs, name)
		if fi.IsDir() {
			full += "/"
		}
		matches = append(matches, full)
		if len(matches) >= maxCompletions {
			break
		}
	}
	sort.Strings(matches)
	if err := c.SendAppMsg(map[string]any{
		"kind":    "complete_ok",
		"id":      id,
		"partial": partial,
		"matches": matches,
	}); err != nil {
		log.Printf("wash-fm send complete_ok: %v", err)
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
	case errors.Is(err, os.ErrExist):
		return "exists"
	}
	return "io"
}

// confineErrCode promotes the sentinel to a stable code; falls
// through to fsErrCode for any other path-resolution failure.
func confineErrCode(err error) string {
	if errors.Is(err, errOutsideRoot) {
		return "outside_root"
	}
	return fsErrCode(err)
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

// fmIcon — Lucide sprite symbol name. The shell renders this via
// <svg><use href="/icons.svg#folder"/></svg>; the sprite is built
// from lucide-static at shell build time. See web/shell/build-icons.mjs.
const fmIcon = "folder"
