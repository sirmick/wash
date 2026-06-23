package priv

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

// Request lifecycle. The state machine is deliberately minimal — the
// human is the planner and decides when something moves; we record
// transitions for the audit log and the FE update channel.
type Status string

const (
	StatusQueued   Status = "queued"   // awaiting user decision
	StatusRunning  Status = "running"  // approved, sudo in flight
	StatusDone     Status = "done"     // sudo exited (any exit code)
	StatusRejected Status = "rejected" // user denied
	StatusError    Status = "error"    // internal failure (no exec)
)

// Kind discriminates between {kind:"run"} sugar, {kind:"spawn"}
// general spawn-as-root, and {kind:"run_inline"} CLI-bridged inline
// exec. Kept on the request so the FE can render each clearly and
// the audit log preserves the distinction.
type Kind string

const (
	KindRun       Kind = "run"        // spawn wash-term --exec argv (window)
	KindSpawn     Kind = "spawn"      // spawn a registered wash app as root
	KindRunInline Kind = "run_inline" // sudo argv directly, stream io to wash-sudo
)

// Request is one approval-queue entry. Fields here are also what the
// audit log records and what the FE renders, so add carefully.
type Request struct {
	ReqID       string            // requester-chosen
	Kind        Kind              // run | spawn | run_inline
	Sender      wire.Sender       // router-attested
	AppID       string            // for KindSpawn: target app to launch
	Argv        []string          // for KindRun: command for wash-term --exec; for KindSpawn: positional args; for KindRunInline: argv to sudo
	Cwd         string            // for KindRunInline: working dir
	Env         map[string]string // for KindRunInline: caller-allowlisted env vars passed via sudo --preserve-env
	Reason      string            // freeform explanation, escape-stripped before FE render
	NoPrompt    bool              // KindRunInline: refuse if password cache is empty
	Pty         bool              // KindRunInline: allocate a PTY instead of pipes
	Cols        uint16            // KindRunInline+Pty: initial column count
	Rows        uint16            // KindRunInline+Pty: initial row count
	CliOrigin   *CliOrigin        // router-attested origin metadata (pid/uid/tty/comm) when sender is a cli-* session
	Status      Status
	CreatedAt   time.Time
	StartedAt   time.Time
	FinishedAt  time.Time
	ExitCode    int
	ErrorMsg    string
	SpawnedInst string // populated once a spawn-style child handshakes
}

// CliOrigin is the router-attested identity of a wash-sudo caller.
// PID + UID come from SO_PEERCRED; Comm + TTY are read by the router
// from /proc/<pid>/. The shape mirrors what the router writes into
// the payload's cli_origin field — wash-priv copies it verbatim.
type CliOrigin struct {
	PID  int64  `json:"pid"`
	UID  int64  `json:"uid"`
	Comm string `json:"comm"`
	TTY  string `json:"tty"`
}

// pendingPrepareSpawn is the in-flight book-keeping for a request
// that called PrepareSpawn and is waiting for the router's reply.
// Keyed by the uint64 req_id we sent on the wire (distinct from
// Request.ReqID which is requester-chosen).
type pendingPrepareSpawn struct {
	internalReqID string // Request.ReqID
	startedAt     time.Time
}

// State is the BE's full singleton state. There's exactly one of
// these (wash-priv is a singleton service). All shared state lives
// behind the mu mutex.
type State struct {
	mu sync.Mutex

	// conn is the SDK connection. Captured at OnReady; used for
	// outbound replies to requesters and FE updates. We never
	// reassign — singleton services have one conn for their life.
	conn *sdk.Conn

	queue []*Request // FIFO by CreatedAt; live + terminal entries pruned on lock

	// Crypto + password state.
	handshake    *Handshake // per-unlock, regenerated each time
	password     []byte     // cached; nil when locked
	lastActivity time.Time  // resets on every approve/unlock; idle timeout reads this

	// pendingApproveReq is the req_id whose Approve click triggered
	// the current need_password modal. On unlock, ONLY this request
	// runs — entering the password is the implicit approval for the
	// item that prompted it. Other items in the queue stay
	// StatusQueued and need their own Approve clicks. Cleared on
	// each unlock attempt (success or fail).
	pendingApproveReq string

	// subs is the set of subscriber instance ids — typically just the
	// session BE acting as the sidebar's gateway. broadcast() fans
	// every state-bearing event to this set. Subscribers are added by
	// a `subscribe` cross-app message and removed by `unsubscribe`.
	// The router silently drops messages to gone instances, so a
	// missed unsubscribe leaks one map entry but doesn't break anything.
	subs map[string]struct{}

	// nextWireReqID counts our outbound PrepareSpawn req_ids. Starts
	// at 1.
	nextWireReqID uint64
	pendingPrep   map[uint64]*pendingPrepareSpawn

	// inlineProcs tracks live KindRunInline subprocesses by req_id
	// so cli-originated stdin / cancel / disconnect messages can
	// address them. Mutated under mu.
	inlineProcs map[string]*inlineProc

	// isRoot is true when wash-priv itself runs as uid 0. When true
	// there's no privilege to escalate to, so the executors skip
	// the `sudo` wrapper and exec the target binary directly.
	isRoot bool

	// passwordless is true when the configured sudo accepts our
	// requests without prompting for a password — typically because
	// /etc/sudoers grants NOPASSWD to the user running wash-priv.
	// Detected once at startup via `sudo -n true`. When true, sudo
	// is still used (we need the uid bump) but with `-n` instead of
	// `-S`, no stdin password pipe, no FE password modal.
	passwordless bool
}

