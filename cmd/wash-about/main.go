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

const washIcon = "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'><circle cx='8' cy='8' r='6' fill='none' stroke='white' stroke-width='1.5'/><circle cx='8' cy='5.5' r='0.8' fill='white'/><rect x='7.3' y='7.5' width='1.4' height='4' fill='white'/></svg>"
