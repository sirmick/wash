// Package radio is wash-radio (com.wash.radio) — a minimalist NATIVE
// internet-radio player (docs/RADIO.md): a single window with a station
// list, basic transport, and a now-playing panel. The BE reverse-proxies
// the upstream stream over the ingress (fixing mixed-content + CORS that
// otherwise break browser radio); the FE plays it with <audio> and
// registers with com.wash.audio. Reuses the @wash/ui media kit +
// internal/audiorelay, like the native Music app.
package radio

import (
	"context"
	"embed"
	"github.com/sirmick/wash/internal/version"
	"io/fs"
	"log"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
)

//go:embed all:assets
var assetsFS embed.FS

// AppID is this app's reserved id.
const AppID = "com.wash.radio"

// radioIcon — Lucide sprite symbol name (added to web/shell/build-icons).
const radioIcon = "radio"

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Printf("wash-radio: assets sub: %v", err)
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Radio",
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-radio",
			Surface:         sdk.SurfaceWindow,
			Icon:            radioIcon,
			Accent:          "#5fb6c8", // cyan — distinct from Music (violet) / Washamp (green)
			Instancing:      sdk.InstancingSingle,
			Window:          &sdk.WindowHints{DefaultWidth: 380, DefaultHeight: 520, MinWidth: 280, MinHeight: 320},
		},
		Assets:  sub,
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-radio",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }
