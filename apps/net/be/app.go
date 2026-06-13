// Package net is wash-net (com.wash.net) — the windowed network UI app
// (docs/NET.md §2.11, §3). It is the UNPRIVILEGED half of the pair: it embeds
// the apps/net/fe bundle (the generated Advanced editor + bespoke screens) and
// serves it to the browser like any wash app, then relays the FE's
// validate/diff/apply/confirm/revert requests to the privileged com.wash.netd
// service over cross-app app_msg. The router stamps these forwards with this
// app's attested identity, which is how netd authorizes them (netd.authz).
//
// This process holds no privilege and no network logic — it is a typed proxy.
// All the model/validate/apply logic lives in com.wash.netd (and the pure
// internal/washnet library it links). Keeping the FE-facing app unprivileged is
// the whole point of the two-app split (§3 privilege boundary).
//
// FE↔BE wire (own-FE request/reply, correlated by `id`):
//
//	→ {kind:"current",  id}               ← {kind:"current_ok", id, config, caps, devices}
//	→ {kind:"validate", id, config:{…}}   ← {kind:"validate_ok", id, diagnostics:[…]}
//	→ {kind:"diff",     id, config:{…}}   ← {kind:"diff_ok", id, entries, summary}
//	→ {kind:"apply",    id, config:{…}}   ← {kind:"apply_ok", id, state, events, …}
//	→ {kind:"confirm",  id}               ← {kind:"confirm_ok", id, state}
//	→ {kind:"revert",   id}               ← {kind:"revert_ok", id, state}
//
// Each FE request is relayed verbatim to netd; netd's reply is relayed back with
// the FE's id echoed. `config` is the FE interchange JSON (codec, §2.11).
package net

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.8.0"

// AppID is this app's id. NetdAppID is the privileged service it relays to —
// declared locally (not imported) so the two apps stay independently buildable
// and the wire contract is owned on each side.
const (
	AppID     = "com.wash.net"
	NetdAppID = "com.wash.netd"
)

// netIcon is the lucide "network" glyph — the shell renders an app's icon as a
// sprite id (window.tsx: <use href="icons.svg#<icon>">), the same glyph the
// session sidebar's Network section uses, so app/launcher/taskbar/sidebar all
// match. (A data: URI doesn't work here — it'd become icons.svg#data:… and
// render nothing; every other app passes a bare lucide name like "radio".)
const netIcon = "network"

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Printf("wash-net: assets sub: %v", err)
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Network",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-net",
			Surface:         sdk.SurfaceWindow,
			Icon:            netIcon,
			Accent:          "#6090e0",
			// Listed in the launcher catalog (network glyph) alongside the
			// sidebar Wi-Fi affordance and the Settings → Network pane, which
			// also spawn it via spawn.request.
			Instancing: sdk.InstancingSingle,
			Window:     &sdk.WindowHints{DefaultWidth: 574, DefaultHeight: 620},
		},
		Assets:  sub,
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-net",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-net ready instance=%s window=%d", instanceID, windowID)
	bus := sdk.NewBus(c)
	// Transparent relay: every FE request kind is forwarded verbatim to netd as a
	// request/reply round-trip (correlated by the FE's id). net owns no FE-message
	// logic of its own — it's a pure typed pipe whose whole job is to carry the
	// FE's calls under this app's attested identity (the router stamps the
	// forward; netd's authz trusts com.wash.net). A catch-all pattern ("" matches
	// every kind) instead of a hand-maintained allowlist means new netd messages
	// need ZERO changes here and can't silently drift out of sync — the bug that
	// left the wifi kinds (scan/radio/connect/forget) unrelayed and the scan dead.
	//
	// Forwarding arbitrary FE-named kinds to the privileged service is safe: netd
	// authorizes the sender and only acts on kinds it explicitly handles; anything
	// else is a not-handled error, never a privileged effect. Mirrors wash-edit's
	// cmd.* passthrough (the other HandlePattern user). Patterns fire only for
	// own-FE messages, so netd's cross-app "state" push below is unaffected.
	bus.HandlePattern("", func(conn *sdk.Conn, kind string, req map[string]any) {
		relayToNetd(bus, conn, kind, req)
	})
	// Subscribe to netd's status so the window sees autonomous transitions (the
	// commit-confirm auto-revert) and the apply-event stream — relayed to the FE
	// as net.state, the same shape the sidebar gateway uses. Without this the
	// window would only learn outcomes it explicitly requested.
	sdk.HandleFromVoid(bus, "state", func(conn *sdk.Conn, _ string, req stateRelay, from wire.Sender) error {
		if from.AppID != NetdAppID {
			return nil
		}
		return conn.SendAppMsg(map[string]any{"kind": "net.state", "state": req.State})
	})
	if err := c.SendAppMsgTo(wire.Recipient{AppID: NetdAppID}, map[string]any{"kind": "subscribe"}); err != nil {
		log.Printf("wash-net: subscribe netd: %v", err)
	}
}

// stateRelay is the opaque netd StateService push we forward verbatim to the FE.
type stateRelay struct {
	State any `json:"state"`
}

// relayToNetd carries one FE request through a cross-app round-trip with netd.
// It runs in a goroutine because sdk.Call blocks awaiting netd's reply and MUST
// NOT run on the dispatch goroutine (it would deadlock the read loop), so the
// relay ships the reply to the FE itself rather than returning it.
func relayToNetd(bus *sdk.Bus, c *sdk.Conn, kind string, req map[string]any) {
	id, _ := req["id"].(string)
	// Forward the request payload verbatim minus the FE envelope (kind/id):
	// `config` for the config flow, `on`/`ssid`/`security`/`psk`/`hidden` for the
	// wifi kinds, nothing for confirm/revert. netd decodes into its typed request
	// and ignores extras.
	fwd := map[string]any{}
	for k, v := range req {
		if k == "kind" || k == "id" {
			continue
		}
		fwd[k] = v
	}
	go func() {
		var resp map[string]any
		err := sdk.Call(context.Background(), bus, wire.Recipient{AppID: NetdAppID}, kind, fwd, &resp)
		if err != nil {
			_ = c.SendAppMsg(errReply(kind, id, err))
			return
		}
		if resp == nil {
			resp = map[string]any{}
		}
		// Relay netd's reply to the FE: rebrand to <kind>_ok, echo the FE's
		// id, and drop netd's hop-level req_id.
		resp["kind"] = kind + "_ok"
		if id != "" {
			resp["id"] = id
		}
		delete(resp, "req_id")
		_ = c.SendAppMsg(resp)
	}()
}

func errReply(kind, id string, err error) map[string]any {
	code, msg := sdk.ErrInternal, err.Error()
	var e sdk.Err
	if errors.As(err, &e) {
		code, msg = e.Code, e.Msg
	}
	r := map[string]any{"kind": kind + "_err", "code": code, "msg": msg}
	if id != "" {
		r["id"] = id
	}
	return r
}
