// Package music is wash-music (com.wash.music) — the Winamp-skinned
// music player window (docs/AUDIO.md §2). It embeds the apps/music/fe
// bundle, which renders Webamp (the JS implementation of the classic
// Winamp 2 skin engine), and serves audio to the browser over the
// ingress reverse-proxy as Case-1 "fe-decoded" sources: the BE hands
// the FE encoded bytes (mp3/wav/…) by URL and the browser's <audio>
// does the decode. The BE never touches PCM.
//
// M1 (this slice) proves the pipeline end to end: a fixed-size window
// renders Webamp with its default skin and plays one synthesized
// sample track served over a Range-capable file server published
// through PublishIngress. M2 brings the real library scan + playlist;
// M3 brings the com.wash.audio control plane + sidebar widget.
//
// FE↔BE wire (own-FE request/reply over wash:msg):
//
//	→ {kind:"tracks", id}   ← {kind:"tracks_ok", id, base, tracks:[{file,title,artist}]}
//
// `base` is the ingress path (e.g. "/app/<token>/"); the FE loads each
// track at base+file. Same router origin as the shell, so no CORS.
package music

import (
	"context"
	"embed"
	"io/fs"
	"log"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.1.0"

// AppID is this app's reserved id.
const AppID = "com.wash.music"

// musicIcon — Lucide sprite symbol name. Like every other app icon it
// is a name resolved against the shared /icons.svg sprite (launcher,
// taskbar, window titlebar all do <use href="icons.svg#name">); the
// manifest accent tints it. The same "music" glyph fronts the sidebar
// Audio widget. (A data: URI here would resolve to icons.svg#data:… and
// render blank everywhere.)
const musicIcon = "music"

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Printf("wash-music: assets sub: %v", err)
	}
	// Classic Winamp is pixel-locked. The window is chromeless (no wash
	// titlebar/border) and sized to Webamp's main-window footprint
	// (275×116) so the Winamp UI sits flush with no black margin and its
	// own titlebar is the only titlebar (docs/AUDIO.md §2). The FE drives
	// move/close through window.wash from Webamp's native chrome; EQ and
	// playlist open as Webamp's own sub-windows below the frame.
	resizable := false
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Music",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-music",
			Surface:         sdk.SurfaceWindow,
			Icon:            musicIcon,
			Accent:          "#30c060",
			Instancing:      sdk.InstancingSingle,
			Window:          &sdk.WindowHints{DefaultWidth: 275, DefaultHeight: 116, Resizable: &resizable, Chromeless: true},
		},
		Assets:  sub,
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-music",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }
