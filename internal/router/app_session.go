package router

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/sirmick/wash/internal/wire"
)

// closeGrace is the deadline an app has to respond to
// window.close_requested before the router force-kills (WIRE.md §10).
const closeGrace = 5 * time.Second

// UIDNoPeer is the sentinel uid value used when SO_PEERCRED can't be
// read — in-process test transports, non-unix sockets. Distinct from
// uid 0 (root) so IsRoot logic can't mistake an unknown uid for root.
const UIDNoPeer = ^uint32(0)

// IsRoot reports whether the WM should treat this app instance as
// part of the privilege chain. True if (a) the process runs as uid 0,
// or (b) the app id is one of the privilege-chain reserved ids
// (com.wash.priv). The reserved-id branch exists so wash-priv's own
// window — which runs as the regular user — still wears the red
// stripe; safe because reserved ids are gated by the registry to
// trusted binaries only.
func (inst *AppInstance) IsRoot() bool {
	if inst.PeerUID == 0 {
		return true
	}
	return inst.AppID == "com.wash.priv"
}

// AppInstance is one running app's connection state. The router holds
// one per spawned process (and one per in-process app in tests).
type AppInstance struct {
	Transport  FrameTransport
	AppID      string
	InstanceID string
	WindowID   uint32 // 0 if surface=desktop OR Kiosk
	Manifest   *Manifest
	Cmd        *exec.Cmd // nil for in-process tests
	// spawnLog captures the BE's stdout+stderr (last ~16 KB) for
	// crash reporting. nil for in-process tests.
	spawnLog *ringBuf
	// startedAt is when the process was spawned; used to compute
	// uptime for the crash dialog.
	startedAt time.Time
	// expectedExit flips true when the router itself terminates the
	// BE (close handshake confirmed, devreload, shutdown). The
	// crash-broadcast path in spawnAndRun checks it and stays
	// silent — a router-driven SIGTERM is not a crash. A panic, an
	// OOM-kill, or a signal from an external party leaves this
	// false and the dialog fires as intended.
	expectedExit atomic.Bool

	// PeerUID is the kernel-attested uid of the connected app process,
	// read via SO_PEERCRED at attach time. Zero means root; uidNoPeer
	// (^uint32(0)) means we couldn't determine it (in-process tests,
	// non-unix transports). The router fills this once at attach and
	// never mutates it; consumers MUST treat it as authoritative.
	PeerUID uint32

	// Kiosk is set for the --initial-app instance. It forces the
	// shell to mount the element at the root surface regardless of
	// the manifest's declared surface, and suppresses the window
	// id so per-window plumbing (set_title etc.) is skipped.
	Kiosk bool

	router *Router

	writeMu sync.Mutex

	// awaiting close confirmation. nil when no close is pending.
	closeMu      sync.Mutex
	closeConfirm chan bool

	// bundleMu guards bundleBytes + bundleChannel + bundleReady.
	// bundleBytes accumulates the JS bundle the SDK uploads
	// post-handshake on the bundle channel. The full bytes are
	// retained for the instance lifetime so every (re)attaching
	// shell can replay them — no second BE round-trip needed.
	bundleMu      sync.Mutex
	bundleBytes   []byte
	bundleChannel uint32        // channel id reserved for app-side upload
	bundleReady   chan struct{} // closed when the upload completes
}

// newBundleReady returns a fresh signaling channel; bundleReady is
// lazy-init'd so AppInstance{} stays zero-valued.
func (inst *AppInstance) ensureBundleReady() {
	inst.bundleMu.Lock()
	if inst.bundleReady == nil {
		inst.bundleReady = make(chan struct{})
	}
	inst.bundleMu.Unlock()
}

// HandleApp is the entrypoint for a freshly-spawned app: it owns the
// transport for the lifetime of the connection. It performs the
// handshake (WIRE.md §6), declares the instance to all shells, then
// runs the frame loop until ctx cancels or the transport closes.
//
// expectedAppID is what the router spawned and what the identity
// frame MUST match. windowed selects whether a window is created on
// declare (surface=window) or the element is mounted as the root
// (surface=desktop).
func (r *Router) HandleApp(ctx context.Context, t FrameTransport, manifest *Manifest, cmd *exec.Cmd) error {
	return r.handleAppOpts(ctx, t, manifest, cmd, false)
}

