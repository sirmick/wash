// wash-fm — a single-pane file manager. Mutations + reads happen
// in-process (architecture's "BEs run as the user, may syscall"
// path); read primitives are factored into internal/fs.
//
// Path confinement: the sandbox root is shipped by the router in
// the IdentityAck Session bag (Session.Root) and applied by every
// path-taking operation via internal/fs.Confine. Outside paths get
// an "outside_root" error. Empty root means unconfined.
//
// Wire shape: typed handlers via sdk.Bus. Each kind has a Req struct
// and (for *_ok replies) a Resp payload. The bus owns the envelope
// (kind + id echo + _ok/_err suffix). Handlers return either the
// payload or an sdk.Err carrying the code/msg.
package fm

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

	"github.com/sirmick/wash/internal/apps/registry"
	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/internal/fswatch"
	"github.com/sirmick/wash/internal/sdk"
)

//go:embed all:assets
var assetsFS embed.FS

const (
	version = "0.8.0"

	// Cap on preview/read size to keep memory bounded.
	maxReadBytes = 256 * 1024
	// Cap on directory listing size — huge dirs (e.g. /usr/bin) get
	// truncated with a flag so the FE doesn't lock up rendering.
	maxListEntries = 5_000
	// Cap on write size. Atomic-write holds the whole payload in
	// memory; bounds protect against mistakes.
	maxWriteBytes = 8 * 1024 * 1024
)

var (
	fmRoot  string
	fmFS    *wfs.FS
	fmWatch *fswatch.RefMap
	bus     *sdk.Bus
)

// fileClipboardMime is the mime fm uses to round-trip a multi-path
// cut/copy state through the router clipboard service. Cross-fm-
// window sync comes free because every fm instance subscribes to
// clipboard.changed.
const fileClipboardMime = "application/x-wash-paths"

