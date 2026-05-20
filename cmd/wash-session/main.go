// wash-session — the session app (declares surface=desktop).
//
// Renders the wash desktop chrome. When the user clicks the
// launcher, the FE sends an APP_MSG to this process; we reply by
// emitting spawn.request{app_id} on the event channel. The router
// answers with spawn.ok / spawn.err on a follow-up frame.
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
		log.Fatalf("wash-session: assets: %v", err)
	}
	sdk.Main(&sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.session",
			Name:            "wash session",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-session",
			Surface:         sdk.SurfaceDesktop,
			Icon:            washIcon,
			Instancing:      sdk.InstancingSingle,
			Capabilities:    []string{sdk.CapSpawn},
			Window:          &sdk.WindowHints{},
		},
		Assets:   sub,
		OnAppMsg: onAppMsg,
	})
}

// washIcon is the inline SVG "W" mark used in catalog/launcher
// rendering. Kept short so the inline-data-URI cap (64 KiB) is not in
// play.
const washIcon = "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'><text x='2' y='13' font-family='system-ui' font-size='13' font-weight='bold' fill='white'>W</text></svg>"

// onAppMsg interprets the FE's launcher click: data is the
// (CBOR-decoded) JSON the FE sent via app_msg.send. v0.0 expects a
// {"action":"launch","app_id":"..."} object.
func onAppMsg(c *sdk.Conn, _ uint32, data any) {
	m, ok := data.(map[any]any)
	if !ok {
		return
	}
	action, _ := m["action"].(string)
	if action != "launch" {
		return
	}
	appID, _ := m["app_id"].(string)
	if appID == "" {
		return
	}
	if err := c.SpawnRequest(appID); err != nil {
		log.Printf("wash-session: spawn.request %s: %v", appID, err)
	}
}
