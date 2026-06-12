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

// musicIcon is a lucide "music" glyph as an inline SVG, tinted with the
// app accent (a '#' is %23-encoded in the data URI). Windowed apps must
// supply an icon; the same glyph fronts the launcher entry.
const musicIcon = `data:image/svg+xml,<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="%2330c060" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 18V5l12-2v13"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="16" r="3"/></svg>`

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Printf("wash-music: assets sub: %v", err)
	}
	// Classic Winamp is pixel-locked; the window is fixed-size and
	// non-resizable (docs/AUDIO.md §2). 300×420 fits Webamp's stacked
	// main+playlist windows with a little margin. True borderless is a
	// WM feature for later — for now Webamp draws inside a normal frame.
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
			Window:          &sdk.WindowHints{DefaultWidth: 300, DefaultHeight: 420, Resizable: &resizable},
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
