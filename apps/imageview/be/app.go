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

const version = "0.9.0"

// maxImages caps a scan so a huge folder can't blow up the list message.
const maxImages = 1000

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
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-imageview",
			Surface:         sdk.SurfaceWindow,
			Icon:            ivIcon,
			Accent:          "#d0876f",
			Instancing:      sdk.InstancingMulti,
			Capabilities:    []string{},
			Window:          &sdk.WindowHints{DefaultWidth: 900, DefaultHeight: 640},
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
	root := "/"
	if r := c.Session().Root; r != "" {
		root = r
		log.Printf("wash-imageview: sandbox root=%s (from router session)", root)
	}
	ivFS = wfs.New(root)
	bus := sdk.NewBus(c)

	// Image bytes + thumbnails over a raw channel, confined to the fs root.
	thumbs.RegisterServer(bus, ivFS.Confine)

	// scan lists the image files in a directory (the open path's folder,
	// or the default) and pushes them to the FE.
	sdk.HandleVoid(bus, "scan", func(_ *sdk.Conn, _ string, req scanReq) error {
		dir := req.Dir
		if dir == "" {
			dir = defaultDir()
		}
		abs, images := scanImages(dir)
		_ = bus.Emit("scan_ok", map[string]any{"dir": abs, "images": images})
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
