// wash-vscode-workbench — a hidden window that hosts one code-server
// workbench. It's a thin FE↔daemon relay (identical pattern to the
// manager): the FE asks the daemon to ensure code-server is up and
// gets back the ingress path + the folder this window should open;
// it renders the workbench in an <IngressFrame> at path?folder=…
//
// Hidden from the launcher — only the VS Code manager spawns these
// (via the daemon's open_window), one per opened folder. They all
// share the daemon's single code-server (one server, many windows).
package vscodeworkbench

import (
	"context"
	"embed"
	"io/fs"
	"log"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.1.0"

const managerAppID = "com.wash.vscode"

// workbenchIcon names a sprite symbol (see web/shell/build-icons.mjs).
// folder-code = an open code workspace, distinct from the manager's
// square-code.
const workbenchIcon = "folder-code"

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
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-vscode-workbench",
			Surface:         sdk.SurfaceWindow,
			Icon:            workbenchIcon,
			Accent:          "#007acc",
			Instancing:      sdk.InstancingMulti,
			Hidden:          true, // spawned by the daemon, not the launcher
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
	_ = c.SendAppMsgTo(wire.Recipient{AppID: managerAppID}, map[string]any{"kind": "subscribe"})
	log.Printf("wash-vscode-workbench ready instance=%s window=%d", instanceID, windowID)
}

func onAppMsg(c *sdk.Conn, _ uint32, data any) {
	if m, ok := data.(map[string]any); ok {
		_ = c.SendAppMsgTo(wire.Recipient{AppID: managerAppID}, m)
	}
}

func onAppMsgFrom(c *sdk.Conn, _ uint32, data any, from wire.Sender) {
	if from.AppID != managerAppID {
		return
	}
	_ = c.SendAppMsg(data)
}