// skipPassword returns true when no FE password prompt is needed for
// privileged actions — either because we're already root (no escalation
// at all) or because sudo is configured passwordless (no credentials to
// collect). Enqueue + Approve consult this to decide whether to mint
// a handshake / open the FE modal or run straight to executeApproved.
//
// Crucially this DOES NOT gate the consent step — see autoApprove for
// that. Skipping the password just means "no credential challenge",
// not "no user gesture": NOPASSWD in sudoers is an explicit trust
// grant on auth, not a blanket pre-approval of every action. The
// passwordless path still requires the user to click Approve in the
// priv queue UI per request — same model as the existing password
// flow, where unlock approves only the prompting request and the
// rest stay queued.
func (s *State) skipPassword() bool { return s.isRoot || s.passwordless }

// autoApprove returns true when each new request should bypass the
// queue UI entirely and run immediately on enqueue, with no human
// gesture in the loop. This is the embedded-VM (uid=0) posture: the
// VM is a single-tenant sandbox, there's no privilege to escalate
// to, and asking the user to Approve every wash launcher click would
// be friction without security benefit. It's NOT what passwordless
// sudo does — see skipPassword.
func (s *State) autoApprove() bool { return s.isRoot }

func NewState() *State {
	isRoot := os.Geteuid() == 0
	return &State{
		queue:        []*Request{},
		pendingPrep:  map[uint64]*pendingPrepareSpawn{},
		inlineProcs:  map[string]*inlineProc{},
		subs:         map[string]struct{}{},
		isRoot:       isRoot,
		passwordless: !isRoot && detectPasswordlessSudo(cfg.SudoBin),
	}
}

// addSubscriber registers inst as a subscriber and ships the current
// state snapshot to it. Called from the subscribe handler.
func (s *State) addSubscriber(c *sdk.Conn, inst string) {
	s.mu.Lock()
	s.subs[inst] = struct{}{}
	snap := s.stateSnapshotLocked()
	s.mu.Unlock()
	_ = c.SendAppMsgTo(wire.Recipient{InstanceID: inst}, snap)
}

// removeSubscriber drops inst from the fan-out set.
func (s *State) removeSubscriber(inst string) {
	s.mu.Lock()
	delete(s.subs, inst)
	s.mu.Unlock()
}

// broadcast sends payload to every subscriber. Caller MUST NOT hold
// s.mu — we lock briefly to snapshot the subs list. Cross-app sends
// to gone instances are silently dropped by the router; we don't
// bother GC-ing here.
func (s *State) broadcast(c *sdk.Conn, payload map[string]any) {
	s.mu.Lock()
	subs := make([]string, 0, len(s.subs))
	for k := range s.subs {
		subs = append(subs, k)
	}
	s.mu.Unlock()
	for _, inst := range subs {
		_ = c.SendAppMsgTo(wire.Recipient{InstanceID: inst}, payload)
	}
}

// stateSnapshotLocked returns the current full state in wire shape.
// Caller MUST hold s.mu. This is the catch-all snapshot that
// subscribers receive on subscribe and via SendStateSnapshot.
func (s *State) stateSnapshotLocked() map[string]any {
	out := map[string]any{
		"kind":   "state",
		"locked": s.password == nil,
		"queue":  renderQueue(s.queue),
	}
	if s.password != nil {
		out["idle_remaining_ms"] = idleRemaining(s.lastActivity, cfg.IdleTimeout)
	}
	return out
}

// detectPasswordlessSudo probes whether the configured sudoBin will
// run a no-op command without prompting for a password. `sudo -n true`
// returns 0 iff our user has NOPASSWD in /etc/sudoers (or one of its
// drop-ins) for the target user we'd elevate to. The probe is run
// once at startup; if you change /etc/sudoers later you need to
// restart wash-priv (which is fine — the daemon is singleton and
// re-spawn is cheap).
//
// Returns false on any error — the safe default is "we'll prompt for
// a password," which is the existing behaviour.
func detectPasswordlessSudo(sudoBin string) bool {
	if sudoBin == "" {
		return false
	}
	c := exec.Command(sudoBin, "-n", "true")
	// Drop our stdin so sudo can't somehow read from a parent's tty.
	c.Stdin = nil
	c.Stdout = nil
	c.Stderr = nil
	// The e2e test stub (cmd/wash-priv-fakesudo) writes every
	// invocation to $FAKESUDO_LOG so tests can assert what crossed
	// the sudo boundary. Our probe is internal bookkeeping, not a
	// test-observable event — clearing the env var for the probe's
	// child keeps the test log uncontaminated. No effect with real
	// sudo, which doesn't read FAKESUDO_LOG.
	c.Env = filterEnv(os.Environ(), "FAKESUDO_LOG")
	err := c.Run()
	if err == nil {
		log.Printf("wash-priv: sudo passwordless probe succeeded — skipping FE password modal")
		return true
	}
	return false
}

// filterEnv returns env with any entries whose key matches one of the
// excluded names removed. Order preserved.
func filterEnv(env []string, excluded ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			out = append(out, kv)
			continue
		}
		key := kv[:idx]
		skip := false
		for _, ex := range excluded {
			if key == ex {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}

// AttachConn binds the SDK conn at OnReady time. Idempotent.
func (s *State) AttachConn(c *sdk.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn = c
}

// WipePassword zeroes the cached password buffer and resets handshake
// state. Idempotent — calling on a locked state is a no-op.
func (s *State) WipePassword() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wipeLocked()
}

