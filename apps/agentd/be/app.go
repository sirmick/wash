// Package agentd is wash-agentd (com.wash.agentd): the singleton service
// that hosts coding agents over ACP, and the roster of them the desktop
// shows. One row per hosted session, so "what are my agents doing?" has one
// answer across every Agent window.
//
// Subscribers (the session gateway, Agent windows) send {kind:"subscribe"}
// / {kind:"unsubscribe"} and receive {kind:"state", state:{rows:[…], …}}
// through sdk.StateService. A session that has exited keeps its row, greyed,
// until the sweep drops it.
package agentd

import (
	"context"

	"github.com/sirmick/wash/internal/version"
	"github.com/sirmick/wash/pkg/apps/registry"
	"github.com/sirmick/wash/pkg/sdk"
)

// AppID is the reserved app id for the agent roster service.
const AppID = "com.wash.agentd"

var def *sdk.AppDef

func init() {
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Agents",
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Surface:         sdk.SurfaceBackground,
			Instancing:      sdk.InstancingSingleton,
			// Resume opens a terminal for a remembered session (§13).
			// That is the only thing this service spawns, and only when a
			// human clicked the button.
			// CapIdleInhibit is why a session survives a closed lid. Zero
			// attached shells reads as "nothing of value is happening",
			// which is true of a desktop and false of an agent — an agent
			// matters most exactly when nobody is watching. This service
			// is the only thing on the box that knows the difference.
			Capabilities: []string{sdk.CapSpawn, sdk.CapIdleInhibit, sdk.CapActivityNote},
		},
		OnReady:        onReady,
		OnInstanceGone: onInstanceGone,
		// The router's reply to the terminal spawn a Resume click asks
		// for (§13); the new instance is then told what to run.
		OnSpawnResult: onSpawnResult,
	}
	registry.Register(&registry.App{
		Name:     "wash-agentd",
		Manifest: def.Manifest,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }
