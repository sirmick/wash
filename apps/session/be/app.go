// wash-session — the session app (declares surface=desktop).
//
// Renders the wash desktop chrome. When the user clicks the
// launcher, the FE sends an APP_MSG to this process; we reply by
// emitting spawn.request{app_id} on the event channel. The router
// answers with spawn.ok / spawn.err on a follow-up frame.
package session

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"io/fs"
	"log"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

// defaultWallpaper is the fallback image used when desktop.json has
// no wallpaper.path set (or the file at that path is missing). Source
// is "04. Catppuccin Mocha" from github.com/fr0st-xyz/wallz (GPL-3.0,
// compatible with wash's AGPL-3.0). Adds ~360 KB to the session
// binary — acceptable for a singleton that ships once per install.
//
//go:embed default-wallpaper.png
var defaultWallpaper []byte

const version = "0.0.0"

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-session: assets: " + err.Error())
	}
	def = &sdk.AppDef{
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
		OnReady:  onReady,
		OnAppMsg: onAppMsg,
	}
	registry.Register(&registry.App{
		Name:     "wash-session",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

// onReady ships the initial desktop config and starts watching the
// config file for live updates from wash-settings. Also installs a
// bus + the one kind-shaped handler we have (desktop.request); the
// action-keyed FE messages (launch / spawn_root) flow through the
// fallback OnAppMsg path, since they don't fit the bus's kind
// dispatch.
func onReady(c *sdk.Conn, _ string, _ uint32) {
	sendDesktopConfig(c)
	sendSystemInfo(c)
	startConfigWatcher(c)
	bus := sdk.NewBus(c)
	sdk.HandleVoid(bus, "desktop.request", func(conn *sdk.Conn, _ string, _ struct{}) error {
		sendDesktopConfig(conn)
		// Resend the banner facts too so a late-connecting shell (or
		// a tab refresh that fires desktop.request on mount) gets a
		// full state, not just the config.
		sendSystemInfo(conn)
		return nil
	})
}

// sendSystemInfo ships the hostname / user / hardware / IP block the
// desktop banner displays. Fire-and-forget — the FE re-renders on
// receipt and ignores absence (banner falls back to "wash").
func sendSystemInfo(c *sdk.Conn) {
	info := gatherSysInfo()
	log.Printf("wash-session sysinfo: %s", sysInfoString(info))
	// Interfaces ship as [{name, ips:[…]}] so the banner can label
	// each address by the interface it lives on. The FE's
	// SystemInfoMsg type mirrors the field name.
	ifaces := make([]map[string]any, 0, len(info.Interfaces))
	for _, g := range info.Interfaces {
		ifaces = append(ifaces, map[string]any{
			"name": g.Name,
			"ips":  g.IPs,
		})
	}
	router := map[string]any{"version": info.Router.Version}
	if info.Router.Commit != "" {
		router["commit"] = info.Router.Commit
	}
	if info.Router.Built != "" {
		router["built"] = info.Router.Built
	}
	if info.Router.Dev {
		router["dev"] = true
	}
	msg := map[string]any{
		"kind":       "system.info",
		"hostname":   info.Hostname,
		"fqdn":       info.FQDN,
		"username":   info.Username,
		"cpus":       info.CPUs,
		"mem_bytes":  info.MemBytes,
		"interfaces": ifaces,
		"router":     router,
	}
	if err := c.SendAppMsg(msg); err != nil {
		log.Printf("wash-session: send system.info: %v", err)
	}
}

// washIcon — Lucide sprite symbol name. Session is surface=desktop
// so it's filtered from the launcher catalog anyway; this is only
// surfaced in --show-hidden / debug paths.
const washIcon = "layout-dashboard"

// onAppMsg interprets the FE's launcher click + desktop-config
// requests. data is the JSON the FE sent via app_msg.send, decoded
// into map[string]any.
//
//   {"action":"launch","app_id":"…"}        — spawn an app
//   {"action":"spawn_root","app_id":"…"}    — ask wash-priv to spawn
//                                              an app as root
//   {"kind":"desktop.request"}              — re-ship desktop.config
// onAppMsg handles the action-keyed launcher messages — these
// predate the bus and use `action` instead of `kind`. The bus
// dispatches kind-shaped messages first; this is the fallback.
func onAppMsg(c *sdk.Conn, _ uint32, data any) {
	m := sdk.AsMap(data)
	if m == nil {
		return
	}
	action, _ := m["action"].(string)
	switch action {
	case "launch":
		appID, _ := m["app_id"].(string)
		if appID == "" {
			return
		}
		if err := c.SpawnRequest(appID); err != nil {
			log.Printf("wash-session: spawn.request %s: %v", appID, err)
		}
	case "spawn_root":
		// The session app routes "spawn as root" requests through
		// wash-priv. wash-priv's queue UI shows com.wash.session as
		// the (router-attested) sender, plus the user-typed reason.
		// We never run sudo ourselves — that's wash-priv's job.
		//
		// Args come from the FE payload, sourced from the source app's
		// manifest.root_variant.args (e.g. wash-term ships ["--login"]).
		// Nothing here is per-app special-cased anymore — the launcher
		// FE is the single source of truth for which apps have a root
		// variant and what argv they prefer.
		appID, _ := m["app_id"].(string)
		if appID == "" {
			return
		}
		args := sdk.ToStringSlice(m["args"])
		reqID := newReqID()
		payload := map[string]any{
			"kind":   "spawn",
			"req_id": reqID,
			"app_id": appID,
			"reason": "session menu",
		}
		if len(args) > 0 {
			payload["args"] = args
		}
		if err := c.SendAppMsgTo(wire.Recipient{AppID: "com.wash.priv"}, payload); err != nil {
			log.Printf("wash-session: spawn_root %s: %v", appID, err)
		}
	}
}

// newReqID is a tiny correlation id for cross-app spawn_root.
// Format is opaque to wash-priv; we use it only in our own logs.
func newReqID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "ss-" + hex.EncodeToString(b[:])
}