// wipeLocked clears the cached password. handshake + pendingApproveReq
// are tied to an in-flight Approve and are cleared separately by the
// explicit lock / idle paths — NOT by a browser refresh. A refresh
// (page-nonce change) gets the security-relevant clear (password
// out of memory) but keeps the pending unlock alive so the new FE
// can re-show the modal via HandleHello below.
func (s *State) wipeLocked() {
	if s.password != nil {
		secureUnlock(s.password, log.Printf)
		wipe(s.password)
		s.password = nil
	}
}

// wipeAllLocked is the harder variant — clears the in-flight Approve
// state too. Used by explicit Lock and by idle timeout, where the
// user has signalled they're done with the current session.
func (s *State) wipeAllLocked() {
	s.wipeLocked()
	s.handshake = nil
	s.pendingApproveReq = ""
}

// IdleTicker zeros the password if idle for ≥ d. Polls every minute
// — finer granularity buys no real protection given GC dwell and
// costs a wakeup. Returns when the conn is closed.
func (s *State) IdleTicker(d time.Duration) {
	if d == 0 {
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		if s.password != nil && !s.lastActivity.IsZero() && time.Since(s.lastActivity) >= d {
			s.wipeAllLocked()
			c := s.conn
			s.mu.Unlock()
			log.Printf("wash-priv: password cache cleared (idle timeout)")
			s.broadcast(c, map[string]any{"kind": "locked", "reason": "idle"})
			s.appendAudit(auditRecord{Decision: "idle_locked"})
			continue
		}
		s.mu.Unlock()
	}
}

// SendStateSnapshot broadcasts the full current state to subscribers.
// Called by resync (rare, debug-y) and as a building block for the
// snapshot-on-subscribe path. The per-event broadcasts (req.new /
// req.update / locked / unlocked / need_password / unlock_err) still
// fire on each individual transition; this is the catch-all push.
func (s *State) SendStateSnapshot(c *sdk.Conn) {
	s.mu.Lock()
	out := s.stateSnapshotLocked()
	s.mu.Unlock()
	s.broadcast(c, out)
}

func renderQueue(q []*Request) []map[string]any {
	out := make([]map[string]any, 0, len(q))
	for _, r := range q {
		out = append(out, requestView(r))
	}
	return out
}

func requestView(r *Request) map[string]any {
	out := map[string]any{
		"req_id":          r.ReqID,
		"kind":            string(r.Kind),
		"sender_app_id":   r.Sender.AppID,
		"sender_inst_id":  r.Sender.InstanceID,
		"app_id":          r.AppID,
		"argv":            r.Argv,
		"cwd":             r.Cwd,
		"reason":          stripControl(r.Reason),
		"status":          string(r.Status),
		"created_ms":      r.CreatedAt.UnixMilli(),
		"started_ms":      asMillis(r.StartedAt),
		"finished_ms":     asMillis(r.FinishedAt),
		"exit_code":       r.ExitCode,
		"error":           r.ErrorMsg,
		"spawned_inst_id": r.SpawnedInst,
	}
	if r.CliOrigin != nil {
		out["cli_origin"] = map[string]any{
			"pid":  r.CliOrigin.PID,
			"uid":  r.CliOrigin.UID,
			"comm": r.CliOrigin.Comm,
			"tty":  r.CliOrigin.TTY,
		}
	}
	return out
}

func asMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// idleRemaining computes how many ms until the idle timeout fires
// given the last-activity timestamp and the timeout. Negative is
// clamped to 0 so the FE doesn't have to special-case.
func idleRemaining(last time.Time, timeout time.Duration) int64 {
	if last.IsZero() || timeout == 0 {
		return 0
	}
	rem := timeout - time.Since(last)
	if rem < 0 {
		return 0
	}
	return rem.Milliseconds()
}

// EnqueueRun records a {kind:"run"} request and notifies the FE.
// argv is the command the user will run in the spawned wash-term.
func (s *State) EnqueueRun(c *sdk.Conn, from wire.Sender, reqID string, argv []string, reason string) {
	if len(argv) == 0 {
		_ = c.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{
			"kind": "error", "req_id": reqID, "code": "bad_request", "msg": "argv is empty",
		})
		return
	}
	s.enqueue(c, &Request{
		ReqID:     reqID,
		Kind:      KindRun,
		Sender:    from,
		Argv:      argv,
		Reason:    reason,
		Status:    StatusQueued,
		CreatedAt: time.Now(),
	})
}

// EnqueueRunInline records a {kind:"run_inline"} request from the
// wash-sudo CLI. argv goes straight to sudo (no wash-term wrap); the
// subprocess's stdin/stdout/stderr are piped through the cliSession
// bridge so wash-sudo behaves like a normal `sudo cmd`.
func (s *State) EnqueueRunInline(c *sdk.Conn, from wire.Sender, reqID string, argv []string, cwd string, env map[string]string, reason string, noPrompt bool, origin *CliOrigin, pty bool, cols, rows uint16) {
	if len(argv) == 0 {
		_ = c.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{
			"kind": "priv.result", "req_id": reqID, "exit_code": 2, "error": "argv is empty",
		})
		return
	}
	if noPrompt {
		s.mu.Lock()
		locked := s.password == nil
		s.mu.Unlock()
		if locked {
			_ = c.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{
				"kind": "priv.result", "req_id": reqID, "exit_code": 1, "error": "no_prompt set and password cache is empty",
			})
			return
		}
	}
	s.enqueue(c, &Request{
		ReqID:     reqID,
		Kind:      KindRunInline,
		Sender:    from,
		Argv:      argv,
		Cwd:       cwd,
		Env:       env,
		Reason:    reason,
		NoPrompt:  noPrompt,
		Pty:       pty,
		Cols:      cols,
		Rows:      rows,
		CliOrigin: origin,
		Status:    StatusQueued,
		CreatedAt: time.Now(),
	})
}