// HandleAppKiosk is like HandleApp but marks the instance as the
// kiosk root: surface forced to desktop, no window id, declared as
// the root surface regardless of the manifest. Used by --initial-app
// for full-screen single-app deployments and e2e tests.
func (r *Router) HandleAppKiosk(ctx context.Context, t FrameTransport, manifest *Manifest, cmd *exec.Cmd) error {
	return r.handleAppOpts(ctx, t, manifest, cmd, true)
}

func (r *Router) handleAppOpts(ctx context.Context, t FrameTransport, manifest *Manifest, cmd *exec.Cmd, kiosk bool) error {
	inst := &AppInstance{
		Transport: t,
		AppID:     manifest.ID,
		Manifest:  manifest,
		Cmd:       cmd,
		Kiosk:     kiosk,
		PeerUID:   UIDNoPeer,
		router:    r,
	}
	if err := inst.handshake(ctx); err != nil {
		_ = t.Close()
		return fmt.Errorf("handshake %s: %w", manifest.ID, err)
	}
	r.bringUp(ctx, inst)
	err := inst.loop(ctx)
	r.tearDown(inst)
	_ = t.Close()
	return err
}

// handshake reads the app's identity frame, validates it, and replies
// with identity.ack. It assigns an instance id (and window id if the
// app's surface is window).
func (inst *AppInstance) handshake(ctx context.Context) error {
	f, err := wire.ReadOne(ctx, inst.Transport)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("read identity: %w", err)
	}
	if f.Channel != ChannelControl {
		return fmt.Errorf("identity frame on wrong channel %d", f.Channel)
	}
	msg, err := wire.DecodeCtrl(f.Payload)
	if err != nil {
		return fmt.Errorf("decode identity: %w", err)
	}
	ident, ok := msg.(wire.Identity)
	if !ok {
		return fmt.Errorf("expected identity, got %T", msg)
	}
	if ident.AppID != inst.AppID {
		_ = inst.writeCtrl(wire.NewError(wire.ErrCodeBadIdentity, "app_id mismatch"))
		return fmt.Errorf("app_id mismatch: spawned %q, identity %q", inst.AppID, ident.AppID)
	}
	if ident.Proto != ProtocolVersion {
		_ = inst.writeCtrl(wire.NewError(wire.ErrCodeProtoMismatch, "protocol version mismatch"))
		return fmt.Errorf("proto mismatch: %d", ident.Proto)
	}

	inst.InstanceID = inst.router.allocInstanceID()
	if inst.Manifest.Surface == SurfaceWindow && !inst.Kiosk {
		inst.WindowID = inst.router.allocWindowID()
	}
	ack := wire.NewIdentityAck(inst.InstanceID, inst.WindowID)
	ack.Session = inst.router.handshakeSession()
	if err := inst.writeCtrl(ack); err != nil {
		return fmt.Errorf("write identity.ack: %w", err)
	}
	return nil
}

