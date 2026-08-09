// wash-vscode-workbench — one VS Code editor window. The launchable
// "VS Code" entry: each instance hosts one code-server workspace.
//
// On cold launch the FE prompts for a folder (FilePicker, directory
// mode), then asks the wash-vscode service (cross-app) to ensure
// code-server is up and gets back the ingress path; it renders the
// workbench in an <IngressFrame> at path?folder=<folder>. The chosen
// folder is persisted per-instance (SaveState) so a browser refresh
// reopens the same workspace without re-prompting.
//
// All workbench instances share the service's single code-server (one
// server, many windows). This BE is a thin bridge: the folder picker
// (fs.* via EnableFilePicker) and the save_folder persist are handled
// locally; everything else (ensure / subscribe) relays cross-app to
// com.wash.vscode, whose replies relay back to this window's FE.
package vscodeworkbench

import (
	"context"
	"embed"
	"github.com/sirmick/wash/internal/version"
	"io/fs"
	"log"

	"github.com/sirmick/wash/pkg/apps/registry"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

//go:embed all:assets
var assetsFS embed.FS

// serviceAppID is the wash-vscode background service (singleton). The
// router spawns it on demand when first addressed by id.
const serviceAppID = "com.wash.vscode"

// workbenchIcon names a sprite symbol (see web/shell/build-icons.mjs).
// square-code is the VS Code glyph; this is now the user-facing VS Code
// app (the former manager window is gone).
const workbenchIcon = "square-code"

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-vscode-workbench: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.vscode.workbench",
			Name:            "VS Code",
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-vscode-workbench",
			Surface:         sdk.SurfaceWindow,
			Icon:            workbenchIcon,
			Accent:          "#007acc",
			Instancing:      sdk.InstancingMulti, // one window per folder
			Window:          &sdk.WindowHints{DefaultWidth: 1100, DefaultHeight: 720},
		},
		Assets:       sub,
		OnReady:      onReady,
		OnAppMsg:     onAppMsg,
		OnAppMsgFrom: onAppMsgFrom,
	}
	registry.Register(&registry.App{
		Name:     "wash-vscode-workbench",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	// Folder picker (fs.*) first: EnableFilePicker wraps the app's
	// OnAppMsg so fs.* messages route to the in-process picker and
	// everything else falls through to onAppMsg below.
	sdk.EnableFilePicker(c)
	// Subscribe to the service so we get status / exited / shutdown
	// pushes (the service spawns on demand if not yet running).
	_ = c.SendAppMsgTo(wire.Recipient{AppID: serviceAppID}, map[string]any{"kind": "subscribe"})
	log.Printf("wash-vscode-workbench ready instance=%s window=%d", instanceID, windowID)
}

// onAppMsg handles own-FE messages. save_folder persists the chosen
// workspace folder per-instance (restored as wash:state on a later
// remount, so a refresh skips the picker). Everything else (ensure)
// relays cross-app to the service.
func onAppMsg(c *sdk.Conn, _ uint32, data any) {
	m, ok := data.(map[string]any)
	if !ok {
		return
	}
	if kind, _ := m["kind"].(string); kind == "save_folder" {
		_ = c.SaveState(map[string]any{"folder": m["folder"]})
		return
	}
	_ = c.SendAppMsgTo(wire.Recipient{AppID: serviceAppID}, m)
}

// onAppMsgFrom relays the service's replies (ready / status / exited /
// error / shutdown) down to this window's FE.
func onAppMsgFrom(c *sdk.Conn, _ uint32, data any, from wire.Sender) {
	if from.AppID != serviceAppID {
		return
	}
	_ = c.SendAppMsg(data)
}