// EnqueueSpawn records a {kind:"spawn"} request.
func (s *State) EnqueueSpawn(c *sdk.Conn, from wire.Sender, reqID, appID string, args []string, reason string) {
	if appID == "" {
		_ = c.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{
			"kind": "error", "req_id": reqID, "code": "bad_request", "msg": "app_id is empty",
		})
		return
	}
	s.enqueue(c, &Request{
		ReqID:     reqID,
		Kind:      KindSpawn,
		Sender:    from,
		AppID:     appID,
		Argv:      args,
		Reason:    reason,
		Status:    StatusQueued,
		CreatedAt: time.Now(),
	})
}

func (s *State) enqueue(c *sdk.Conn, r *Request) {
	s.mu.Lock()
	// Reject duplicates: the requester may be retrying after a router
	// hiccup, but for an audit-grade tool we treat that as ambiguous.
	for _, existing := range s.queue {
		if existing.ReqID == r.ReqID && existing.Sender.InstanceID == r.Sender.InstanceID {
			s.mu.Unlock()
			return
		}
	}
	s.queue = append(s.queue, r)

	// Auto-prompt for password if we're locked AND no other pw
	// dialog is already pending. This is the "arrival pops the
	// modal directly" UX — entering the password both unlocks the
	// cache and approves the prompting item; other queued items
	// still need their own Approve clicks. If a second request
	// arrives while a modal is already up, it queues quietly
	// rather than spawning a competing prompt.
	//
	// Skipped entirely when skipPassword (already root, or sudo is
	// configured NOPASSWD) — we auto-approve below instead.
	var autoPrompt bool
	var autoPub []byte
	if !s.skipPassword() && s.password == nil && s.pendingApproveReq == "" {
		hs, pub, err := NewHandshake()
		if err != nil {
			log.Printf("wash-priv: handshake init: %v", err)
		} else {
			s.handshake = hs
			s.pendingApproveReq = r.ReqID
			autoPrompt = true
			autoPub = pub
		}
	}
	s.mu.Unlock()

	// One ops-friendly line per enqueue so the e2e + ops eyeballs
	// can find the req_id without parsing audit jsonl. Quiet on
	// every other state transition — those land in priv-audit.log.
	log.Printf("wash-priv enqueue: req_id=%s kind=%s sender=%s/%s", r.ReqID, r.Kind, r.Sender.AppID, r.Sender.InstanceID)
	s.broadcast(c, map[string]any{"kind": "req.new", "req": requestView(r)})
	if autoPrompt {
		// Pubkey is public by definition — logging gives e2e + ops
		// a handle on the unlock attempt without a browser.
		log.Printf("wash-priv: need_password be_pubkey=%s (auto-prompt req=%s)", base64.StdEncoding.EncodeToString(autoPub), r.ReqID)
		s.broadcast(c, map[string]any{
			"kind":      "need_password",
			"be_pubkey": autoPub,
			"req_id":    r.ReqID,
		})
	}
	// Auto-approve passthrough (isRoot only, NOT passwordless):
	// promote straight to Running + execute without a user gesture.
	// The queue UI still receives req.new + req.update, so the audit
	// trail / FE list is preserved. Passwordless mode falls through
	// here — the user still clicks Approve in the queue UI, but
	// HandleApprove won't open a password modal (skipPassword=true
	// short-circuits the s.password==nil branch).
	if s.autoApprove() {
		log.Printf("wash-priv: passthrough auto-approve req_id=%s (isRoot=%v)", r.ReqID, s.isRoot)
		go s.autoApproveAndExecute(c, r)
	}
}

// autoApproveAndExecute promotes a queued request to Running without
// the password gate and kicks executeApproved. Only called from the
// skip-password passthrough path; the regular path goes through
// HandleApprove.
func (s *State) autoApproveAndExecute(c *sdk.Conn, r *Request) {
	s.mu.Lock()
	if r.Status != StatusQueued {
		s.mu.Unlock()
		return
	}
	r.Status = StatusRunning
	r.StartedAt = time.Now()
	s.lastActivity = time.Now()
	view := requestView(r)
	s.mu.Unlock()
	s.broadcast(c, map[string]any{"kind": "req.update", "req": view})
	s.executeApproved(c, r)
}