// loop reads frames until the transport closes. ctx cancel triggers a
// best-effort shutdown.
func (inst *AppInstance) loop(ctx context.Context) error {
	err := wire.ReadLoop(ctx, inst.Transport, inst.dispatch)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (inst *AppInstance) dispatch(f wire.Frame) error {
	switch f.Channel {
	case ChannelControl:
		return inst.handleCtrl(f.Payload)
	case ChannelEvent:
		return inst.handleEvt(f.Payload, f.Class())
	default:
		// Channel ≥ 2: raw byte stream.
		b := inst.router.lookupChannel(f.Channel)
		if b == nil || b.app != inst {
			inst.router.log("app %s: drop raw frame on unbound channel %d", inst.AppID, f.Channel)
			return nil
		}
		if b.kind == wire.ChannelKindBundle {
			// Bundle bytes from SDK during handshake — append to
			// the instance's bundle cache. No shell forwarding;
			// per-shell replay channels handle delivery.
			inst.bundleMu.Lock()
			inst.bundleBytes = append(inst.bundleBytes, f.Payload...)
			inst.bundleMu.Unlock()
			return nil
		}
		// Tee into the scrollback ring buffer (so a future
		// reattaching shell can replay them) and forward to the
		// currently-attached shell, if any.
		b.shellMu.Lock()
		if b.buf != nil {
			b.buf.Write(f.Payload)
		}
		sh := b.shell
		b.shellMu.Unlock()
		if sh == nil {
			// Shell detached — bytes already captured in the buffer;
			// the next attached shell will replay them.
			return nil
		}
		// Preserve the sender app's class on the forward — if the
		// app's SDK marked this stream Bulk (pty output), we want
		// the FE-bound forward to inherit that. The default-Bulk
		// path in WriteRawFrame is the right answer when the
		// originating frame doesn't carry an explicit class bit.
		return sh.WriteRawFrameClass(f.Channel, f.Payload, f.Class())
	}
}

func (inst *AppInstance) handleCtrl(payload []byte) error {
	msg, err := wire.DecodeCtrl(payload)
	if err != nil {
		return fmt.Errorf("ctrl decode: %w", err)
	}
	switch m := msg.(type) {
	case wire.ChannelOpen:
		return inst.handleChannelOpen(m)
	case wire.ChannelClose:
		// If the closing channel was the bundle uploader, the
		// bundle is now complete — flip bundleReady and fan it
		// out to every currently-attached shell.
		inst.bundleMu.Lock()
		isBundle := inst.bundleChannel == m.ChannelID && inst.bundleReady != nil
		if isBundle {
			select {
			case <-inst.bundleReady:
				// already closed
			default:
				close(inst.bundleReady)
			}
		}
		inst.bundleMu.Unlock()
		inst.router.closeChannel(m.ChannelID, "app requested close")
		if isBundle {
			inst.router.fanOutBundleToAttachedShells(inst)
		}
		return nil
	case wire.Error:
		inst.router.log("app %s: error code=%s: %s", inst.AppID, m.Code, m.Msg)
		return nil
	}
	// Other ctrl messages on app side are not expected post-handshake.
	inst.router.log("app %s: unexpected ctrl msg %T", inst.AppID, msg)
	return nil
}

// handleChannelOpen processes the app's channel.open request: validates
// the window belongs to this app (kiosk root counts), allocates an id,
// records the binding, tells both sides.
func (inst *AppInstance) handleChannelOpen(m wire.ChannelOpen) error {
	// Bundle channels are special — they're an upload-only pipe
	// from the SDK to the router used during/right after handshake
	// to ship the embedded bundle. No shell forwarding; bytes go
	// straight into inst.bundleBytes. The router replays them to
	// every (re)attaching shell on a per-shell channel.
	if m.Kind == wire.ChannelKindBundle {
		id := inst.router.allocChannelID()
		b := &channelBinding{
			channelID: id,
			app:       inst,
			windowID:  inst.WindowID,
			kind:      wire.ChannelKindBundle,
		}
		inst.router.registerChannel(b)
		inst.bundleMu.Lock()
		inst.bundleChannel = id
		if inst.bundleReady == nil {
			inst.bundleReady = make(chan struct{})
		}
		inst.bundleMu.Unlock()
		return inst.writeCtrl(wire.NewChannelOpened(m.ReqID, id))
	}
	// For kiosk / desktop-surface apps the app has WindowID=0; the
	// app must request windowID=0 as well (we treat that as "the
	// root surface of this app"). For windowed apps, the requested
	// window must match the app's window.
	if m.WindowID != inst.WindowID {
		return inst.writeCtrl(wire.NewChannelOpenErr(m.ReqID, wire.ErrCodeForbidden, "window not owned by app"))
	}
	// v0.1: one shell. Pick it (any). For multi-shell we'd need the
	// app to specify, or open one channel per shell.
	shells := inst.router.shellList()
	if len(shells) == 0 {
		return inst.writeCtrl(wire.NewChannelOpenErr(m.ReqID, wire.ErrCodeInternal, "no shell attached"))
	}
	shell := shells[0]
	id := inst.router.allocChannelID()
	b := &channelBinding{
		channelID: id,
		app:       inst,
		shell:     shell,
		windowID:  m.WindowID,
		buf:       newRingBuffer(ChannelScrollbackBytes),
	}
	inst.router.registerChannel(b)
	if err := shell.WriteCtrl(wire.NewShellChannelBind(id, m.WindowID)); err != nil {
		inst.router.closeChannel(id, "shell bind failed")
		return inst.writeCtrl(wire.NewChannelOpenErr(m.ReqID, wire.ErrCodeInternal, err.Error()))
	}
	return inst.writeCtrl(wire.NewChannelOpened(m.ReqID, id))
}

func (inst *AppInstance) handleEvt(payload []byte, class wire.Class) error {
	t, err := wire.PeekEvtType(payload)
	if err != nil {
		return fmt.Errorf("evt peek: %w", err)
	}
	switch t {
	case wire.TEvtAppMsg:
		var m wire.EvtAppMsg
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayAppMsgToShell(m, class)
	case wire.TEvtAppMsgSendTo:
		var m wire.EvtAppMsgSendTo
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayAppMsgCrossInstance(m)
	case wire.TEvtWindowSetTitle:
		var m wire.EvtWindowSetTitle
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayWindowTitle(m)
	case wire.TEvtWindowConfirmClose:
		var m wire.EvtWindowConfirmClose
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		inst.deliverCloseConfirm(m.Allow)
		return nil
	case wire.TEvtSpawnRequest:
		var m wire.EvtSpawnRequest
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleSpawnRequest(m)
	case wire.TEvtPrepareSpawn:
		var m wire.EvtPrepareSpawn
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handlePrepareSpawn(m)
	case wire.TEvtNotify:
		var m wire.EvtNotify
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayNotify(m)
	case wire.TEvtClipboardSet:
		var m wire.EvtClipboardSet
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleClipboardSet(m)
	case wire.TEvtClipboardGet:
		var m wire.EvtClipboardGet
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleClipboardGet(m)
	case wire.TEvtAppStateSet:
		var m wire.EvtAppStateSet
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		// State is JSON bytes; pass straight through. RawMessage
		// validates that it's well-formed JSON; on bad bytes we
		// drop silently — apps that mis-encode get a no-op.
		var state json.RawMessage = m.State
		if !json.Valid(state) {
			inst.router.log("app %s: app_state.set with invalid JSON, dropping", inst.AppID)
			return nil
		}
		inst.router.log("app_state.set instance=%s bytes=%d", inst.InstanceID, len(state))
		inst.router.broadcastPatches(inst.router.winSession.setAppState(inst.InstanceID, state))
		return nil
	}
	inst.router.log("app %s: unexpected evt %q", inst.AppID, t)
	return nil
}

// relayAppMsgCrossInstance forwards data to a different running
// instance as that instance's normal EvtAppMsg event. v0.1 trust
// model has no capability gate — anyone can address anyone, single-
// user assumption. The router still validates the recipient
// (existence, singleton-vs-sentinel rules) and logs failures.
func (inst *AppInstance) relayAppMsgCrossInstance(m wire.EvtAppMsgSendTo) error {
	// From identifies the sending instance to the receiver. The
	// router is the only party that can fill it accurately —
	// trust-by-relay. wash-priv's approval UI uses this for the
	// "App <X> wants to run <Y>" prompt; payload-claimed identity
	// would be spoofable.
	from := wire.Sender{AppID: inst.AppID, InstanceID: inst.InstanceID}
	// CLI session fast-path: SendAppMsgTo({InstanceID:"cli-..."})
	// targets a control-socket connection promoted to a streaming
	// back-channel, not a registered app. Used by wash-priv to
	// stream stdout/stderr/exit back to wash-sudo. The session
	// renders the payload as a JSON envelope on the conn.
	if cli := inst.router.findCliSession(m.Recipient.InstanceID); cli != nil {
		return cli.writeAppMsg(&Sender{AppID: from.AppID, InstanceID: from.InstanceID}, m.Data)
	}
	target, _, err := inst.router.resolveRecipient(context.Background(), m.Recipient)
	if err != nil {
		inst.router.log("app %s app_msg.send.to: %v", inst.AppID, err)
		return nil
	}
	return target.WriteEvt(wire.NewEvtAppMsgFrom(target.WindowID, m.Data, from))
}

// relayAppMsgToShell forwards an APP_MSG from BE to its FE half.
// CBOR-decoded values (often map[any]any) are converted to a JSON-
// friendly shape so the shell can decode without a CBOR runtime.
// Side effect: if the normalized data is a map carrying a string
// "id" field, any control-socket watcher registered for (instance,
// id) gets delivered the data before shell relay. This is how the
// `wash-launch msg --await` path correlates BE replies.
// relayAppMsgToShell forwards an app's BE→FE message to every
// attached shell. class is the priority class the originating app
// stamped on its frame (Interactive by default; Bulk for streaming
// senders that called SendAppMsgBulk / EmitBulk in the SDK). It is
// preserved on the FE-bound ShellAppMsgDeliver so the scheduler
// queues the relayed envelope at the same priority.
func (inst *AppInstance) relayAppMsgToShell(m wire.EvtAppMsg, class wire.Class) error {
	normalized := wire.ToJSONValue(m.Data)
	if asMap, ok := normalized.(map[string]any); ok {
		if msgID, _ := asMap["id"].(string); msgID != "" {
			inst.router.dispatchAppMsgWatchers(inst.InstanceID, msgID, asMap)
		}
	}
	dataJSON, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal app_msg data: %w", err)
	}
	send := wire.NewShellAppMsgDeliver(inst.InstanceID, dataJSON)
	for _, s := range inst.router.shellList() {
		if err := s.WriteCtrlClass(send, class); err != nil {
			return err
		}
	}
	return nil
}

