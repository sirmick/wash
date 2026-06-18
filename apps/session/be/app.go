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

const version = "0.9.1"

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
	startHostStats(c)
	bus := sdk.NewBus(c)
	sdk.HandleVoid(bus, "desktop.request", func(conn *sdk.Conn, _ string, _ struct{}) error {
		sendDesktopConfig(conn)
		// Resend the banner facts too so a late-connecting shell (or
		// a tab refresh that fires desktop.request on mount) gets a
		// full state, not just the config.
		sendSystemInfo(conn)
		return nil
	})
	// save_state mirrors the wash-edit / wash-fm pattern: the FE
	// ships its persistable blob (sidebar mode, per-section state)
	// and we hand it to the SDK's router-side persistence. On every
	// (re)mount the shell dispatches the blob back as a wash:state
	// event so the FE can restore.
	sdk.HandleVoid(bus, "save_state", func(conn *sdk.Conn, _ string, req sessionStateReq) error {
		return conn.SaveState(req.State)
	})

	// Sidebar widget gateway: the FE can't address other apps with a
	// trustworthy sender (shell-originated cross-app msgs lack a
	// router-attested From), so every subscribe / control op to a
	// background service goes through here. Session BE does the
	// cross-app send; the router stamps the session instance as
	// sender; the service replies cross-app to this BE; we forward
	// to our FE under a service-specific kind so the wash:msg dispatch
	// stays unambiguous.
	registerNotifyGateway(bus)
	registerBulkGateway(bus)
	registerPrivGateway(bus)
	registerNetGateway(bus)
	registerAudioGateway(bus)
	registerRemoteGateway(bus)
	// state forwarder: notify, bulk, and priv all push
	// {kind:"state", state:...} cross-app. Branch on the sender's
	// AppID to re-brand for the FE under a service-specific kind.
	sdk.HandleFromVoid(bus, "state", func(conn *sdk.Conn, _ string, req serviceStateMsg, from wire.Sender) error {
		feKind := serviceFEKind(from.AppID)
		if feKind == "" {
			return nil
		}
		return conn.SendAppMsg(map[string]any{
			"kind":  feKind,
			"state": req.State,
		})
	})
	// priv produces additional non-state events (need_password,
	// unlock_err, unlocked, locked, req.new, req.update) that the
	// password modal + queue widget consume directly. Re-brand each
	// to a priv-prefixed kind so the FE's dispatcher stays unambiguous
	// across services.
	for _, evt := range []string{"need_password", "unlock_err", "unlocked", "locked", "req.new", "req.update"} {
		registerPrivPassthrough(bus, evt)
	}
}

// registerPrivPassthrough installs a HandleFromVoid that forwards a
// single priv event to the session FE under a priv-prefixed kind.
// The payload is forwarded verbatim (minus the kind field, which we
// rewrite). Kept as a helper so the loop in onReady stays readable
// even as more priv events are added.
func registerPrivPassthrough(bus *sdk.Bus, kind string) {
	sdk.HandleFromVoid(bus, kind, func(conn *sdk.Conn, _ string, req map[string]any, from wire.Sender) error {
		if from.AppID != PrivAppID {
			return nil
		}
		// Strip wire-meta keys so the FE-bound envelope is clean.
		delete(req, "kind")
		out := map[string]any{"kind": "priv." + kind}
		for k, v := range req {
			out[k] = v
		}
		return conn.SendAppMsg(out)
	})
}

// Reserved app ids for the background services we gateway. Duplicated
// here (vs. imported) so the session BE has no compile-time
// dependency on the service packages; the contract is the app-id
// string, which is the same trust boundary either way.
const (
	NotifyAppID = "com.wash.notify"
	BulkAppID   = "com.wash.bulk"
	PrivAppID   = "com.wash.priv"
	NetdAppID   = "com.wash.netd"
	AudioAppID  = "com.wash.audio"
	RemoteAppID = "com.wash.remote"
)

// serviceFEKind maps a service app id to the FE-side kind we
// re-brand its `state` push to. Empty string = ignore (state from a
// non-gatewayed app — we don't speak for it).
func serviceFEKind(appID string) string {
	switch appID {
	case NotifyAppID:
		return "notify.state"
	case BulkAppID:
		return "bulk.state"
	case PrivAppID:
		return "priv.state"
	case NetdAppID:
		return "net.state"
	case AudioAppID:
		return "audio.state"
	case RemoteAppID:
		return "remote.state"
	}
	return ""
}

