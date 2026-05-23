// Package about is the wash-about app's compiled-in form. The
// standalone shim (cmd/wash-about/main.go) imports this package
// blank so its init() registers; sdk.Main does the rest. The
// multi-call dispatcher (cmd/wash) blank-imports it too — same
// registration, same Run, exec'd via argv[0] = "wash-about".
package about

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

const version = "0.0.0"

// washIcon — Lucide sprite symbol name. The shell's sprite already
// includes "info" via web/shell/build-icons.mjs.
const washIcon = "info"

// def is built once at init and reused by Def (for sdk.Main in the
// standalone shim) and by run (for the registered Run callback).
var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		// Panic instead of log+continue: a missing asset sub-fs means
		// the //go:embed pattern produced an empty FS, which never
		// works for an app. Fail loudly at startup.
		panic("wash-about: assets: " + err.Error())
	}
	def = &sdk.AppDef{
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
	}
	registry.Register(&registry.App{
		Name:     "wash-about",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def returns the *sdk.AppDef for the standalone shim to hand to
// sdk.Main. Keeps the manifest/Assets/callbacks construction in
// exactly one place — shim and registry both reference the same struct.
func Def() *sdk.AppDef { return def }

// run is the post-handshake event loop used by the multi-call
// dispatcher (which has already handled --wash-manifest itself).
func run(ctx context.Context) error { return sdk.Run(ctx, def) }
