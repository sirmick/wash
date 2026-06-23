// Package remote is wash-remote — the A-side connectivity service for
// running wash apps on remote hosts (docs/REMOTE.md, R2).
//
// Architecture:
//   - Singleton, surface=background. No window, no FE bundle.
//   - Manages SSH connections to remote hosts: for each, it brings up a
//     wash-router on the host (bound to loopback, cross-origin allowed)
//     and forwards it to a local port via `ssh -L`. The desktop's shell
//     then opens a second RouterClient to that local endpoint, so the
//     remote host's app windows composite into this desktop.
//   - Publishes per-host status + the local endpoint via sdk.StateService;
//     the session FE's Hosts widget renders it and tells the shell to
//     attach/detach the endpoint.
//
// This first cut ties the remote router's lifetime to the ssh process
// (one `ssh -L … wash-router …`): connection up ⇒ router up. Detaching the
// router so a transient SSH drop doesn't kill it (the durable, reconnect-
// surviving design) is a later milestone — see docs/REMOTE.md §2/§9.
//
// Wire shape — controls (cross-app from the session FE):
//
//	→ connect    {host, remote_port?}
//	→ disconnect {host}
//	→ subscribe / unsubscribe (sdk.StateService)
//
// Wire shape — state pushed to subscribers:
//
//	{ "kind":"state", "state": { "hosts":[ {host,origin,status,error,code}, ... ] } }
package remote

import (
	"context"
	"embed"
	"io/fs"
	"log"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/version"
	"github.com/sirmick/wash/internal/wire"
)

// AppID is the reserved-DNS id of the remote-hosts service.
const AppID = "com.wash.remote"

// Status values for a host connection.
const (
	StatusStarting     = "starting"
	StatusUp           = "up"
	StatusReconnecting = "reconnecting"
	StatusDown         = "down"
)

// HostState is one remote host's connection state, surfaced to the FE.
type HostState struct {
	// Host is the SSH target as the user typed it (user@host[:port]); it
	// doubles as the stable FE origin tag.
	Host string `json:"host"`
	// Origin is the FE origin tag for this host (== Host today; kept
	// distinct so a friendly label can diverge from the SSH target later).
	Origin string `json:"origin"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Code classifies a StatusDown. "auth" means SSH refused
	// authentication under BatchMode (no usable key in the agent) — the
	// signal for wash-connect to offer its ssh-add auth widget and retry
	// (docs/REMOTE.md §6.1, mechanism a). The supervisor itself never
	// prompts; it stays BatchMode-only. Empty for non-auth failures.
	Code string `json:"code,omitempty"`
}

// State is the published state shape.
type State struct {
	Hosts  []HostState  `json:"hosts"`
	Mounts []MountState `json:"mounts,omitempty"`
	// Candidates are hosts found on the network (mDNS today) that the user
	// hasn't saved or connected to — rendered as an "On your network" list.
	Candidates []Candidate `json:"candidates,omitempty"`
}

// assetsFS embeds the settings "Remote" panel bundle (panel.js), staged
// by the Makefile from apps/remote/fe/dist. wash-remote has no window;
// this is only the panel the settings app hosts.
//
//go:embed all:assets
var assetsFS embed.FS

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Printf("wash-remote: assets sub: %v", err)
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Remote Hosts",
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Surface:         sdk.SurfaceBackground,
			Instancing:      sdk.InstancingSingleton,
			// Settings "Remote" panel: lists live sessions + mounts with
			// graceful teardown; "Open Connect…" opens the com.wash.connect
			// window. The supervisor owns the panel (it owns the state and the
			// disconnect/unmount handlers), mirroring com.wash.netd / the
			// Network panel.
			SettingsPanel: &sdk.SettingsPanel{
				Section: "Remote",
				Element: "wash-settings-panel-remote",
			},
		},
		Assets:  sub,
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-remote",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

func onReady(c *sdk.Conn, instanceID string, _ uint32) {
	log.Printf("wash-remote ready instance=%s", instanceID)
	bus := sdk.NewBus(c)
	svc := sdk.NewStateService(bus, State{})
	sup := newSupervisor(svc, c)

	// LAN discovery — announce this box and browse for peers, mirroring
	// found hosts into State.Candidates (see discovery.go).
	newDiscoverer(svc).start()

	sdk.HandleFromVoid(bus, "connect", func(_ *sdk.Conn, _ string, req connectReq, _ wire.Sender) error {
		if req.Host == "" {
			return nil
		}
		sup.connect(req.Host, req.RemotePort)
		return nil
	})
	sdk.HandleFromVoid(bus, "disconnect", func(_ *sdk.Conn, _ string, req disconnectReq, _ wire.Sender) error {
		if req.Host == "" {
			return nil
		}
		sup.disconnect(req.Host)
		return nil
	})

	mounts := newMountManager(svc, c)
	mounts.restoreMounts() // re-establish "reconnect at launch" mounts
	sdk.HandleFromVoid(bus, "mount", func(_ *sdk.Conn, _ string, req mountCtlReq, _ wire.Sender) error {
		if req.Host == "" {
			return nil
		}
		go mounts.mount(req.Host, req.RemoteRoot, req.Persist) // mounting blocks on ssh + FUSE
		return nil
	})
	sdk.HandleFromVoid(bus, "unmount", func(_ *sdk.Conn, _ string, req unmountCtlReq, _ wire.Sender) error {
		if req.MountPoint == "" {
			return nil
		}
		go mounts.unmount(req.MountPoint)
		return nil
	})
}

type connectReq struct {
	Host       string `json:"host"`
	RemotePort int    `json:"remote_port"`
}

type disconnectReq struct {
	Host string `json:"host"`
}