type filesClipboardPayload struct {
	Op    string   `json:"op"` // "copy" | "cut"
	Paths []string `json:"paths"`
}

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-fm: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.fm",
			Name:            "Files",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-fm",
			Surface:         sdk.SurfaceWindow,
			Icon:            fmIcon,
			Accent:          "#6090e0",
			Instancing:      sdk.InstancingMulti,
			Window:          &sdk.WindowHints{DefaultWidth: 760, DefaultHeight: 520},
		},
		Assets:             sub,
		OnReady:            onReady,
		OnClipboardChanged: onClipboardChanged,
	}
	registry.Register(&registry.App{
		Name:     "wash-fm",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-fm ready instance=%s window=%d", instanceID, windowID)
	if root := c.Session().Root; root != "" {
		fmRoot = root
		log.Printf("wash-fm: sandbox root=%s (from router session)", fmRoot)
	}
	fmFS = wfs.New(fmRoot)
	if mgr, err := fswatch.New(); err != nil {
		log.Printf("wash-fm: fswatch unavailable: %v", err)
	} else {
		fmWatch = fswatch.NewRefMap(mgr)
	}

	bus = sdk.NewBus(c)
	registerHandlers(bus)

	// Initial paint: ship a list_ok for the root path the FE will land on.
	// Use the bus.Emit path with the same shape the list handler produces;
	// the FE treats unsolicited list_ok the same as a response.
	if reply, err := listReplyFor("", initialPath()); err == nil {
		_ = bus.Emit("list_ok", reply)
	}

	// Seed the FE's file-clipboard view from whatever's already on
	// the router clipboard.
	go pushFilesClipboardToFE(c)
}

// initialPath is the directory fm shows on first paint.
func initialPath() string {
	if fmRoot != "" {
		return fmRoot
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "/"
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

type chmodReq struct {
	Path string `json:"path"`
	Mode any    `json:"mode"` // FE may send number or "0755" string
}

type chownReq struct {
	Path  string `json:"path"`
	Owner string `json:"owner"`
	Group string `json:"group"`
}

type symlinkReq struct {
	Target   string `json:"target"`
	LinkPath string `json:"link_path"`
	Replace  bool   `json:"replace"`
}

type completeReq struct {
	Partial string `json:"partial"`
}

type completeResp struct {
	Partial string   `json:"partial"`
	Matches []string `json:"matches"`
}

type clipboardCopyPathReq struct {
	Path string `json:"path"`
}

type clipboardFilesSetReq struct {
	Op    string   `json:"op"`
	Paths []string `json:"paths"`
}

type clipboardFilesSetResp struct {
	Op    string   `json:"op"`
	Paths []string `json:"paths"`
}

type saveStateReq struct {
	State any `json:"state"`
}

// fsEvent is the watch-fired message the BE pushes to the FE
// whenever a fswatch.Sub reports a change. Unsolicited (Emit), not
// a reply, so no id.
type fsEvent struct {
	Op   string `json:"op"`
	Path string `json:"path"`
}

// ----- handler registration -----

func registerHandlers(b *sdk.Bus) {
	c := b.Conn()

	sdk.Handle(b, "list", func(_ *sdk.Conn, _ string, req listReq) (wfs.ListReply, error) {
		return listReplyFor("", req.Path)
	})
	sdk.Handle(b, "read", func(_ *sdk.Conn, _ string, req readReq) (wfs.ReadReply, error) {
		return readFile(req.Path)
	})
	sdk.Handle(b, "write", func(_ *sdk.Conn, _ string, req writeReq) (wfs.WriteReply, error) {
		abs, n, err := fmFS.Write(req.Path, []byte(req.Content), maxWriteBytes)
		if err != nil {
			return wfs.WriteReply{}, fsErr(err, fallbackPath(abs, req.Path))
		}
		return wfs.WriteReply{Path: abs, Bytes: n}, nil
	})
	sdk.Handle(b, "rename", func(_ *sdk.Conn, _ string, req renameReq) (wfs.RenameReply, error) {
		src, dst, err := fmFS.Rename(req.From, req.To, req.Replace)
		if err != nil {
			return wfs.RenameReply{}, fsErr(err, fallbackPath(src, req.From))
		}
		return wfs.RenameReply{From: src, To: dst}, nil
	})
	sdk.Handle(b, "delete", func(_ *sdk.Conn, _ string, req pathReq) (wfs.PathReply, error) {
		abs, err := fmFS.Delete(req.Path)
		if err != nil {
			return wfs.PathReply{}, fsErr(err, fallbackPath(abs, req.Path))
		}
		return wfs.PathReply{Path: abs}, nil
	})
	sdk.Handle(b, "create_file", func(_ *sdk.Conn, _ string, req pathReq) (wfs.PathReply, error) {
		abs, err := fmFS.CreateFile(req.Path)
		if err != nil {
			return wfs.PathReply{}, fsErr(err, fallbackPath(abs, req.Path))
		}
		return wfs.PathReply{Path: abs}, nil
	})
	sdk.Handle(b, "create_dir", func(_ *sdk.Conn, _ string, req pathReq) (wfs.PathReply, error) {
		abs, err := fmFS.CreateDir(req.Path)
		if err != nil {
			return wfs.PathReply{}, fsErr(err, fallbackPath(abs, req.Path))
		}
		return wfs.PathReply{Path: abs}, nil
	})
	sdk.Handle(b, "chmod", func(_ *sdk.Conn, _ string, req chmodReq) (wfs.PathReply, error) {
		if req.Path == "" {
			return wfs.PathReply{}, sdk.Errf(sdk.ErrBadRequest, "missing path")
		}
		mode, ok := sdk.ToUint32(req.Mode)
		if !ok {
			return wfs.PathReply{}, sdk.Errf(sdk.ErrBadRequest, "invalid mode")
		}
		abs, err := fmFS.Confine(req.Path)
		if err != nil {
			return wfs.PathReply{}, fsErr(err, req.Path)
		}
		if err := os.Chmod(abs, os.FileMode(mode&0o7777)); err != nil {
			return wfs.PathReply{}, fsErr(err, abs)
		}
		return wfs.PathReply{Path: abs}, nil
	})
	sdk.Handle(b, "chown", func(_ *sdk.Conn, _ string, req chownReq) (wfs.PathReply, error) {
		return doChown(req)
	})
	sdk.Handle(b, "symlink", func(_ *sdk.Conn, _ string, req symlinkReq) (wfs.SymlinkReply, error) {
		return doSymlink(req)
	})
	sdk.Handle(b, "watch", func(_ *sdk.Conn, _ string, req pathReq) (wfs.PathReply, error) {
		return doWatch(c, req.Path)
	})
	sdk.Handle(b, "unwatch", func(_ *sdk.Conn, _ string, req pathReq) (wfs.PathReply, error) {
		return doUnwatch(req.Path)
	})
	sdk.Handle(b, "complete", func(_ *sdk.Conn, _ string, req completeReq) (completeResp, error) {
		matches := fmFS.Complete(req.Partial, 0)
		if matches == nil {
			matches = []string{}
		}
		return completeResp{Partial: req.Partial, Matches: matches}, nil
	})
	sdk.Handle(b, "clipboard_files_set", func(_ *sdk.Conn, id string, req clipboardFilesSetReq) (clipboardFilesSetResp, error) {
		return doClipboardFilesSet(c, id, req.Op, req.Paths)
	})

	// Fire-and-forget commands (no reply).
	sdk.HandleVoid(b, "request_initial", func(_ *sdk.Conn, _ string, _ struct{}) error {
		reply, err := listReplyFor("", initialPath())
		if err != nil {
			return err
		}
		return b.Emit("list_ok", reply)
	})
	sdk.HandleVoid(b, "save_state", func(conn *sdk.Conn, _ string, req saveStateReq) error {
		return conn.SaveState(req.State)
	})
	sdk.HandleVoid(b, "clipboard_copy_path", func(conn *sdk.Conn, _ string, req clipboardCopyPathReq) error {
		if req.Path == "" {
			return nil
		}
		return conn.ClipboardSet("text/plain", []byte(req.Path))
	})
	sdk.HandleVoid(b, "clipboard_files_get", func(conn *sdk.Conn, _ string, _ struct{}) error {
		// On-demand fetch — reuses the same push path as
		// onClipboardChanged. Runs in a goroutine because
		// ClipboardGet blocks on the SDK read loop.
		go pushFilesClipboardToFE(conn)
		return nil
	})
}

// ----- handler bodies -----

// listReplyFor lists the directory at path. Auto-resolves "" to
// initialPath() so the on-mount paint can reuse the handler shape.
func listReplyFor(_, path string) (wfs.ListReply, error) {
	if path == "" {
		return wfs.ListReply{}, sdk.Errf(sdk.ErrBadRequest, "missing path")
	}
	entries, abs, truncated, err := fmFS.List(path, maxListEntries)
	if err != nil {
		return wfs.ListReply{}, fsErr(err, path)
	}
	return wfs.ListReply{Path: abs, Entries: entries, Truncated: truncated}, nil
}

// readFile loads up to maxReadBytes of path. Binary files report a
// flag and empty content so the FE can render a placeholder.
func readFile(path string) (wfs.ReadReply, error) {
	if path == "" {
		return wfs.ReadReply{}, sdk.Errf(sdk.ErrBadRequest, "missing path")
	}
	abs, err := fmFS.Confine(path)
	if err != nil {
		return wfs.ReadReply{}, fsErr(err, path)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return wfs.ReadReply{}, fsErr(err, abs)
	}
	if st.IsDir() {
		return wfs.ReadReply{}, sdk.Errf("is_dir", "path is a directory")
	}
	f, err := os.Open(abs)
	if err != nil {
		return wfs.ReadReply{}, fsErr(err, abs)
	}
	defer f.Close()
	buf := make([]byte, maxReadBytes)
	n, err := f.Read(buf)
	if err != nil && err.Error() != "EOF" && n == 0 {
		return wfs.ReadReply{}, sdk.Err{Code: sdk.ErrIO, Msg: err.Error()}
	}
	chunk := buf[:n]
	binary := looksBinary(chunk)
	reply := wfs.ReadReply{
		Path:      abs,
		Size:      st.Size(),
		Truncated: int64(n) < st.Size(),
		Binary:    binary,
	}
	if !binary {
		reply.Content = string(chunk)
	}
	return reply, nil
}

func doChown(req chownReq) (wfs.PathReply, error) {
	if req.Path == "" {
		return wfs.PathReply{}, sdk.Errf(sdk.ErrBadRequest, "missing path")
	}
	if req.Owner == "" && req.Group == "" {
		return wfs.PathReply{}, sdk.Errf(sdk.ErrBadRequest, "missing owner and group")
	}
	abs, err := fmFS.Confine(req.Path)
	if err != nil {
		return wfs.PathReply{}, fsErr(err, req.Path)
	}
	uid := -1
	if req.Owner != "" {
		n, lerr := resolveUID(req.Owner)
		if lerr != nil {
			return wfs.PathReply{}, sdk.Err{Code: "bad_user", Msg: lerr.Error()}
		}
		uid = n
	}
	gid := -1
	if req.Group != "" {
		n, lerr := resolveGID(req.Group)
		if lerr != nil {
			return wfs.PathReply{}, sdk.Err{Code: "bad_group", Msg: lerr.Error()}
		}
		gid = n
	}
	if err := os.Chown(abs, uid, gid); err != nil {
		return wfs.PathReply{}, fsErr(err, abs)
	}
	return wfs.PathReply{Path: abs}, nil
}

func doSymlink(req symlinkReq) (wfs.SymlinkReply, error) {
	if req.Target == "" || req.LinkPath == "" {
		return wfs.SymlinkReply{}, sdk.Errf(sdk.ErrBadRequest, "missing target or link_path")
	}
	link, err := fmFS.Confine(req.LinkPath)
	if err != nil {
		return wfs.SymlinkReply{}, fsErr(err, req.LinkPath)
	}
	if req.Replace {
		if dstInfo, err := os.Lstat(link); err == nil {
			if err := os.Remove(link); err != nil {
				if dstInfo.IsDir() && strings.Contains(err.Error(), "directory not empty") {
					return wfs.SymlinkReply{}, sdk.Errf("not_empty_dir",
						"target is a non-empty directory; delete it via the queue first")
				}
				return wfs.SymlinkReply{}, fsErr(err, link)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return wfs.SymlinkReply{}, fsErr(err, link)
		}
	}
	if err := os.Symlink(req.Target, link); err != nil {
		code := wfs.ErrCode(err)
		if errors.Is(err, os.ErrExist) {
			code = "exists"
		}
		return wfs.SymlinkReply{}, sdk.Err{Code: code, Msg: err.Error()}
	}
	return wfs.SymlinkReply{Target: req.Target, LinkPath: link}, nil
}

func doWatch(c *sdk.Conn, path string) (wfs.PathReply, error) {
	if fmWatch == nil {
		return wfs.PathReply{}, sdk.Errf("unavailable", "fswatch unavailable")
	}
	if path == "" {
		return wfs.PathReply{}, sdk.Errf(sdk.ErrBadRequest, "missing path")
	}
	abs, err := fmFS.Confine(path)
	if err != nil {
		return wfs.PathReply{}, fsErr(err, path)
	}
	sub, first, err := fmWatch.Watch(abs)
	if err != nil {
		return wfs.PathReply{}, sdk.Err{Code: sdk.ErrIO, Msg: err.Error()}
	}
	if first {
		// One forwarder goroutine per Sub. The RefMap keeps the Sub
		// alive across multiple subscribers; the goroutine exits
		// when the last Unwatch closes the events channel.
		go func(s *fswatch.Sub) {
			for ev := range s.Events() {
				if err := bus.Emit("fs_event", fsEvent{Op: ev.Op.String(), Path: ev.Path}); err != nil {
					log.Printf("wash-fm send fs_event: %v", err)
					return
				}
			}
		}(sub)
	}
	return wfs.PathReply{Path: abs}, nil
}

func doUnwatch(path string) (wfs.PathReply, error) {
	if fmWatch == nil {
		return wfs.PathReply{Path: path}, nil
	}
	if path == "" {
		return wfs.PathReply{}, sdk.Errf(sdk.ErrBadRequest, "missing path")
	}
	abs, err := fmFS.Confine(path)
	if err != nil {
		return wfs.PathReply{}, fsErr(err, path)
	}
	fmWatch.Unwatch(abs)
	return wfs.PathReply{Path: abs}, nil
}

// doClipboardFilesSet stores op + paths on the router clipboard
// under our private mime. Every other fm window subscribed to
// clipboard.changed gets pushed a clipboard_files_state event.
// Paths are not confined here — the clipboard is a transport, not a
// write; confinement happens when bulk-ops actually does the move.
func doClipboardFilesSet(c *sdk.Conn, id, op string, paths []string) (clipboardFilesSetResp, error) {
	if op != "copy" && op != "cut" {
		return clipboardFilesSetResp{}, sdk.Errf(sdk.ErrBadRequest, "op must be copy or cut")
	}
	data, err := json.Marshal(filesClipboardPayload{Op: op, Paths: paths})
	if err != nil {
		return clipboardFilesSetResp{}, sdk.Err{Code: sdk.ErrInternal, Msg: err.Error()}
	}
	if err := c.ClipboardSet(fileClipboardMime, data); err != nil {
		return clipboardFilesSetResp{}, sdk.Err{Code: sdk.ErrIO, Msg: err.Error()}
	}
	// Echo state to THIS fm's FE immediately — the setter doesn't
	// receive its own OnClipboardChanged (router suppresses).
	_ = bus.Emit("clipboard_files_state", map[string]any{"op": op, "paths": paths})
	return clipboardFilesSetResp{Op: op, Paths: paths}, nil
}

// onClipboardChanged fires when ANOTHER app updates the clipboard.
func onClipboardChanged(c *sdk.Conn, mime string) {
	if mime == fileClipboardMime {
		go pushFilesClipboardToFE(c)
		return
	}
	// A non-files clipboard set means our previous cut/copy is no
	// longer the active clipboard content. Tell the FE.
	_ = bus.Emit("clipboard_files_state", map[string]any{"op": "", "paths": []string{}})
}

// pushFilesClipboardToFE fetches the current router clipboard and,
// if it's our mime, pushes the parsed payload to the FE.
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
	_ = bus.Emit("clipboard_files_state", map[string]any{"op": p.Op, "paths": p.Paths})
}

// ----- helpers -----

// fsErr wraps an internal/fs error into a bus.Err with the canonical
// code from wfs.ErrCode. path is the path the error is about; today
// the wire envelope only carries code/msg, but path is still useful
// for logging in the (unlikely) future where the bus learns a Path
// field.
func fsErr(err error, _ string) error {
	return sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
}

// fallbackPath returns abs when set, otherwise the user-supplied path.
// Used in error-returning ops where the internal/fs helper may have
// (or may not have) resolved the path before failing.
func fallbackPath(abs, orig string) string {
	if abs != "" {
		return abs
	}
	return orig
}

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

func looksBinary(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// fmIcon — Lucide sprite symbol name.
const fmIcon = "folder"
