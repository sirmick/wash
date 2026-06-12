// Package music is wash-music (com.wash.music) — a minimalist NATIVE
// local music player (docs/MUSIC.md): a single resizable window with a
// recursive track list of one selectable folder, basic transport, and a
// now-playing info panel. Distinct from Washamp (the Webamp bonus app):
// this is plain wash UI (Solid + @wash/ui), reusing internal/medialib
// (scan + ingress serve), the @wash/ui media kit, and internal/audiorelay
// (the com.wash.audio producer bridge).
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

// musicIcon — Lucide sprite symbol name (added to web/shell/build-icons).
// A track-list glyph, distinct from Washamp's plain "music" note.
const musicIcon = "list-music"

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Printf("wash-music: assets sub: %v", err)
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Music",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-music",
			Surface:         sdk.SurfaceWindow,
			Icon:            musicIcon,
			Accent:          "#a78bfa", // violet — distinct from Washamp's green
			Instancing:      sdk.InstancingSingle,
			Window:          &sdk.WindowHints{DefaultWidth: 380, DefaultHeight: 520, MinWidth: 280, MinHeight: 320},
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