// relayNotify forwards an app's notification to every attached shell.
// v0.1 is open — any app can notify, no capability gate.
func (inst *AppInstance) relayNotify(m wire.EvtNotify) error {
	out := wire.NewShellNotify(inst.InstanceID, m.Title, m.Body, m.Level)
	for _, s := range inst.router.shellList() {
		if err := s.WriteCtrl(out); err != nil {
			return err
		}
	}
	return nil
}

func (inst *AppInstance) relayWindowTitle(m wire.EvtWindowSetTitle) error {
	if m.Win != inst.WindowID {
		return nil
	}
	inst.router.broadcastPatches(inst.router.winSession.setTitle(m.Win, m.Title))
	return nil
}

// handleSpawnRequest enforces the spawn capability and forks a new
// child if the request is allowed.
func (inst *AppInstance) handleSpawnRequest(m wire.EvtSpawnRequest) error {
	if !inst.Manifest.HasCapability(CapSpawn) {
		return inst.WriteEvt(wire.NewEvtSpawnErr(m.AppID, wire.ErrCodeForbidden, "spawn capability not declared"))
	}
	target := inst.router.reg.ByID(m.AppID)
	if target == nil || !target.Enabled() {
		return inst.WriteEvt(wire.NewEvtSpawnErr(m.AppID, wire.ErrCodeNotFound, "unknown app id"))
	}
	if target.Manifest.ProtocolVersion != ProtocolVersion {
		return inst.WriteEvt(wire.NewEvtSpawnErr(m.AppID, wire.ErrCodeIncompatibleProtocol, "protocol mismatch"))
	}
	// Spawn in a goroutine so we don't block this app's read loop on
	// the child's handshake.
	go inst.router.spawnChild(target, inst)
	return nil
}