// HandleApprove starts the spawn pipeline for the named request. If
// the password isn't cached yet, the request stays queued and we ask
// the FE for the password; the unlock path completes the approval.
func (s *State) HandleApprove(c *sdk.Conn, reqID string) {
	s.mu.Lock()
	r := s.findLocked(reqID)
	if r == nil || r.Status != StatusQueued {
		s.mu.Unlock()
		return
	}
	if s.password == nil && !s.skipPassword() {
		// Re-use an existing handshake when there is one — most
		// often because the enqueue path's auto-prompt already
		// minted a keypair the FE has been encrypting to. A fresh
		// keypair every Approve click would invalidate the FE's
		// in-progress password entry. Only mint when no handshake
		// is in flight.
		var pub []byte
		if s.handshake != nil {
			pub = s.handshake.priv.PublicKey().Bytes()
		} else {
			hs, p, err := NewHandshake()
			if err != nil {
				s.mu.Unlock()
				log.Printf("wash-priv: handshake init: %v", err)
				return
			}
			s.handshake = hs
			pub = p
		}
		// pendingApproveReq records WHICH req prompted the unlock so
		// HandleUnlock runs only that one. Subsequent items still
		// need their own Approve clicks.
		s.pendingApproveReq = reqID
		s.mu.Unlock()
		// Pubkey is public by definition — logging it gives the e2e
		// harness a way to drive the password flow without a browser.
		// In prod this is one harmless log line per unlock attempt.
		log.Printf("wash-priv: need_password be_pubkey=%s", base64.StdEncoding.EncodeToString(pub))
		s.broadcast(c, map[string]any{
			"kind":      "need_password",
			"be_pubkey": pub,
			"req_id":    reqID,
		})
		return
	}
	r.Status = StatusRunning
	r.StartedAt = time.Now()
	s.lastActivity = time.Now()
	view := requestView(r)
	s.mu.Unlock()
	s.broadcast(c, map[string]any{"kind": "req.update", "req": view})
	go s.executeApproved(c, r)
}

// HandleReject removes a request from the queue and notifies the
// requester. Always allowed — no password required to say no.
func (s *State) HandleReject(c *sdk.Conn, reqID, reason string) {
	s.mu.Lock()
	r := s.findLocked(reqID)
	if r == nil || r.Status != StatusQueued {
		s.mu.Unlock()
		return
	}
	r.Status = StatusRejected
	r.FinishedAt = time.Now()
	r.ErrorMsg = reason
	view := requestView(r)
	s.appendAudit(auditRecordFromRequest(r, "reject"))
	s.mu.Unlock()
	s.broadcast(c, map[string]any{"kind": "req.update", "req": view})
	_ = c.SendAppMsgTo(wire.Recipient{InstanceID: r.Sender.InstanceID}, map[string]any{
		"kind":   "rejected",
		"req_id": r.ReqID,
		"reason": reason,
	})
}

// HandleUnlock decrypts the FE-encrypted password, validates it via
// `sudo -S -k -v`, caches it on success, and re-attempts whatever
// approved requests were waiting on it.
func (s *State) HandleUnlock(c *sdk.Conn, ciphertext, fePubKey, nonce []byte) {
	s.mu.Lock()
	hs := s.handshake
	s.mu.Unlock()
	if hs == nil {
		s.broadcast(c, map[string]any{"kind": "unlock_err", "code": "no_handshake", "msg": "no unlock in progress"})
		return
	}
	pw, err := hs.Decrypt(ciphertext, fePubKey, nonce)
	if err != nil {
		s.broadcast(c, map[string]any{"kind": "unlock_err", "code": "decrypt_failed", "msg": err.Error()})
		s.mu.Lock()
		s.handshake = nil
		s.mu.Unlock()
		return
	}
	// Validate by running `sudo -S -k -v`. We feed pw\n via stdin;
	// sudo exits 0 on success, non-zero with stderr "Sorry, try
	// again." on bad password. We deliberately don't loop sudo's
	// retries — one shot, FE re-prompts on failure.
	if err := validateSudo(pw); err != nil {
		wipe(pw)
		s.broadcast(c, map[string]any{"kind": "unlock_err", "code": "bad_password", "msg": err.Error()})
		s.mu.Lock()
		s.handshake = nil
		s.pendingApproveReq = ""
		s.mu.Unlock()
		s.appendAudit(auditRecord{Decision: "password_failed"})
		return
	}
	// Successful unlock approves ONLY the request whose Approve
	// click triggered the password prompt — pendingApproveReq.
	// Other items in the queue are NOT auto-drained; they stay
	// StatusQueued waiting for their own Approve clicks. Reasoning:
	// password entry is implicit consent for one thing, not a
	// blanket approval of everything pending in the queue.
	s.mu.Lock()
	s.password = pw
	secureLock(s.password, log.Printf)
	s.lastActivity = time.Now()
	s.handshake = nil
	pendingID := s.pendingApproveReq
	s.pendingApproveReq = ""
	var toExec *Request
	if pendingID != "" {
		if r := s.findLocked(pendingID); r != nil && r.Status == StatusQueued {
			r.Status = StatusRunning
			r.StartedAt = time.Now()
			toExec = r
		}
	}
	s.mu.Unlock()
	s.broadcast(c, map[string]any{"kind": "unlocked"})
	if toExec != nil {
		s.broadcast(c, map[string]any{"kind": "req.update", "req": requestView(toExec)})
		go s.executeApproved(c, toExec)
	}
}

// HandleLock clears the cache and notifies the FE. reason goes into
// the audit log. Explicit lock clears the in-flight approval state
// too — the user said "stop", we drop everything.
func (s *State) HandleLock(c *sdk.Conn, reason string) {
	s.mu.Lock()
	had := s.password != nil
	s.wipeAllLocked()
	s.mu.Unlock()
	if had {
		log.Printf("wash-priv: password cache cleared (%s)", reason)
		s.appendAudit(auditRecord{Decision: "locked", Reason: reason})
	}
	s.broadcast(c, map[string]any{"kind": "locked", "reason": reason})
}

