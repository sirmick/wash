// Package imageview is the wash-imageview app: a small single-image viewer
// (zoom/pan) with a thumbnail list of the sibling images on the left. The
// BE scans a directory for image files and serves their bytes + thumbnails
// over a raw channel via internal/thumbs (no HTTP). The standalone shim
// (cmd/wash-imageview) and the multi-call dispatcher both register this
// package; sdk does the rest.
//
// FE ↔ BE:
//
//	FE → iv   scan     {dir?}                 → iv → FE  scan_ok {dir, images:[{name,path}]}
//	FE → iv   get_file {req_id, path, dim?}   (internal/thumbs; raw-channel bytes)
//
// The full-resolution display and the list thumbnails both go through
// get_file (dim=0 for full, dim>0 for a thumbnail).
package imageview

import (
	"context"
	"embed"
	"github.com/sirmick/wash/internal/version"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sirmick/wash/internal/apps/registry"
	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/thumbs"
)

//go:embed all:assets
var assetsFS embed.FS

// maxImages caps a scan so a huge folder can't blow up the list message.
// The FE list is windowed, so this can be generous; it bounds the one
// scan_ok message, not the DOM.
const maxImages = 5000

// ivIcon — Lucide sprite symbol; added to web/shell/build-icons.mjs.
const ivIcon = "image"

// imageExts are the files the viewer lists. The browser renders all of
// these natively in the main <img>; internal/thumbs only thumbnails the
// jpeg/png/gif subset, and the FE falls back to an icon for the rest.
var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".bmp": true, ".svg": true, ".avif": true, ".ico": true, ".tiff": true, ".tif": true,
}

var (
	ivFS *wfs.FS
	def  *sdk.AppDef
)

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-imageview: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.imageview",
			Name:            "Image Viewer",
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-imageview",
			Surface:         sdk.SurfaceWindow,
			Icon:            ivIcon,
			Accent:          "#d0876f",
			Instancing:      sdk.InstancingMulti,
			Capabilities:    []string{},
			// Image extensions this app handles for open routing (fm
			// double-click → router → wash-imageview --open <path>).
			Opens: []string{
				".jpg", ".jpeg", ".png", ".gif", ".webp",
				".bmp", ".svg", ".avif", ".ico", ".tiff", ".tif",
			},
			Window: &sdk.WindowHints{DefaultWidth: 900, DefaultHeight: 640},
		},
		Assets:  sub,
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-imageview",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def returns the *sdk.AppDef for the standalone shim's sdk.Main.
func Def() *sdk.AppDef { return def }

// run is the post-handshake event loop used by the multi-call dispatcher.
func run(ctx context.Context) error { return sdk.Run(ctx, def) }

type scanReq struct {
	Dir string `json:"dir"`
}

type imageItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-imageview ready instance=%s window=%d", instanceID, windowID)
	// Empty root = unconfined (browse anywhere), matching wash-fm / wash-edit.
	// NOT "/" — wfs.New("/") is a degenerate sandbox: Confine tests
	// hasPrefix(path, root+"/") == "//", which rejects every real path, so the
	// viewer would see nothing outside "/" itself. The router ships a non-empty
	// Root only when a sandbox is actually configured.
	root := c.Session().Root
	if root != "" {
		log.Printf("wash-imageview: sandbox root=%s (from router session)", root)
	}
	ivFS = wfs.New(root)

	// FilePicker bridge for the Open dialog (Open folder / Open image).
	// Install before NewBus so the bus wraps it: fs.* messages route to the
	// picker, everything else (scan, get_file) flows through the bus.
	// Mirrors wash-edit.
	sdk.EnableFilePicker(c)

	bus := sdk.NewBus(c)

	// Image bytes + thumbnails over a raw channel, confined to the fs root.
	thumbs.RegisterServer(bus, ivFS.Confine)

	// launchOpen: the file the router told us to open (--open argv). The
	// first default scan resolves to its folder and tells the FE to select
	// it; consumed after, so later folder switches behave normally.
	launchOpen := c.LaunchOpenPath()

	// scan lists the image files in a directory (the open path's folder,
	// or the default) and pushes them to the FE.
	sdk.HandleVoid(bus, "scan", func(_ *sdk.Conn, _ string, req scanReq) error {
		dir := req.Dir
		open := ""
		if dir == "" {
			if launchOpen != "" {
				dir, open, launchOpen = filepath.Dir(launchOpen), launchOpen, ""
			} else {
				dir = defaultDir()
			}
		} else if abs, err := ivFS.Confine(dir); err == nil {
			// The Open dialog hands us either a folder ("Open folder") or a
			// single image ("Open image"). For a file, scan its folder and
			// pre-select it — same shape as the --open launch path.
			if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
				dir, open = filepath.Dir(dir), dir
			}
		}
		abs, images := scanImages(dir)
		payload := map[string]any{"dir": abs, "images": images}
		if open != "" {
			// Match the item path format scanImages emits so the FE selects it.
			payload["open"] = filepath.Join(abs, filepath.Base(open))
		}
		_ = bus.Emit("scan_ok", payload)
		return nil
	})
}

// scanImages confines dir, lists its image files (non-recursive), and
// returns the confined absolute dir + the sorted, capped items. On any
// error it returns the dir and an empty list so the FE shows "no images".
func scanImages(dir string) (string, []imageItem) {
	abs, err := ivFS.Confine(dir)
	if err != nil {
		return dir, nil
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return abs, nil
	}
	out := make([]imageItem, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() || !imageExt(e.Name()) {
			continue
		}
		out = append(out, imageItem{Name: e.Name(), Path: filepath.Join(abs, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	if len(out) > maxImages {
		out = out[:maxImages]
	}
	return abs, out
}

func imageExt(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}

// defaultDir is the folder scanned when no dir is given: $WASH_IMAGEVIEW_DIR,
// else ~/Pictures, else ~.
func defaultDir() string {
	if d := os.Getenv("WASH_IMAGEVIEW_DIR"); d != "" && isDir(d) {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil {
		if p := filepath.Join(home, "Pictures"); isDir(p) {
			return p
		}
		return home
	}
	return "/"
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
