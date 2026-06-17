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
// External wire (cross-app, via app_msg.send with to={AppID:"com.wash.priv"}):
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
package priv

import (
	"context"
	"log"
	"os"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

const version = "0.9.1"

// AppID is the reserved id this app claims. The registry refuses any
// non-trusted binary from serving this id (see internal/router/
// registry.go reservedIDs), so a malicious local wash app cannot
// shadow the real wash-priv to inherit its trust signal.
const AppID = "com.wash.priv"

var (
	st  *State
	cfg Config
	def *sdk.AppDef
)

func init() {
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Privileged Actions",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Surface:         sdk.SurfaceBackground,
			Instancing:      sdk.InstancingSingleton,
			Capabilities:    []string{sdk.CapPrepareSpawn},
		},
		OnReady:              onReady,
		OnPrepareSpawnResult: onPrepareSpawnResult,
	}
	registry.Register(&registry.App{
		Name:     "wash-priv",
		Manifest: def.Manifest,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call. The
// shim still runs the same startup sequence (config + hardening +
// state) before sdk.Main; the dispatched-via-multicall path uses
// run() which mirrors it. Keeping both paths converge here lets
// the standalone binary remain a thin sdk.Main wrapper while the
// multi-call dispatcher gets a self-contained Run.
func Def() *sdk.AppDef {
	startup()
	return def
}

// run is the multi-call entrypoint. Runs the Connect/Run/Close
// sequence via sdk.Run with the populated def, plus the cleanup
// scrub on a clean exit.
func run(ctx context.Context) error {
	startup()
	err := sdk.Run(ctx, def)
	// Best-effort cache scrub on a clean exit. Doesn't help against
	// signal-9 or panics, but it makes the happy-path memory window
	// as short as practical.
	if st != nil {
		st.WipePassword()
	}
	return err
}

// startup loads config, runs process-hardening best-effort, and
// constructs the State. Idempotent guard so a hypothetical second
// call (e.g. tests that import Def()) doesn't reload state.
func startup() {
	if st != nil {
		return
	}
	cfg = LoadConfig()
	// Process hardening: best-effort because some configurations
	// (low RLIMIT_MEMLOCK on embedded, certain LSM profiles) reject
	// these and refusing to start would lock the user out of the
	// privilege escalation path entirely.
	applyHardening(log.Printf)
	st = NewState()
	switch {
	case st.isRoot:
		log.Printf("wash-priv: running as euid=0 — auto-approve (no sudo, no password modal, no Approve click)")
	case st.passwordless:
		log.Printf("wash-priv: sudo is passwordless for this user — skip password modal (sudo -n), Approve click still required")
	}
}

// onReady fires after the SDK handshake completes. We seed the state
// with the live conn and start the idle ticker; the FE will send a
// hello soon after mount and pick up the full state then.
func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-priv ready instance=%s window=%d", instanceID, windowID)
	st.AttachConn(c)
	bus := sdk.NewBus(c)
	registerHandlers(bus)
	go st.IdleTicker(cfg.IdleTimeout)
}

// ----- bus types -----
//
// wash-priv doesn't return _ok/_err replies — the state code in
// queue.go produces domain-specific event kinds ({kind:"spawned"},
// {kind:"result"}, {kind:"rejected"}, {kind:"req.update"}, …) via
// broadcasts to subscribers + cross-app SendAppMsgTo replies. So
// every handler here is HandleFromVoid; the bus is just a typed
// dispatcher onto the State methods. All FE-bound control flows
// through the session BE gateway, which subscribes here and forwards
// to its own FE — so we never see own-FE messages directly.

type approveReq struct {
	ReqID string `json:"req_id"`
}

type rejectReq struct {
	ReqID  string `json:"req_id"`
	Reason string `json:"reason"`
}

type unlockReq struct {
	Ciphertext any `json:"ciphertext"`
	FEPubKey   any `json:"fe_pubkey"`
	Nonce      any `json:"nonce"`
}

type runReq struct {
	Argv   []string `json:"argv"`
	Reason string   `json:"reason"`
}

type spawnReq struct {
	AppID  string   `json:"app_id"`
	Args   []string `json:"args"`
	Reason string   `json:"reason"`
}

type runInlineReq struct {
	Argv      []string          `json:"argv"`
	Cwd       string            `json:"cwd"`
	Reason    string            `json:"reason"`
	NoPrompt  bool              `json:"no_prompt"`
	Env       map[string]string `json:"env"`
	CliOrigin any               `json:"cli_origin"`
	// Pty=true allocates a PTY pair for the child instead of plain
	// pipes. Output still flows back as priv.stream messages (base64
	// bytes); the requester's xterm-class widget interprets the
	// terminal escapes. Use this when the wrapped command needs
	// real terminal semantics (progress bars, color, interactive
	// prompts) — apt-get is the canonical case.
	Pty  bool   `json:"pty"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

type resizeReq struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

type stdinReq struct {
	Bytes any `json:"bytes"` // base64 string
}

type emptyReq struct{}

func registerHandlers(b *sdk.Bus) {
	// ----- cross-app subscribe/control (FE→session BE→here) -----
	//
	// Every FE-bound interaction comes through the session BE
	// gateway: it subscribes here and forwards approve / reject /
	// unlock / lock cross-app. router-attested From identifies the
	// session instance; we don't otherwise care who's calling.
	sdk.HandleFromVoid(b, "subscribe", func(c *sdk.Conn, _ string, _ emptyReq, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		st.addSubscriber(c, from.InstanceID)
		return nil
	})
	sdk.HandleFromVoid(b, "unsubscribe", func(_ *sdk.Conn, _ string, _ emptyReq, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		st.removeSubscriber(from.InstanceID)
		return nil
	})
	// approve/reject/unlock/lock are HandleVoid (sender-agnostic) so
	// the session BE gateway, the control-socket-driven e2e tests,
	// and any future trusted caller can all drive them without a
	// router-attested From. v1's trust model considers every FE
	// (FE-originated cross-app sends lack From by design) trusted;
	// the BE-level guard is the registry's reserved-id check at
	// internal/router/registry.go:162, which still gates who can
	// claim com.wash.priv at all.
	sdk.HandleVoid(b, "approve", func(c *sdk.Conn, _ string, req approveReq) error {
		st.HandleApprove(c, req.ReqID)
		return nil
	})
	sdk.HandleVoid(b, "reject", func(c *sdk.Conn, _ string, req rejectReq) error {
		st.HandleReject(c, req.ReqID, req.Reason)
		return nil
	})
	sdk.HandleVoid(b, "unlock", func(c *sdk.Conn, _ string, req unlockReq) error {
		st.HandleUnlock(c,
			sdk.DecodeBase64(req.Ciphertext),
			sdk.DecodeBase64(req.FEPubKey),
			sdk.DecodeBase64(req.Nonce))
		return nil
	})
	sdk.HandleVoid(b, "lock", func(c *sdk.Conn, _ string, _ emptyReq) error {
		st.HandleLock(c, "explicit")
		return nil
	})

	// ----- cross-app spawn/run requests (from producers) -----
	sdk.HandleFromVoid(b, "run", func(c *sdk.Conn, reqID string, req runReq, from wire.Sender) error {
		if reqID == "" {
			log.Printf("wash-priv: run without req_id from %s/%s", from.AppID, from.InstanceID)
			return nil
		}
		st.EnqueueRun(c, from, reqID, req.Argv, req.Reason)
		return nil
	})
	sdk.HandleFromVoid(b, "spawn", func(c *sdk.Conn, reqID string, req spawnReq, from wire.Sender) error {
		if reqID == "" {
			log.Printf("wash-priv: spawn without req_id from %s/%s", from.AppID, from.InstanceID)
			return nil
		}
		st.EnqueueSpawn(c, from, reqID, req.AppID, req.Args, req.Reason)
		return nil
	})
	sdk.HandleFromVoid(b, "run_inline", func(c *sdk.Conn, reqID string, req runInlineReq, from wire.Sender) error {
		if reqID == "" {
			log.Printf("wash-priv: run_inline without req_id from %s/%s", from.AppID, from.InstanceID)
			return nil
		}
		origin := parseCliOrigin(req.CliOrigin)
		st.EnqueueRunInline(c, from, reqID, req.Argv, req.Cwd, req.Env, req.Reason, req.NoPrompt, origin, req.Pty, req.Cols, req.Rows)
		return nil
	})
	sdk.HandleFromVoid(b, "resize", func(_ *sdk.Conn, reqID string, req resizeReq, _ wire.Sender) error {
		if reqID == "" {
			return nil
		}
		st.HandleInlinePtyResize(reqID, req.Cols, req.Rows)
		return nil
	})
	sdk.HandleFromVoid(b, "stdin", func(_ *sdk.Conn, reqID string, req stdinReq, _ wire.Sender) error {
		if reqID == "" {
			return nil
		}
		b := sdk.DecodeBase64(req.Bytes)
		if len(b) > 0 {
			st.HandleInlineStdin(reqID, b)
		}
		return nil
	})
	sdk.HandleFromVoid(b, "stdin_close", func(_ *sdk.Conn, reqID string, _ emptyReq, _ wire.Sender) error {
		if reqID == "" {
			return nil
		}
		st.HandleInlineStdinClose(reqID)
		return nil
	})
	// cancel and cli.disconnect both terminate an in-flight inline run.
	for _, kind := range []string{"cancel", "cli.disconnect"} {
		sdk.HandleFromVoid(b, kind, func(_ *sdk.Conn, reqID string, _ emptyReq, _ wire.Sender) error {
			if reqID == "" {
				return nil
			}
			st.HandleInlineCancel(reqID)
			return nil
		})
	}
}

// parseCliOrigin pulls the router-attested pid/uid/comm/tty fields
// out of the cli_origin sub-map. Returns nil when the message
// didn't carry one (i.e. the sender wasn't a cli session). All
// fields are best-effort — a missing tty just shows blank.
func parseCliOrigin(v any) *CliOrigin {
	m := sdk.AsMap(v)
	if m == nil {
		return nil
	}
	return &CliOrigin{
		PID:  sdk.ToInt64(m["pid"]),
		UID:  sdk.ToInt64(m["uid"]),
		Comm: sdk.ToString(m["comm"]),
		TTY:  sdk.ToString(m["tty"]),
	}
}

// onPrepareSpawnResult dispatches to the per-req handler that called
// PrepareSpawn. The State indexes pending prepare-spawn callbacks by
// the req_id we passed.
func onPrepareSpawnResult(c *sdk.Conn, reqID uint64, instanceID, token, binary string, err error) {
	st.HandlePrepareSpawnResult(c, reqID, instanceID, token, binary, err)
}

// fatalUsage is called for programmer-error config problems where
// starting up half-configured would be misleading.
func fatalUsage(format string, args ...any) {
	log.Printf("wash-priv: "+format, args...)
	os.Exit(1)
}