// registerNetGateway forwards the sidebar's subscribe/unsubscribe to the
// com.wash.netd networking service (docs/NET.md §2.11, B1d). netd's status
// pushes return as {kind:"state"} and are re-branded to "net.state" by the
// shared state forwarder (serviceFEKind). The sidebar widget's "configure"
// click launches com.wash.net via the existing launcher path, not here.
func registerNetGateway(bus *sdk.Bus) {
	sdk.HandleVoid(bus, "net_subscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: NetdAppID}, map[string]any{"kind": "subscribe"})
	})
	sdk.HandleVoid(bus, "net_unsubscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: NetdAppID}, map[string]any{"kind": "unsubscribe"})
	})
}

// registerAudioGateway forwards the sidebar's subscribe/control ops to
// the com.wash.audio control plane (docs/AUDIO.md §3). State pushes
// return as {kind:"state"} and are re-branded to "audio.state" by the
// shared state forwarder. control addresses one source by id; the
// service relays the transport command to the owning producer.
func registerAudioGateway(bus *sdk.Bus) {
	sdk.HandleVoid(bus, "audio_subscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: AudioAppID}, map[string]any{"kind": "subscribe"})
	})
	sdk.HandleVoid(bus, "audio_unsubscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: AudioAppID}, map[string]any{"kind": "unsubscribe"})
	})
	sdk.HandleVoid(bus, "audio_control", func(conn *sdk.Conn, _ string, req audioControlReq) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: AudioAppID}, map[string]any{
			"kind": "control", "id": req.ID, "action": req.Action,
		})
	})
	sdk.HandleVoid(bus, "audio_set_master_volume", func(conn *sdk.Conn, _ string, req audioVolReq) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: AudioAppID}, map[string]any{
			"kind": "set_master_volume", "value": req.Value,
		})
	})
}

// registerRemoteGateway forwards the Hosts widget's subscribe + connect/
// disconnect to the com.wash.remote service (docs/REMOTE.md R2). Its
// per-host status pushes return as {kind:"state"} and are re-branded to
// "remote.state" by the shared state forwarder; the FE then attaches the
// reported local endpoint as a second RouterClient.
func registerRemoteGateway(bus *sdk.Bus) {
	sdk.HandleVoid(bus, "remote_subscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: RemoteAppID}, map[string]any{"kind": "subscribe"})
	})
	sdk.HandleVoid(bus, "remote_unsubscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: RemoteAppID}, map[string]any{"kind": "unsubscribe"})
	})
	sdk.HandleVoid(bus, "remote_connect", func(conn *sdk.Conn, _ string, req remoteHostReq) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: RemoteAppID}, map[string]any{
			"kind": "connect", "host": req.Host, "remote_port": req.RemotePort,
		})
	})
	sdk.HandleVoid(bus, "remote_disconnect", func(conn *sdk.Conn, _ string, req remoteHostReq) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: RemoteAppID}, map[string]any{
			"kind": "disconnect", "host": req.Host,
		})
	})
}

type remoteHostReq struct {
	Host       string `json:"host"`
	RemotePort int    `json:"remote_port"`
}

func registerNotifyGateway(bus *sdk.Bus) {
	sdk.HandleVoid(bus, "notify_subscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: NotifyAppID}, map[string]any{"kind": "subscribe"})
	})
	sdk.HandleVoid(bus, "notify_unsubscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: NotifyAppID}, map[string]any{"kind": "unsubscribe"})
	})
	sdk.HandleVoid(bus, "notify_mark_read", func(conn *sdk.Conn, _ string, req idReq) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: NotifyAppID}, map[string]any{"kind": "mark_read", "id": req.ID})
	})
	sdk.HandleVoid(bus, "notify_clear_all", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: NotifyAppID}, map[string]any{"kind": "clear_all"})
	})
}