// handlePrepareSpawn enforces the prepare_spawn capability, mints an
// instance id + attach token, and replies with EvtPrepareSpawnOk so
// the calling app can fork+exec the registered binary itself. The
// router does NOT spawn the binary — that's the caller's job, by
// design (the caller may want to wrap the exec in sudo, a uid switch,
// or a cgroup move). The dial-back is matched in handleAttach by
// AttachToken, and /proc/<pid>/exe is still checked against the
// registered binary path.
func (inst *AppInstance) handlePrepareSpawn(m wire.EvtPrepareSpawn) error {
	if !inst.Manifest.HasCapability(CapPrepareSpawn) {
		return inst.WriteEvt(wire.NewEvtPrepareSpawnErr(m.ReqID, wire.ErrCodeForbidden, "prepare_spawn capability not declared"))
	}
	target := inst.router.reg.ByID(m.AppID)
	if target == nil || !target.Enabled() {
		return inst.WriteEvt(wire.NewEvtPrepareSpawnErr(m.ReqID, wire.ErrCodeNotFound, "unknown app id"))
	}
	if target.Manifest.ProtocolVersion != ProtocolVersion {
		return inst.WriteEvt(wire.NewEvtPrepareSpawnErr(m.ReqID, wire.ErrCodeIncompatibleProtocol, "protocol mismatch"))
	}
	tok, err := mintAttachToken()
	if err != nil {
		return inst.WriteEvt(wire.NewEvtPrepareSpawnErr(m.ReqID, wire.ErrCodeInternal, err.Error()))
	}
	instanceID := inst.router.allocInstanceID()
	inst.router.pendingMu.Lock()
	inst.router.pendingByToken[tok] = &tokenPending{
		appID:      target.Manifest.ID,
		instanceID: instanceID,
		binary:     target.Path,
		spawner:    inst,
		expires:    time.Now().Add(60 * time.Second),
	}
	inst.router.pendingMu.Unlock()
	return inst.WriteEvt(wire.NewEvtPrepareSpawnOk(m.ReqID, instanceID, tok, target.Path))
}

