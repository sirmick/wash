// go-about — the minimal wash app. A window that shows the BE⇄FE app_msg
// round-trip: the BE greets the FE on ready, the FE sends a signal on a
// button click, the BE echoes it back. Copy this directory to start a new
// Go wash app; swap the id/name/element and grow from here.
//
// Build:  make            (just `go build` — the FE is hand-written, no bundler)
// Probe:  ./out/go-about --wash-manifest   (sdk.Main handles this flag)
package main

import (
	"embed"
	"io/fs"
	"log"

	"github.com/sirmick/wash/pkg/sdk"
)

// The FE bundle lives in assets/index.js (vanilla JS, no build step) and
// is embedded into the binary. sdk.Main ships it raw in the framed probe.
//
//go:embed all:assets
var assetsFS embed.FS

// icon is a tiny inline SVG — the manifest requires one for windowed apps.
const icon = `data:image/svg+xml,<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="%2360c090" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 16v-4M12 8h.01"/></svg>`

func main() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("go-about: assets: %v", err)
	}
	def := &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.examples.goabout",
			Name:            "Go About (example)",
			Version:         "0.1.0",
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-go-about",
			Surface:         sdk.SurfaceWindow,
			Icon:            icon,
			Instancing:      sdk.InstancingMulti,
			Window:          &sdk.WindowHints{DefaultWidth: 440, DefaultHeight: 340},
		},
		Assets: sub,
		// OnReady: greet the FE once it has mounted.
		OnReady: func(c *sdk.Conn, instanceID string, win uint32) {
			_ = c.SendAppMsg(map[string]any{"kind": "hello", "msg": "hello from the Go BE"})
		},
		// OnAppMsg: the FE sent us a signal — echo it straight back.
		OnAppMsg: func(c *sdk.Conn, win uint32, data any) {
			_ = c.SendAppMsg(map[string]any{"kind": "echo", "from_fe": data})
		},
	}
	// sdk.Main intercepts --wash-manifest (prints the framed probe) or
	// otherwise runs the app loop until the socket closes.
	sdk.Main(def)
}