func registerBulkGateway(bus *sdk.Bus) {
	sdk.HandleVoid(bus, "bulk_subscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: BulkAppID}, map[string]any{"kind": "subscribe"})
	})
	sdk.HandleVoid(bus, "bulk_unsubscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: BulkAppID}, map[string]any{"kind": "unsubscribe"})
	})
	sdk.HandleVoid(bus, "bulk_cancel", func(conn *sdk.Conn, _ string, req bulkCancelReq) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: BulkAppID}, map[string]any{"kind": "cancel", "job_id": req.JobID})
	})
	sdk.HandleVoid(bus, "bulk_resolve_conflict", func(conn *sdk.Conn, _ string, req bulkResolveConflictReq) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: BulkAppID}, map[string]any{
			"kind":   "resolve_conflict",
			"job_id": req.JobID,
			"action": req.Action,
		})
	})
}

func registerPrivGateway(bus *sdk.Bus) {
	sdk.HandleVoid(bus, "priv_subscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: PrivAppID}, map[string]any{"kind": "subscribe"})
	})
	sdk.HandleVoid(bus, "priv_unsubscribe", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: PrivAppID}, map[string]any{"kind": "unsubscribe"})
	})
	sdk.HandleVoid(bus, "priv_approve", func(conn *sdk.Conn, _ string, req privReqIDReq) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: PrivAppID}, map[string]any{"kind": "approve", "req_id": req.ReqID})
	})
	sdk.HandleVoid(bus, "priv_reject", func(conn *sdk.Conn, _ string, req privRejectReq) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: PrivAppID}, map[string]any{
			"kind":   "reject",
			"req_id": req.ReqID,
			"reason": req.Reason,
		})
	})
	sdk.HandleVoid(bus, "priv_lock", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: PrivAppID}, map[string]any{"kind": "lock"})
	})
	// priv_unlock carries the noble-encrypted password handshake (FE
	// pubkey + ciphertext + nonce, all base64 strings) — forward
	// verbatim. The session BE never sees plaintext; the priv BE
	// alone decrypts.
	sdk.HandleVoid(bus, "priv_unlock", func(conn *sdk.Conn, _ string, req privUnlockReq) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: PrivAppID}, map[string]any{
			"kind":       "unlock",
			"ciphertext": req.Ciphertext,
			"fe_pubkey":  req.FEPubKey,
			"nonce":      req.Nonce,
		})
	})
}

// sessionStateReq is the wire envelope for the FE's persistence push.
// State is opaque (the schema is FE-owned); the SDK marshals it to
// JSON for router storage.
type sessionStateReq struct {
	State any `json:"state"`
}

// serviceStateMsg captures the `state` field of a StateService push.
// The body is opaque (each service has its own shape); we forward
// verbatim under a re-branded kind.
type serviceStateMsg struct {
	State any `json:"state"`
}

// idReq / bulkCancelReq / bulkResolveConflictReq are the FE-bound
// wire shapes for the gateway forwarders. Inlining them as anonymous
// struct types in the handler signatures would make the json tags
// less greppable; pulling them out makes the contract obvious.
type idReq struct {
	ID string `json:"id"`
}

type bulkCancelReq struct {
	JobID string `json:"job_id"`
}

type bulkResolveConflictReq struct {
	JobID  string `json:"job_id"`
	Action string `json:"action"`
}

type audioControlReq struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

type audioVolReq struct {
	Value float64 `json:"value"`
}

type privReqIDReq struct {
	ReqID string `json:"req_id"`
}

type privRejectReq struct {
	ReqID  string `json:"req_id"`
	Reason string `json:"reason"`
}

// privUnlockReq carries the ECDH/HKDF/AES-GCM handshake bytes from
// the session FE's password modal. All fields are base64 strings —
// the priv BE base64-decodes before processing. Forwarded verbatim;
// the session BE never sees plaintext.
type privUnlockReq struct {
	Ciphertext string `json:"ciphertext"`
	FEPubKey   string `json:"fe_pubkey"`
	Nonce      string `json:"nonce"`
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
	if info.SessionName != "" {
		msg["session_name"] = info.SessionName
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
//	{"action":"launch","app_id":"…"}        — spawn an app
//	{"action":"spawn_root","app_id":"…"}    — ask wash-priv to spawn
//	                                           an app as root
//	{"kind":"desktop.request"}              — re-ship desktop.config
//
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
		// No app is special-cased here — the launcher FE is the single
		// source of truth for which apps have a root variant and what
		// argv they prefer.
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