// executeApproved is the spawn workhorse. Off the SDK goroutine,
// blocking on the child's exit. Sends req.update to the FE and the
// {kind:"result"} reply to the requester when done.
func (s *State) executeApproved(c *sdk.Conn, r *Request) {
	defer s.touchActivity()

	switch r.Kind {
	case KindRun:
		// Sugar: spawn wash-term --exec argv as root, hand back exit code.
		s.executeRun(c, r)
	case KindSpawn:
		s.executeSpawn(c, r)
	case KindRunInline:
		s.executeRunInline(c, r)
	default:
		s.finishWithError(c, r, "bad_kind", "unknown kind "+string(r.Kind))
	}
}

// executeRunInline runs argv directly under sudo with piped
// stdin/stdout/stderr, and ferries the bytes to the cli session via
// SendAppMsgTo. No PrepareSpawn dance — the target isn't a wash app,
// it's a plain command line.
func (s *State) executeRunInline(c *sdk.Conn, r *Request) {
	s.mu.Lock()
	pw := append([]byte(nil), s.password...)
	s.mu.Unlock()

	// Snapshot the sender's instance id; we use it to write streams
	// back. The whole point of EnqueueRunInline is that this is a
	// cli-... synthetic instance.
	cliInst := r.Sender.InstanceID

	onStream := func(stream string, b []byte) {
		_ = c.SendAppMsgTo(wire.Recipient{InstanceID: cliInst}, map[string]any{
			"kind":   "priv.stream",
			"req_id": r.ReqID,
			"stream": stream,
			"bytes":  base64.StdEncoding.EncodeToString(b),
		})
	}
	var proc *inlineProc
	var startErr error
	if r.Pty {
		proc, startErr = startInlinePTY(cfg.SudoBin, r.Argv, r.Cwd, r.Env, pw, r.Cols, r.Rows, onStream)
	} else {
		proc, startErr = startInlineRun(cfg.SudoBin, r.Argv, r.Cwd, r.Env, pw, onStream)
	}
	wipe(pw)
	if startErr != nil {
		s.finishInline(c, r, -1, startErr.Error())
		return
	}
	s.mu.Lock()
	s.inlineProcs[r.ReqID] = proc
	s.mu.Unlock()

	exit := proc.Wait()
	s.mu.Lock()
	delete(s.inlineProcs, r.ReqID)
	s.mu.Unlock()
	s.finishInline(c, r, exit, "")
}

// finishInline records the terminal state on the request, audits,
// and sends the final priv.result to the cli session.
func (s *State) finishInline(c *sdk.Conn, r *Request, exit int, errMsg string) {
	s.mu.Lock()
	r.FinishedAt = time.Now()
	r.ExitCode = exit
	if errMsg != "" {
		r.Status = StatusError
		r.ErrorMsg = errMsg
	} else {
		r.Status = StatusDone
	}
	view := requestView(r)
	s.appendAudit(auditRecordFromRequest(r, "approve"))
	s.mu.Unlock()
	s.broadcast(c, map[string]any{"kind": "req.update", "req": view})
	_ = c.SendAppMsgTo(wire.Recipient{InstanceID: r.Sender.InstanceID}, map[string]any{
		"kind":      "priv.result",
		"req_id":    r.ReqID,
		"exit_code": exit,
		"error":     errMsg,
	})
}

// HandleInlineStdin forwards bytes the cli session sent to the
// running subprocess's stdin pipe. Best-effort — if the process is
// gone (already exited / cancelled) we silently drop.
func (s *State) HandleInlineStdin(reqID string, bytes []byte) {
	s.mu.Lock()
	proc := s.inlineProcs[reqID]
	s.mu.Unlock()
	if proc == nil {
		return
	}
	proc.WriteStdin(bytes)
}

// HandleInlineStdinClose closes the stdin pipe to the subprocess —
// the conventional way to signal "no more input." Used by wash-sudo
// when its own stdin reaches EOF.
func (s *State) HandleInlineStdinClose(reqID string) {
	s.mu.Lock()
	proc := s.inlineProcs[reqID]
	s.mu.Unlock()
	if proc == nil {
		return
	}
	proc.CloseStdin()
}

// HandleInlineCancel kills a running inline subprocess. Used by
// wash-sudo Ctrl-C and by the router when the cli session conn
// closes unexpectedly.
func (s *State) HandleInlineCancel(reqID string) {
	s.mu.Lock()
	proc := s.inlineProcs[reqID]
	s.mu.Unlock()
	if proc == nil {
		return
	}
	proc.Cancel()
}

// HandleInlinePtyResize forwards a terminal resize from the requester
// (a packages-class app whose embedded xterm widget reflowed). No-op
// for pipe-mode inline runs — only PTY-allocated procs honour resize.
func (s *State) HandleInlinePtyResize(reqID string, cols, rows uint16) {
	s.mu.Lock()
	proc := s.inlineProcs[reqID]
	s.mu.Unlock()
	if proc == nil {
		return
	}
	proc.Resize(cols, rows)
}

// executeRun spawns wash-term with --exec ARGV as root via the
// PrepareSpawn → fork+exec(sudo) path. The user watches the command
// run in the wash-term window; this BE only reports the exit code.
func (s *State) executeRun(c *sdk.Conn, r *Request) {
	args := append([]string{"--exec"}, r.Argv...)
	r2 := *r
	r2.AppID = cfg.TermAppID
	r2.Argv = args
	s.requestPrepareSpawn(c, r, &r2)
}

