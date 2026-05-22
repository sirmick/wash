// wash-priv — privilege primitive.
//
// Other wash apps ask wash-priv to launch a registered binary as
// root. wash-priv shows the request in a queue UI; the user approves
// or rejects. The cached sudo password is held in BE memory for the
// session (cleared by Lock, 15-min idle, or browser refresh) and
// piped into sudo on each approved spawn — "individually run through
// sudo" semantically, no shared sudo credential cache.
//
// Trust shape: wash-priv is the single point where a sudo password
// lives in process memory. Everything else routes through it. Its
// own window wears the red ROOT stripe (via the reserved-id rule in
// the router) because compromising wash-priv compromises root.
//
// External wire (cross-app, via app_msg.send.to {AppID:"com.wash.priv"}):
//
//   →  {kind:"run",   req_id, argv, reason?}      # sugar
//   →  {kind:"spawn", req_id, app_id, args?, reason?}
//   ←  {kind:"spawned",  req_id, instance_id}
//   ←  {kind:"result",   req_id, exit_code}
//   ←  {kind:"rejected", req_id, reason}
//   ←  {kind:"error",    req_id, code, msg}
//
// Internal wire (FE↔BE):
//   FE→BE  hello, approve, reject, unlock, lock, resync
//   BE→FE  state, req.new, req.update, unlocked, unlock_err, locked
//
// See AGENTS/ARCHITECTURE for the broader picture. The mock-sudo
// hook (env WASH_PRIV_SUDO_BIN) is documented at runSudo.
package main

import (
	"embed"
	"io/fs"
	"log"
	"os"

	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.0.0"

// AppID is the reserved id this app claims. The registry refuses any
// non-trusted binary from serving this id (see internal/router/
// registry.go reservedIDs), so a malicious local wash app cannot
// shadow the real wash-priv to inherit its red-stripe trust signal.
const AppID = "com.wash.priv"

// privIcon — lucide sprite name. shield-check signals "protected /
// gateway" without looking like a generic warning.
const privIcon = "shield-check"

var (
	st  *State
	cfg Config
)

func main() {
	cfg = LoadConfig()

	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("wash-priv: assets: %v", err)
	}

	// Process hardening: kept best-effort because some configurations
	// (low RLIMIT_MEMLOCK on embedded, certain LSM profiles) reject
	// these and refusing to start would lock the user out of the
	// privilege escalation path entirely. We log on failure.
	applyHardening(log.Printf)

	st = NewState()

	sdk.Main(&sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Privileged Actions",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-priv",
			Surface:         sdk.SurfaceWindow,
			Icon:            privIcon,
			Instancing:      sdk.InstancingSingleton,
			Capabilities:    []string{sdk.CapPrepareSpawn},
			Window:          &sdk.WindowHints{DefaultWidth: 520, DefaultHeight: 420},
		},
		Assets:               sub,
		OnReady:              onReady,
		OnAppMsg:             onAppMsg,
		OnAppMsgFrom:         onAppMsgFrom,
		OnPrepareSpawnResult: onPrepareSpawnResult,
	})

	// Best-effort cache scrub on a clean exit. Doesn't help against
	// signal-9 or panics, but it makes the happy-path memory window
	// as short as practical.
	st.WipePassword()
}

// onReady fires after the SDK handshake completes. We seed the state
// with the live conn and start the idle ticker; the FE will send a
// hello soon after mount and pick up the full state then.
func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-priv ready instance=%s window=%d", instanceID, windowID)
	st.AttachConn(c)
	go st.IdleTicker(cfg.IdleTimeout)
}

// onAppMsg handles own-FE messages. Cross-app requests land in
// onAppMsgFrom instead — we keep the two paths separate so the FE
// can never impersonate a requester.
func onAppMsg(c *sdk.Conn, _ uint32, data any) {
	m, ok := data.(map[any]any)
	if !ok {
		return
	}
	kind, _ := m["kind"].(string)
	switch kind {
	case "hello":
		nonce, _ := m["page_nonce"].(string)
		st.HandleHello(c, nonce)
	case "resync":
		st.SendStateSnapshot(c)
	case "approve":
		st.HandleApprove(c, toString(m["req_id"]))
	case "reject":
		st.HandleReject(c, toString(m["req_id"]), toString(m["reason"]))
	case "unlock":
		ct := decodeBase64(m["ciphertext"])
		pk := decodeBase64(m["fe_pubkey"])
		nonce := decodeBase64(m["nonce"])
		st.HandleUnlock(c, ct, pk, nonce)
	case "lock":
		st.HandleLock(c, "explicit")
	}
}

// onAppMsgFrom is the cross-app entry point. The router has stamped
// `from` with the sender's instance — never trust anything the
// payload claims about origin.
func onAppMsgFrom(c *sdk.Conn, _ uint32, data any, from wire.Sender) {
	m, ok := data.(map[any]any)
	if !ok {
		return
	}
	kind, _ := m["kind"].(string)
	reqID := toString(m["req_id"])
	if reqID == "" {
		// We can't reply without a req_id (the requester wouldn't be
		// able to correlate the answer). Log and drop.
		log.Printf("wash-priv: cross-app msg without req_id from %s/%s", from.AppID, from.InstanceID)
		return
	}
	switch kind {
	case "run":
		argv := toStringSlice(m["argv"])
		reason := toString(m["reason"])
		st.EnqueueRun(c, from, reqID, argv, reason)
	case "spawn":
		appID := toString(m["app_id"])
		args := toStringSlice(m["args"])
		reason := toString(m["reason"])
		st.EnqueueSpawn(c, from, reqID, appID, args, reason)
	default:
		_ = c.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{
			"kind":   "error",
			"req_id": reqID,
			"code":   "bad_request",
			"msg":    "unknown kind " + kind,
		})
	}
}

// onPrepareSpawnResult dispatches to the per-req handler that called
// PrepareSpawn. The State indexes pending prepare-spawn callbacks by
// the req_id we passed.
func onPrepareSpawnResult(c *sdk.Conn, reqID uint64, instanceID, token, binary string, err error) {
	st.HandlePrepareSpawnResult(c, reqID, instanceID, token, binary, err)
}

// toString coerces any → string for CBOR-decoded payloads. Returns
// "" for missing / wrong-type values, matching the defensive style
// used elsewhere in the wash codebase.
func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// toStringSlice coerces a CBOR-decoded array-of-strings ([]any with
// string elements) into []string. Non-string elements are dropped.
func toStringSlice(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	}
	return nil
}

// fatalUsage is called for programmer-error config problems where
// starting up half-configured would be misleading.
func fatalUsage(format string, args ...any) {
	log.Printf("wash-priv: "+format, args...)
	os.Exit(1)
}
