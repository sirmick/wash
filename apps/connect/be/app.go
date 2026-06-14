// Package connect is wash-connect (com.wash.connect) — the windowed
// front-end for remote hosts (docs/REMOTE.md R2, §6.1).
//
// It is the user-facing face of the com.wash.remote background
// supervisor: enter a host → Connect → the supervisor SSHes out, brings
// up B's router and reports a local endpoint + status; wash-connect then
// lists B's apps (delivered over the shell's second RouterClient) and
// launches the one you pick. com.wash.remote stays a *background*
// supervisor so remote sessions persist when this window is closed.
//
// This BE is a thin relay between its own FE and com.wash.remote: the FE
// can't address another app with a router-attested sender, so every
// subscribe / connect / disconnect goes through here (the router stamps
// this instance as the cross-app sender). The supervisor's {kind:"state"}
// pushes come back here and are re-branded "remote.state" for the FE.
//
// The catalog of B's apps and the launch itself do NOT flow through this
// BE — they ride the shell's RouterClient directly (window.wash.catalogFor
// / launchOn), because that connection is the one actually attached to B.
//
// Wire shape — FE → BE:
//
//	→ subscribe / unsubscribe            (relayed to com.wash.remote)
//	→ connect    {host, remote_port?}    (relayed to com.wash.remote)
//	→ disconnect {host}                  (relayed to com.wash.remote)
//
// Wire shape — BE → FE:
//
//	← remote.state {state:{hosts:[…]}}   (re-branded from the supervisor)
package connect

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

const version = "0.9.0"

// AppID is the reserved-DNS id of the connect window app.
const AppID = "com.wash.connect"

// remoteAppID is the background supervisor wash-connect fronts. Duplicated
// as a string (not imported) so this app has no compile-time dependency
// on the supervisor package — the contract is the app-id, same trust
// boundary either way (mirrors the session BE's gateway convention).
const remoteAppID = "com.wash.remote"

// washIcon — Lucide sprite symbol (already in web/shell/build-icons.mjs).
const washIcon = "server-cog"

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-connect: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Connect",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-connect",
			Surface:         sdk.SurfaceWindow,
			Icon:            washIcon,
			Accent:          "#5c8fb0",
			Instancing:      sdk.InstancingSingleton,
			Capabilities:    []string{},
			Window:          &sdk.WindowHints{DefaultWidth: 540, DefaultHeight: 660},
		},
		Assets:  sub,
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-connect",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def returns the *sdk.AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

// run is the post-handshake event loop used by the multi-call dispatcher.
func run(ctx context.Context) error { return sdk.Run(ctx, def) }

// onReady wires the relay: FE control ops out to com.wash.remote, and the
// supervisor's state pushes back to the FE.
func onReady(c *sdk.Conn, instanceID string, _ uint32) {
	log.Printf("wash-connect ready instance=%s", instanceID)
	bus := sdk.NewBus(c)

	// FE → supervisor. subscribe/unsubscribe carry no payload; the
	// router stamps this instance as the sender so the supervisor knows
	// who to push state to.
	sdk.HandleVoid(bus, "subscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: remoteAppID}, map[string]any{"kind": "subscribe"})
	})
	sdk.HandleVoid(bus, "unsubscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: remoteAppID}, map[string]any{"kind": "unsubscribe"})
	})
	sdk.HandleVoid(bus, "connect", func(conn *sdk.Conn, _ string, req connectReq) error {
		if req.Host == "" {
			return nil
		}
		return conn.SendAppMsgTo(wire.Recipient{AppID: remoteAppID}, map[string]any{
			"kind": "connect", "host": req.Host, "remote_port": req.RemotePort,
		})
	})
	sdk.HandleVoid(bus, "disconnect", func(conn *sdk.Conn, _ string, req disconnectReq) error {
		if req.Host == "" {
			return nil
		}
		return conn.SendAppMsgTo(wire.Recipient{AppID: remoteAppID}, map[string]any{
			"kind": "disconnect", "host": req.Host,
		})
	})

	// Supervisor → FE. The supervisor's StateService pushes {kind:"state",
	// state:{hosts:[…]}}; re-brand to "remote.state" so the FE dispatcher
	// stays unambiguous. State is `any` (not json.RawMessage) so the CBOR
	// re-encode stays structured, not base64 (see wash_cbor_json_pitfall).
	sdk.HandleFromVoid(bus, "state", func(conn *sdk.Conn, _ string, req stateRelay, from wire.Sender) error {
		if from.AppID != remoteAppID {
			return nil
		}
		return conn.SendAppMsg(map[string]any{"kind": "remote.state", "state": req.State})
	})
}

type connectReq struct {
	Host       string `json:"host"`
	RemotePort int    `json:"remote_port"`
}

type disconnectReq struct {
	Host string `json:"host"`
}

// stateRelay captures the `state` field of the supervisor's StateService
// push for verbatim re-forwarding to the FE.
type stateRelay struct {
	State any `json:"state"`
}