// executeSpawn spawns the named app as root. Argv goes through as
// positional args.
func (s *State) executeSpawn(c *sdk.Conn, r *Request) {
	s.requestPrepareSpawn(c, r, r)
}

// requestPrepareSpawn issues a PrepareSpawn over the wire and records
// the pending-callback. spawnArgs is what we'll actually exec — for
// run-style it's the wash-term variant; for spawn-style it's the
// original request.
func (s *State) requestPrepareSpawn(c *sdk.Conn, displayReq, spawnArgs *Request) {
	s.mu.Lock()
	s.nextWireReqID++
	wireReqID := s.nextWireReqID
	s.pendingPrep[wireReqID] = &pendingPrepareSpawn{
		internalReqID: displayReq.ReqID,
		startedAt:     time.Now(),
	}
	s.mu.Unlock()
	// We don't carry spawnArgs through the pending record:
	// HandlePrepareSpawnResult looks up displayReq and recomputes the
	// exec args there from displayReq.Kind. Only spawnArgs.AppID is
	// needed at PrepareSpawn time.
	_ = c.PrepareSpawn(wireReqID, spawnArgs.AppID)
}

// HandlePrepareSpawnResult continues the spawn after the router
// replies with the minted instance_id + attach_token + binary path.
// On error, we report back to the requester and finish.
func (s *State) HandlePrepareSpawnResult(c *sdk.Conn, wireReqID uint64, instanceID, token, binary string, err error) {
	s.mu.Lock()
	pp, ok := s.pendingPrep[wireReqID]
	if ok {
		delete(s.pendingPrep, wireReqID)
	}
	r := s.findLocked(unwrap(pp))
	s.mu.Unlock()
	if r == nil {
		log.Printf("wash-priv: prepare_spawn.result for unknown wire req %d", wireReqID)
		return
	}
	if err != nil {
		s.finishWithError(c, r, "prepare_spawn", err.Error())
		return
	}
	// Re-derive args for run-vs-spawn at exec time.
	var execArgs []string
	switch r.Kind {
	case KindRun:
		execArgs = append([]string{"--exec"}, r.Argv...)
	case KindSpawn:
		execArgs = r.Argv
	}
	s.mu.Lock()
	r.SpawnedInst = instanceID
	view := requestView(r)
	pw := append([]byte(nil), s.password...)
	s.mu.Unlock()
	s.broadcast(c, map[string]any{"kind": "req.update", "req": view})
	_ = c.SendAppMsgTo(wire.Recipient{InstanceID: r.Sender.InstanceID}, map[string]any{
		"kind":        "spawned",
		"req_id":      r.ReqID,
		"instance_id": instanceID,
	})
	// Fork+exec sudo in a goroutine. This handler is invoked from
	// the SDK's read goroutine, and runSudo's cmd.Run waits for the
	// spawned target to exit — for long-lived UI apps (wash-term,
	// wash-about) that's forever. Blocking the read goroutine would
	// deadlock every subsequent message: refresh hello, next
	// approve, even shutdown signals. The goroutine off-loads the
	// wait so the read loop keeps draining.
	go func() {
		exitCode, runErr := runSudo(cfg.SudoBin, binary, execArgs, instanceID, token, pw)
		wipe(pw)
		s.mu.Lock()
		r.FinishedAt = time.Now()
		r.ExitCode = exitCode
		if runErr != nil {
			r.Status = StatusError
			r.ErrorMsg = runErr.Error()
		} else {
			r.Status = StatusDone
		}
		view2 := requestView(r)
		s.appendAudit(auditRecordFromRequest(r, "approve"))
		s.mu.Unlock()
		s.broadcast(c, map[string]any{"kind": "req.update", "req": view2})
		_ = c.SendAppMsgTo(wire.Recipient{InstanceID: r.Sender.InstanceID}, map[string]any{
			"kind":      "result",
			"req_id":    r.ReqID,
			"exit_code": exitCode,
		})
	}()
}

// unwrap pulls the internal req id from a pending record or "" if
// the record was nil — keeps HandlePrepareSpawnResult flat.
func unwrap(pp *pendingPrepareSpawn) string {
	if pp == nil {
		return ""
	}
	return pp.internalReqID
}

// finishWithError records an internal failure (no exec attempted) on
// the request and notifies both FE and requester.
func (s *State) finishWithError(c *sdk.Conn, r *Request, code, msg string) {
	s.mu.Lock()
	r.Status = StatusError
	r.FinishedAt = time.Now()
	r.ErrorMsg = msg
	view := requestView(r)
	s.appendAudit(auditRecordFromRequest(r, "error"))
	s.mu.Unlock()
	s.broadcast(c, map[string]any{"kind": "req.update", "req": view})
	_ = c.SendAppMsgTo(wire.Recipient{InstanceID: r.Sender.InstanceID}, map[string]any{
		"kind":   "error",
		"req_id": r.ReqID,
		"code":   code,
		"msg":    msg,
	})
}

func (s *State) findLocked(reqID string) *Request {
	if reqID == "" {
		return nil
	}
	for _, r := range s.queue {
		if r.ReqID == reqID {
			return r
		}
	}
	return nil
}