// mintAttachToken returns a 32-byte cryptographic-random hex string.
// The token is the only secret the router relies on to bind a
// dial-back to a pending prepare-spawn record; collisions would let
// an attacker redeem someone else's pending attach. crypto/rand is
// required.
func mintAttachToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// takeTokenPending consumes the pending record for the given token,
// or returns nil if the token is unknown or expired. Expiry is
// checked at lookup time so we don't need a separate reaper goroutine
// for the common case.
func (r *Router) takeTokenPending(token string) *tokenPending {
	if token == "" {
		return nil
	}
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	rec, ok := r.pendingByToken[token]
	if !ok {
		return nil
	}
	delete(r.pendingByToken, token)
	if time.Now().After(rec.expires) {
		return nil
	}
	return rec
}

// spawnChild execs target and runs HandleApp on the resulting transport.
// On success, sends EvtSpawnOk to the requester; on failure,
// EvtSpawnErr. Backed by the shared spawnAndRun.
func (r *Router) spawnChild(target *Entry, requester *AppInstance) {
	inst, err := r.spawnAndRun(context.Background(), target, false)
	if err != nil {
		r.log("spawn %s: %v", target.Manifest.ID, err)
		_ = requester.WriteEvt(wire.NewEvtSpawnErr(target.Manifest.ID, wire.ErrCodeInternal, err.Error()))
		return
	}
	_ = requester.WriteEvt(wire.NewEvtSpawnOk(target.Manifest.ID, inst.InstanceID))
}

// requestClose initiates the X-style close handshake (WIRE.md §10).
// Returns when the app confirms (allow=true/false) or grace expires
// (force-close).
func (inst *AppInstance) requestClose(ctx context.Context) (allowed bool, err error) {
	inst.closeMu.Lock()
	if inst.closeConfirm != nil {
		inst.closeMu.Unlock()
		return false, errors.New("close already in progress")
	}
	ch := make(chan bool, 1)
	inst.closeConfirm = ch
	inst.closeMu.Unlock()

	defer func() {
		inst.closeMu.Lock()
		inst.closeConfirm = nil
		inst.closeMu.Unlock()
	}()

	if err := inst.WriteEvt(wire.NewEvtWindowCloseRequested(inst.WindowID)); err != nil {
		return false, err
	}
	timer := time.NewTimer(closeGrace)
	defer timer.Stop()
	select {
	case allow := <-ch:
		return allow, nil
	case <-timer.C:
		// Force-kill per §10. Subsequent loop iteration will tear down.
		if inst.Cmd != nil && inst.Cmd.Process != nil {
			inst.expectedExit.Store(true)
			_ = inst.Cmd.Process.Kill()
		}
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (inst *AppInstance) deliverCloseConfirm(allow bool) {
	inst.closeMu.Lock()
	ch := inst.closeConfirm
	inst.closeMu.Unlock()
	if ch != nil {
		select {
		case ch <- allow:
		default:
		}
	}
}

// writeCtrl encodes m as JSON and writes a control-channel frame.
func (inst *AppInstance) writeCtrl(m any) error {
	data, err := wire.EncodeCtrl(m)
	if err != nil {
		return err
	}
	return inst.writeFrame(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: data})
}

// WriteEvt encodes m as CBOR and writes an event-channel frame at
// the default class (Interactive). Use WriteEvtClass to preserve a
// class from a relayed FE-originated frame.
func (inst *AppInstance) WriteEvt(m any) error {
	return inst.WriteEvtClass(m, wire.ClassInteractive)
}

// WriteEvtClass is WriteEvt with an explicit priority class. The
// router uses this when relaying ShellAppMsgSend → EvtAppMsg so the
// app's read path observes the same class the FE sender stamped.
func (inst *AppInstance) WriteEvtClass(m any, class wire.Class) error {
	data, err := wire.EncodeEvt(m)
	if err != nil {
		return err
	}
	f := wire.Frame{Flags: wire.FlagEnd, Channel: ChannelEvent, Payload: data}.WithClass(class)
	return inst.writeFrame(f)
}

func (inst *AppInstance) writeFrame(f wire.Frame) error {
	inst.writeMu.Lock()
	defer inst.writeMu.Unlock()
	return inst.Transport.WriteFrame(f)
}

// writeRawFrame forwards bare bytes back to the app on a dynamic
// channel. Mirror of ShellSession.WriteRawFrame.
func (inst *AppInstance) writeRawFrame(channelID uint32, payload []byte) error {
	return inst.writeFrame(wire.Frame{Flags: wire.FlagEnd, Channel: channelID, Payload: payload})
}
