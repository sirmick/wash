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
	"github.com/sirmick/wash/internal/version"
	"io/fs"
	"log"
	"sync"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/pty"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

// AppID is the reserved-DNS id of the connect window app.
const AppID = "com.wash.connect"

// remoteAppID is the background supervisor wash-connect fronts. Duplicated
// as a string (not imported) so this app has no compile-time dependency
// on the supervisor package — the contract is the app-id, same trust
// boundary either way (mirrors the session BE's gateway convention).
const remoteAppID = "com.wash.remote"

// washIcon — Lucide sprite symbol (already in web/shell/build-icons.mjs).
// "monitor" (a remote machine) — distinct from wash-services' "server-cog".
const washIcon = "monitor"

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
			Version:         version.Version,
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
			"kind": "connect", "host": req.Host, "addr": req.Addr, "remote_port": req.RemotePort,
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
	sdk.HandleVoid(bus, "mount", func(conn *sdk.Conn, _ string, req mountReq) error {
		if req.Host == "" {
			return nil
		}
		return conn.SendAppMsgTo(wire.Recipient{AppID: remoteAppID}, map[string]any{
			"kind": "mount", "host": req.Host, "remote_root": req.RemoteRoot, "persist": req.Persist,
		})
	})
	sdk.HandleVoid(bus, "unmount", func(conn *sdk.Conn, _ string, req unmountReq) error {
		if req.MountPoint == "" {
			return nil
		}
		return conn.SendAppMsgTo(wire.Recipient{AppID: remoteAppID}, map[string]any{
			"kind": "unmount", "mount_point": req.MountPoint,
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

	// Interactive SSH auth (docs/REMOTE.md §6.1, mechanism a). The
	// supervisor is BatchMode-only and reports a host down with code
	// "auth" when no usable key authorises it; wash-connect owns the fix.
	// auth_begin spawns ssh-copy-id in a pty bound to this window's
	// channel; the FE renders an xterm on it so the user enters the
	// target's password once. ssh-copy-id installs our public key into the
	// target's authorized_keys and exits; the FE then re-issues connect,
	// which now succeeds under BatchMode. The supervisor never sees a
	// prompt — it stays the sole tunnel owner.
	registerAuth(bus)

	// Bookmarks (docs/REMOTE.md §6.1) — long-lived host[+app] targets the
	// FE saves and replays for one-click connect/launch. Persisted to
	// disk (see bookmarks.go), unlike router-memory FE state.
	registerBookmarks(bus)
}

func registerBookmarks(bus *sdk.Bus) {
	push := func(conn *sdk.Conn) error {
		bms, err := loadBookmarks()
		if err != nil {
			log.Printf("wash-connect: load bookmarks: %v", err)
			bms = []Bookmark{}
		}
		return conn.SendAppMsg(map[string]any{"kind": "bookmarks", "bookmarks": bms})
	}
	sdk.HandleVoid(bus, "bookmarks_load", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return push(conn)
	})
	sdk.HandleVoid(bus, "bookmarks_save", func(conn *sdk.Conn, _ string, req bookmarksSaveReq) error {
		if err := saveBookmarks(req.Bookmarks); err != nil {
			log.Printf("wash-connect: save bookmarks: %v", err)
			// Re-push the on-disk truth so the FE doesn't drift from a
			// failed write.
			return push(conn)
		}
		// Echo the saved set back as the authoritative list.
		return conn.SendAppMsg(map[string]any{"kind": "bookmarks", "bookmarks": req.Bookmarks})
	})
}

type bookmarksSaveReq struct {
	Bookmarks []Bookmark `json:"bookmarks"`
}

// authState holds the single in-flight ssh-copy-id session. wash-connect is
// singleton (one window), so one session at a time; a new auth_begin
// supersedes any prior.
var authState struct {
	mu      sync.Mutex
	session *pty.Session
}

func registerAuth(bus *sdk.Bus) {
	sdk.HandleVoid(bus, "auth_begin", func(conn *sdk.Conn, _ string, req authBeginReq) error {
		// Supersede any prior session so a retried auth doesn't leak a pty.
		authState.mu.Lock()
		if authState.session != nil {
			authState.session.CloseWithReason("superseded")
			authState.session = nil
		}
		authState.mu.Unlock()

		// ssh-copy-id installs our public key into the target's authorized_keys
		// after one password prompt, BOOTSTRAPPING trust on a host we have no
		// key on yet (the common case for a freshly-discovered peer). ssh-add
		// only helps when a key is *already* authorized but locked — useless
		// for a first connection. We dial the same target string the connect
		// uses (a name, so the user's ~/.ssh/config picks the identity).
		argv := []string{"ssh-copy-id"}
		if req.KeyFile != "" {
			argv = append(argv, "-i", req.KeyFile)
		}
		argv = append(argv, req.Host)
		// OpenChannel can't run on the SDK read goroutine — spawn off it.
		go func() {
			sess, err := pty.Open(context.Background(), conn, conn.WindowID(), 80, 24, argv, pty.WithWashEnv,
				func(s *pty.Session, reason string) {
					authState.mu.Lock()
					if authState.session == s {
						authState.session = nil
					}
					authState.mu.Unlock()
					_ = conn.SendAppMsg(map[string]any{"kind": "auth_closed", "host": req.Host, "reason": reason})
				})
			if err != nil {
				log.Printf("wash-connect: ssh-copy-id open: %v", err)
				_ = conn.SendAppMsg(map[string]any{"kind": "auth_error", "host": req.Host, "msg": err.Error()})
				return
			}
			authState.mu.Lock()
			authState.session = sess
			authState.mu.Unlock()
			_ = conn.SendAppMsg(map[string]any{"kind": "auth_opened", "host": req.Host, "channel_id": uint64(sess.ID())})
		}()
		return nil
	})
	sdk.HandleVoid(bus, "auth_cancel", func(_ *sdk.Conn, _ string, _ struct{}) error {
		authState.mu.Lock()
		if authState.session != nil {
			authState.session.CloseWithReason("cancelled")
			authState.session = nil
		}
		authState.mu.Unlock()
		return nil
	})
}

type connectReq struct {
	Host       string `json:"host"`
	Addr       string `json:"addr"` // optional IP fallback when Host doesn't resolve
	RemotePort int    `json:"remote_port"`
}

type disconnectReq struct {
	Host string `json:"host"`
}

type mountReq struct {
	Host       string `json:"host"`
	RemoteRoot string `json:"remote_root"`
	Persist    bool   `json:"persist"`
}

type unmountReq struct {
	MountPoint string `json:"mount_point"`
}

// authBeginReq starts an ssh-copy-id session for Host (a name, so the user's
// ssh_config picks the identity). KeyFile is optional — empty copies the
// default public key, set runs `ssh-copy-id -i <keyfile> <host>`.
type authBeginReq struct {
	Host    string `json:"host"`
	KeyFile string `json:"key_file"`
}

// stateRelay captures the `state` field of the supervisor's StateService
// push for verbatim re-forwarding to the FE.
type stateRelay struct {
	State any `json:"state"`
}