func (s *State) touchActivity() {
	s.mu.Lock()
	if s.password != nil {
		s.lastActivity = time.Now()
	}
	s.mu.Unlock()
}

// validateSudo runs `sudo -S -k -v` with pw\n on stdin. -v validates
// credentials without executing a command; -S reads from stdin; -k
// invalidates any cached credentials (we want fresh auth every time).
// Returns nil on success, error on bad password / configuration.
func validateSudo(pw []byte) error {
	c := exec.Command(cfg.SudoBin, "-S", "-k", "-v")
	c.Stdin = stdinPipeOnce(pw)
	c.Stdout = nil
	stderr := &strings.Builder{}
	c.Stderr = stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("sudo -v failed: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// runSudo execs the spawn. argv is what wash-priv wants to run; the
// outgoing exec.Cmd is sudo wrapping the registered binary with
// --preserve-env so the SDK env vars survive sudo's default scrubbing.
//
// pw is consumed by sudo's -S stdin. We pipe a copy because Go's
// exec.Cmd will close stdin after the child reads it; we don't want
// the caller's buffer reused by sudo.
//
// Mock: if cfg.SudoBin points to a stub (e.g. WASH_PRIV_SUDO_BIN
// = /path/to/fakesudo), the same interface applies — the stub reads
// pw\n from stdin, then exec's the target. Our exit-code wiring
// works identically.
func runSudo(sudoBin, binary string, args []string, instanceID, token string, pw []byte) (int, error) {
	// Three modes — empty pw + non-root means passwordless sudo
	// (NOPASSWD policy detected at startup); pw populated means the
	// standard auth-then-exec flow; root means skip sudo entirely.
	var c *exec.Cmd
	switch {
	case os.Geteuid() == 0:
		c = exec.Command(binary, args...)
	default:
		// Shared sudo-arg assembly (sudoargv.go). appSpawnPreserveEnv
		// carries the router re-attach identity (app/instance/token).
		// With a password we use -S -k and feed pw\n on stdin; without
		// one we use -n (passwordless NOPASSWD policy, verified at
		// startup — a prompt here would be an audit-worthy config skew).
		injectPassword := len(pw) > 0
		cmdArgs := sudoArgv(injectPassword, appSpawnPreserveEnv, nil, append([]string{binary}, args...))
		c = exec.Command(sudoBin, cmdArgs...)
		if injectPassword {
			c.Stdin = stdinPipeOnce(pw)
		}
	}
	c.Env = append(os.Environ(),
		"WASH_APP_ID="+filepath.Base(binary), // overridden below if we can derive better
		"WASH_INSTANCE_ID="+instanceID,
		"WASH_ATTACH_TOKEN="+token,
	)
	// Filter env to remove our own WASH_INSTANCE_ID/etc from the
	// parent so the child uses the freshly-set ones we just appended.
	c.Env = overrideEnv(c.Env, map[string]string{
		"WASH_INSTANCE_ID":  instanceID,
		"WASH_ATTACH_TOKEN": token,
	})
	c.Stdout = nil
	stderr := &strings.Builder{}
	c.Stderr = stderr
	err := c.Run()
	if s := strings.TrimSpace(stderr.String()); s != "" {
		// sudo's "[sudo] password for X:" prompt also lands here.
		// Log so failures (wrong password, refused command, missing
		// env-preserve grant) are visible; harmless on success.
		log.Printf("wash-priv runSudo %s stderr: %s", binary, s)
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			log.Printf("wash-priv: runSudo %s instance=%s exit=%d", binary, instanceID, ee.ExitCode())
			return ee.ExitCode(), nil
		}
		log.Printf("wash-priv: runSudo %s instance=%s: %v", binary, instanceID, err)
		return -1, err
	}
	// Positive audit trail: clean privileged spawns must be visible
	// too, not just failures.
	log.Printf("wash-priv: runSudo %s instance=%s exit=0", binary, instanceID)
	return 0, nil
}

// stdinPipeOnce returns an *os.File-like reader that yields pw\n
// then EOF. exec.Cmd accepts io.Reader for Stdin, so this is fine.
func stdinPipeOnce(pw []byte) *strings.Reader {
	buf := make([]byte, 0, len(pw)+1)
	buf = append(buf, pw...)
	buf = append(buf, '\n')
	// strings.Reader takes a string; we tolerate the copy because the
	// alternative (a pipe + goroutine writer) needs cleanup and risks
	// a dangling FD if sudo dies before consuming.
	return strings.NewReader(string(buf))
}

// overrideEnv replaces values in env for keys named in m. Anything
// not in env is appended. Keeps a stable order so logging is sane.
func overrideEnv(env []string, m map[string]string) []string {
	out := env[:0:0]
	seen := map[string]bool{}
	for _, kv := range env {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			out = append(out, kv)
			continue
		}
		key := kv[:idx]
		if newVal, ok := m[key]; ok {
			out = append(out, key+"="+newVal)
			seen[key] = true
			continue
		}
		out = append(out, kv)
	}
	for k, v := range m {
		if !seen[k] {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// stripControl removes ASCII C0 + DEL + ESC bytes so a malicious
// reason string can't render misleading escape sequences in the FE's
// "App X wants to run …" prompt.
func stripControl(s string) string {
	if s == "" {
		return ""
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < 0x20 || b == 0x7f {
			if b == '\n' || b == '\t' {
				out = append(out, b)
				continue
			}
			continue
		}
		out = append(out, b)
	}
	return string(out)
}
