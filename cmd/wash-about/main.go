// wash-about — a small windowed app that displays version and
// license. The spawn flow's exit point: launching it via the session
// app's launcher proves the v0.0 spine end to end.
package main

import (
	"embed"
	"io/fs"
	"log"

	"github.com/sirmick/wash/internal/sdk"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.0.0"

func main() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("wash-about: assets: %v", err)
	}
	sdk.Main(&sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.about",
			Name:            "About wash",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-about",
			Surface:         sdk.SurfaceWindow,
			Icon:            washIcon,
			Instancing:      sdk.InstancingMulti,
			Capabilities:    []string{},
			Window:          &sdk.WindowHints{DefaultWidth: 480, DefaultHeight: 320},
		},
		Assets: sub,
		// Set the titlebar text as soon as the window is mapped, so
		// the user sees "About wash" instead of the default name.
		OnMapped: func(c *sdk.Conn, _ uint32) {
			if err := c.SetTitle("About wash"); err != nil {
				log.Printf("wash-about: set title: %v", err)
			}
		},
		// OnCloseRequested is nil — the SDK's default confirms allow=true
		// immediately (matches the spec for this app).
	})
}

// washIcon — Lucide sprite symbol name. See cmd/wash-fm/main.go's
// fmIcon for the resolution scheme.
const washIcon = "info"
