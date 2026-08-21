package router

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirmick/wash/pkg/wire"
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
	spawnLog *ringBuffer
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
	// shellGoneLogged keeps the "browser went away mid-write" note to one
	// line per instance. Without it a chatty app would log per dropped
	// frame, which is a log flood at exactly the moment the log matters.
	//
	// shellGoneDrops counts what was dropped in that stretch, so the
	// recovery line can say how much the browser missed. Both are only
	// touched from the app's single read goroutine (dispatch), with
	// atomics so a reader elsewhere is not a data race.
	shellGoneLogged atomic.Bool
	shellGoneDrops  atomic.Int64

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

	// winMu guards extraWins. extraWins is the set of window ids this
	// instance created via window.create (CapWindows) — beyond the one
	// primary WindowID minted at handshake. A multi-window app
	// (wash-display maps each Wayland/X11 toplevel to a window) owns
	// its toplevels here; ordinary single-window apps leave it nil.
	// See docs/DISPLAY.md §4.
	winMu     sync.Mutex
	extraWins map[uint32]bool
}

// maxWindowsPerInstance caps how many windows one instance may create
// via window.create. A bound, not a tuning knob: unbounded window
// creation is a chrome-DoS vector. The primary handshake window does
// not count against it.
const maxWindowsPerInstance = 64

// ownsWindow reports whether win belongs to this instance — either the
// primary handshake window (covers the 0==0 desktop/kiosk case) or one
// created via window.create.
func (inst *AppInstance) ownsWindow(win uint32) bool {
	if win == inst.WindowID {
		return true
	}
	inst.winMu.Lock()
	defer inst.winMu.Unlock()
	return inst.extraWins[win]
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

// dispatch handles one frame from the app. It is the boundary where a
// DEAD BROWSER stops being an error.
//
// AppInstance.loop is wire.ReadLoop, which ends on any dispatch error —
// so an error returned from here tears the app down and kills the
// process. Forwarding to a shell whose scheduler has closed must
// therefore fail the FRAME, never the app: the browser leaving is a fact
// about the browser, and the whole reattach/replay design exists so
// backend apps outlive it (GH #23, where com.wash.session was killed by
// a browser disconnect and respawned with none of its window state).
//
// Dropping is safe because the raw path writes to the channel's ring
// buffer BEFORE forwarding, and every recovery path — reattach, resync,
// session.snapshot — replays from there. Nothing addressed to a departed
// shell could have been delivered anyway.
//
// Note this is only reachable when the outgoing queue was FULL: Submit's
// fast path enqueues (and returns nil) even on a closed scheduler. That
// is why it takes a stalled browser — a network stall, a sleeping laptop
// — rather than an ordinary reload, and why it survived this long.
func (inst *AppInstance) dispatch(f wire.Frame) error {
	err := inst.dispatchFrame(f)
	if errors.Is(err, ErrSchedulerClosed) {
		inst.shellGoneDrops.Add(1)
		// One line per stretch, not per frame: a chatty app would flood
		// the log at exactly the moment it needs to be readable. The
		// count is reported on recovery instead.
		//
		// router is nil in tests that drive an AppInstance in isolation,
		// the same guard drainLoop carries.
		if inst.router != nil && inst.shellGoneLogged.CompareAndSwap(false, true) {
			inst.router.log("app %s instance=%s: shell gone mid-write ch=%d class=%s — dropping frames, buffered for replay",
				inst.AppID, inst.InstanceID, f.Channel, f.Class())
		}
		return nil
	}
	// Recovery. Without this the log shows a session going dark and never
	// says it came back, so "did it recover?" is unanswerable after the
	// fact — and the drop count is the size of what the reattaching shell
	// had to replay.
	if err == nil && inst.shellGoneLogged.Load() {
		if n := inst.shellGoneDrops.Swap(0); n > 0 {
			inst.shellGoneLogged.Store(false)
			if inst.router != nil {
				inst.router.log("app %s instance=%s: shell back — resumed after dropping %d frame(s)",
					inst.AppID, inst.InstanceID, n)
			}
		}
	}
	return err
}

func (inst *AppInstance) dispatchFrame(f wire.Frame) error {
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
		// Tee into the scrollback ring buffer (so a future
		// reattaching shell can replay them) and forward to the
		// currently-attached shell, if any. The ring is byte-exact
		// and is written unconditionally — it is the authoritative
		// recent history that every recovery path (reattach, resync)
		// replays from.
		b.shellMu.Lock()
		sh := b.shell
		behind := b.behind
		if b.buf != nil {
			// While nobody is taking delivery — detached, or so far behind
			// that we've stopped forwarding — grow the ring rather than
			// overwrite history at the steady-state size. The process is
			// never stalled to preserve bytes, so buffering is the only
			// lever we have; it shrinks again once a shell takes the
			// history (see the reattach/resync paths).
			if sh == nil || behind {
				b.buf.GrowFor(len(f.Payload), ChannelScrollbackMaxBytes)
			}
			b.buf.Write(f.Payload)
		}
		b.shellMu.Unlock()
		if sh == nil {
			// Shell detached — bytes already captured in the buffer;
			// the next attached shell will replay them.
			return nil
		}
		class := f.Class()
		// Non-blocking terminal output (docs/PTY_ROBUST.md, Fix B).
		// Credit-gated Bulk output is the wedge-prone path: a blocking
		// Reserve here runs on the per-app read goroutine, so a wedged
		// FE that has stopped granting credit would back-pressure all
		// the way into the child shell's stdout and hang the terminal.
		// Instead we forward non-blocking: on a would-block we mark the
		// channel "behind" and suppress live output — held byte-exact
		// in the ring — until a resync (a clean reset + realigned
		// snapshot). We never stream past a drop: a hole mid-escape
		// would strand the terminal in a wrong mode. Peer/noCredit and
		// Interactive (transactional) forwards keep the lossless path.
		if class == wire.ClassBulk && b.credit != nil && b.peerConn == nil {
			if behind {
				// Already desynced: ring holds the bytes; a resync replays
				// them — driven by credit recovery, reattach, or the per-shell
				// behind watchdog (behindWatchdogLoop → resyncBehindChannels).
				return nil
			}
			if !sh.tryWriteRawBulk(b, f.Payload) {
				b.shellMu.Lock()
				b.behind = true
				b.shellMu.Unlock()
				inst.router.log("channel %d: FE behind — suppressing live output until resync", f.Channel)
			}
			return nil
		}
		return sh.WriteRawFrameClass(f.Channel, f.Payload, class)
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
		inst.router.closeChannel(m.ChannelID, "app requested close")
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
	// For kiosk / desktop-surface apps the app has WindowID=0; the
	// app must request windowID=0 as well (we treat that as "the
	// root surface of this app"). For windowed apps, the requested
	// window must be one the app owns — its primary handshake window
	// or one it created via window.create (the per-window video stream
	// path for wash-display).
	if !inst.ownsWindow(m.WindowID) {
		return inst.writeCtrl(wire.NewChannelOpenErr(m.ReqID, wire.ErrCodeForbidden, "window not owned by app"))
	}
	// Bind to the foreground head shell — the connection the user is
	// actually looking at — not an arbitrary shells[0]. With several
	// shells stacked (reconnect zombies / multiple tabs) shells[0] is
	// map-order-random, so a terminal's PTY output would land on the
	// wrong connection and the window would hang with no prompt.
	// (True multi-head fanout — every shell sees every terminal — is
	// the tracked follow-up.)
	shell := inst.router.headShellOrAny()
	if shell == nil {
		return inst.writeCtrl(wire.NewChannelOpenErr(m.ReqID, wire.ErrCodeInternal, "no shell attached"))
	}
	id := inst.router.allocChannelID()
	b := &channelBinding{
		channelID: id,
		app:       inst,
		shell:     shell,
		windowID:  m.WindowID,
		kind:      m.Kind,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		// A "file" channel (fm download) skips the credit ledger so its
		// Bulk frames take the LOSSLESS forward path — the credit-gated
		// path's FE-behind suppression drops frames, which would corrupt a
		// downloaded file (docs/QOS.md). Backpressure is the blocking
		// scheduler Submit instead.
		noCredit: m.Kind == wire.ChannelKindFile,
	}
	inst.router.registerChannel(b)
	if err := shell.WriteCtrl(wire.NewShellChannelBind(id, m.WindowID, m.Kind)); err != nil {
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
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		// Unified app_msg: To set ⇒ cross-app outbound; otherwise
		// own-FE relay. The router never trusts a sender-supplied
		// From field — relayAppMsgCrossInstance stamps a router-
		// attested From on the receiver-bound frame.
		if m.To != nil {
			return inst.relayAppMsgCrossInstance(m)
		}
		return inst.relayAppMsgToShell(m, class)
	case wire.TEvtWindowSetTitle:
		var m wire.EvtWindowSetTitle
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayWindowTitle(m)
	case wire.TEvtWindowGeometry:
		var m wire.EvtWindowGeometry
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayWindowGeometry(m)
	case wire.TEvtWindowRaise:
		var m wire.EvtWindowRaise
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayWindowRaise(m)
	case wire.TEvtWindowAttention:
		var m wire.EvtWindowAttention
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayWindowAttention(m)
	case wire.TEvtWindowState:
		var m wire.EvtWindowState
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayWindowState(m)
	case wire.TEvtWindowConfirmClose:
		var m wire.EvtWindowConfirmClose
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		inst.deliverCloseConfirm(m.Win, m.Allow)
		return nil
	case wire.TEvtWindowCreate:
		var m wire.EvtWindowCreate
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleWindowCreate(m)
	case wire.TEvtWindowDestroy:
		var m wire.EvtWindowDestroy
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleWindowDestroy(m)
	case wire.TEvtSpawnRequest:
		var m wire.EvtSpawnRequest
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		if m.Prepare {
			return inst.handlePrepareSpawn(m)
		}
		return inst.handleSpawnRequest(m)
	case wire.TEvtOpenRequest:
		var m wire.EvtOpenRequest
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleOpenRequest(m)
	case wire.TEvtIdleInhibit:
		var m wire.EvtIdleInhibit
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleIdleInhibit(m)
	case wire.TEvtEnvPublish:
		var m wire.EvtEnvPublish
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleEnvPublish(m)
	case wire.TEvtAppRestart:
		var m wire.EvtAppRestart
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleAppRestart(m)
	case wire.TEvtNotify:
		var m wire.EvtNotify
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayNotify(m)
	case wire.TEvtClipboardSet:
		var m wire.EvtClipboardSet
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleClipboardSet(m)
	case wire.TEvtClipboardGet:
		var m wire.EvtClipboardGet
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleClipboardGet(m)
	case wire.TEvtAppStateSet:
		var m wire.EvtAppStateSet
		if err := json.Unmarshal(payload, &m); err != nil {
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
	case wire.TEvtRuntimeStats:
		var m wire.EvtRuntimeStats
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		inst.router.recordRuntimeStats(inst.InstanceID, m)
		return nil
	case wire.TEvtPeerRegister:
		var m wire.EvtPeerRegister
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handlePeerRegister(m)
	case wire.TEvtPeerUnregister:
		var m wire.EvtPeerUnregister
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		inst.router.unregisterPeer(m.Origin)
		return nil
	case wire.TEvtIngressPublish:
		var m wire.EvtIngressPublish
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleIngressPublish(m)
	case wire.TEvtIngressUnpublish:
		var m wire.EvtIngressUnpublish
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		inst.router.ingress.unpublish(m.Path)
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
//
// The To field on the inbound EvtAppMsg names the recipient; the
// router writes the relayed frame with From set to the sender's
// router-attested identity so the receiver knows who messaged it.
func (inst *AppInstance) relayAppMsgCrossInstance(m wire.EvtAppMsg) error {
	if m.To == nil {
		return nil
	}
	to := *m.To
	from := wire.Sender{AppID: inst.AppID, InstanceID: inst.InstanceID}
	// Synthetic router peer: com.wash.router is not a real spawned
	// process — the router answers in-process. Used today by the
	// About panel to fetch the runtime-stats table without us having
	// to broadcast it to every shell. Reply is shipped back to the
	// sender as an EvtAppMsg with From={AppID:"com.wash.router"}.
	if to.AppID == RouterPeerAppID {
		return inst.router.handleRouterPeerMsg(inst, m.Data)
	}
	// CLI session fast-path: SendAppMsgTo({InstanceID:"cli-..."})
	// targets a control-socket connection promoted to a streaming
	// back-channel, not a registered app. Used by wash-priv to
	// stream stdout/stderr/exit back to wash-sudo. The session
	// renders the payload as a JSON envelope on the conn.
	if cli := inst.router.findCliSession(to.InstanceID); cli != nil {
		return cli.writeAppMsg(&Sender{AppID: from.AppID, InstanceID: from.InstanceID}, m.Data)
	}
	target, _, err := inst.router.resolveRecipient(context.Background(), to)
	if err != nil {
		inst.router.log("app %s app_msg cross-instance: %v", inst.AppID, err)
		return nil
	}
	return target.WriteEvt(wire.NewEvtAppMsgFrom(target.WindowID, m.Data, from))
}

// relayAppMsgToShell forwards an APP_MSG from BE to its FE half.
// The event channel speaks JSON, so the payload is already
// JSON bytes — relayed verbatim, no decode+re-encode hop.
//
// Side effect: if the data is a JSON object carrying a string "id"
// field, any control-socket watcher registered for (instance, id)
// gets delivered the decoded map before shell relay. This is how
// the `wash-launch msg --await` path correlates BE replies.
//
// class is the priority class the originating app stamped on its
// frame (Interactive by default; Bulk for streaming senders). It is
// preserved on the FE-bound ShellAppMsgDeliver so the scheduler
// queues the relayed envelope at the same priority.
func (inst *AppInstance) relayAppMsgToShell(m wire.EvtAppMsg, class wire.Class) error {
	if len(m.Data) > 0 {
		var probe map[string]any
		if err := json.Unmarshal(m.Data, &probe); err == nil {
			if msgID, _ := probe["id"].(string); msgID != "" {
				inst.router.dispatchAppMsgWatchers(inst.InstanceID, msgID, probe)
			}
		}
	}
	send := wire.NewShellAppMsgDeliver(inst.InstanceID, json.RawMessage(m.Data))
	for _, s := range inst.router.shellList() {
		if err := s.WriteCtrlClass(send, class); err != nil {
			return err
		}
	}
	return nil
}

// relayNotify routes an EvtNotify based on the sender:
//
//   - From any app OTHER than the notify service: the notification is
//     forwarded to the com.wash.notify singleton as a cross-app msg.
//     The service stores history and decides when/whether to emit a
//     toast (re-emitting its own Notify; the router then broadcasts
//     that to every shell via the branch below). v0.1 has no DND or
//     filtering, so the service always re-emits — but the seam for
//     adding policy lives in one place now.
//
//   - From the notify service itself: broadcast as ShellNotify to
//     every attached shell. The service is the toast authority; this
//     is the only path that produces a visible toast.
//
// Single-authority routing means a notify service crash (or a not-
// yet-running service early in boot) loses transient toasts — that's
// the cost of having one source of truth for filtering / DND / cross-
// shell sync. forwardNotifyToService spawns the singleton on demand,
// which keeps the cold-start window narrow.
func (inst *AppInstance) relayNotify(m wire.EvtNotify) error {
	if inst.AppID == NotifyAppID {
		// The toast names the instance it is ABOUT so the shell can focus
		// that window when it is clicked. The service re-emits other apps'
		// notifications, so it passes the original sender through in
		// Source; only it is trusted to do so (every other app's notify
		// takes the forward path below, which never reads Source).
		src := m.Source
		if src == "" {
			src = inst.InstanceID
		}
		// Key rides through untouched: it is the source app's own word
		// for what the toast is about, and the shell only ever hands it
		// back to that app (wire.EvtNotify.Key).
		out := wire.NewShellNotifyKeyed(src, m.Title, m.Body, m.Level, m.Key)
		var firstErr error
		for _, s := range inst.router.shellList() {
			if err := s.WriteCtrl(out); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
	// Run in a goroutine because resolveRecipient may spawn the
	// notify singleton on first reference — synchronous spawn would
	// stall the producer's read loop for the duration of the handshake.
	go inst.router.forwardNotifyToService(inst.AppID, inst.InstanceID, m)
	return nil
}

// NotifyAppID is the reserved app id for the notify service. Kept in
// the router (not imported from the notify package) so the router has
// no compile-time dependency on the service binary's package — a
// notify replacement could ship as a separate binary claiming the
// same id and the router would route to it without recompile.
const NotifyAppID = "com.wash.notify"

// forwardNotifyToService resolves the notify singleton (spawning it
// on demand if not yet running) and ships an `app_msg{kind:"notify"}`
// from the original producer. Best-effort: logs and returns on any
// failure — the shell broadcast already happened, the history surface
// is the secondary consumer.
func (r *Router) forwardNotifyToService(sourceAppID, sourceInstID string, m wire.EvtNotify) {
	target, _, err := r.resolveRecipient(context.Background(), wire.Recipient{AppID: NotifyAppID})
	if err != nil {
		// Not registered (or spawn failed). Background apps are best-
		// effort — toasts still fire either way.
		return
	}
	// Pass the payload as a typed map (not pre-marshalled bytes).
	// NewEvtAppMsgFrom calls mustJSON internally — handing it []byte
	// would round-trip through base64, which the receiver's decoder
	// then can't unmarshal back into a map.
	payload := map[string]any{
		"kind":  "notify",
		"title": m.Title,
		"body":  m.Body,
		"level": m.Level,
	}
	if m.Key != "" {
		payload["key"] = m.Key
	}
	from := wire.Sender{AppID: sourceAppID, InstanceID: sourceInstID}
	if err := target.WriteEvt(wire.NewEvtAppMsgFrom(target.WindowID, payload, from)); err != nil {
		r.log("notify forward: write: %v", err)
	}
}

func (inst *AppInstance) relayWindowTitle(m wire.EvtWindowSetTitle) error {
	if !inst.ownsWindow(m.Win) {
		return nil
	}
	inst.router.broadcastPatches(inst.router.winSession.setTitle(m.Win, m.Title))
	return nil
}

// relayWindowRaise brings an app's own window forward — the tail of a
// gesture the user made elsewhere (docs/AGENT_UX.md N1). Only for owned
// windows: an app may raise itself, never a neighbour.
//
// raiseInstanceWindow is the same helper launchOrRaise uses, so a raise
// ends in exactly the state a taskbar-pill click does: un-minimized,
// top of the stack, focused, with the focus/unfocus events relayed to
// the apps either side of the change. Focusing alone was not enough —
// it left a minimized window minimized, which is precisely the case a
// toast has to rescue you from.
func (inst *AppInstance) relayWindowRaise(m wire.EvtWindowRaise) error {
	if !inst.ownsWindow(m.Win) {
		return nil
	}
	inst.router.raiseWindow(inst, m.Win)
	return nil
}

// relayWindowAttention flags (or clears) "this window needs the human".
// Only for owned windows. The clear-on-focus half lives in the window
// session's focus path, so a window the user has actually looked at
// cannot stay lit no matter what the app claims.
func (inst *AppInstance) relayWindowAttention(m wire.EvtWindowAttention) error {
	if !inst.ownsWindow(m.Win) {
		return nil
	}
	inst.router.broadcastPatches(inst.router.winSession.setAttention(m.Win, m.On))
	return nil
}

// relayWindowGeometry applies an app-reported content-size change to the
// window's geometry so the shell frame tracks it. Used by wash-display
// when a guest surface resizes. Only honored for owned windows.
func (inst *AppInstance) relayWindowGeometry(m wire.EvtWindowGeometry) error {
	if !inst.ownsWindow(m.Win) {
		return nil
	}
	inst.router.broadcastPatches(inst.router.winSession.resize(m.Win, m.W, m.H))
	return nil
}

// relayWindowState applies an app-requested window state change (e.g. a CSD
// minimize button in a wash-display guest → "minimized"), gated to windows the
// app owns. setState validates the target and no-ops an unknown one
// (REVIEW-X11-WAYLAND H7).
func (inst *AppInstance) relayWindowState(m wire.EvtWindowState) error {
	if !inst.ownsWindow(m.Win) {
		return nil
	}
	inst.router.broadcastPatches(inst.router.winSession.setState(m.Win, m.State))
	return nil
}

// handleWindowCreate gives a CapWindows app another window beyond its
// primary one. The router allocates a window id, registers it under
// this instance (so byWin routing + teardown find it), adds it to the
// session, and replies with EvtWindowCreated. See docs/DISPLAY.md §4.
//
// role/parent_win are accepted but not yet honoured at the WM level —
// popups are created as ordinary toplevels for now; positioning a
// popup relative to its parent is a later shell-rendering change.
// envKeyRe allowlists publishable env keys: only WASH_*-namespaced names.
// Even a capable app can't set PATH / LD_PRELOAD via env.publish — the
// router silently drops anything that doesn't match. See docs/DISPLAY_ENV.md.
var envKeyRe = regexp.MustCompile(`^WASH_[A-Z0-9_]+$`)

// handleEnvPublish records WASH_*-namespaced env hints that spawnEnv then
// merges into every subsequently-spawned app's environment. Gated by the
// env-publish capability; non-matching keys are dropped. Used by
// wash-display to propagate DISPLAY / WAYLAND_DISPLAY.
func (inst *AppInstance) handleEnvPublish(m wire.EvtEnvPublish) error {
	if !inst.Manifest.HasCapability(CapEnvPublish) {
		return inst.WriteEvt(wire.NewEvtEnvPublishErr(wire.ErrCodeForbidden, "env-publish capability not declared"))
	}
	clean := make(map[string]string, len(m.Env))
	for k, v := range m.Env {
		if envKeyRe.MatchString(k) {
			clean[k] = v
		} else {
			inst.router.log("env.publish: dropped disallowed key %q from %s", k, inst.InstanceID)
		}
	}
	inst.router.publishedEnvMu.Lock()
	if inst.router.publishedEnv == nil {
		inst.router.publishedEnv = make(map[string]string, len(clean))
	}
	for k, v := range clean {
		inst.router.publishedEnv[k] = v
	}
	inst.router.publishedEnvMu.Unlock()
	// Name the keys, not just the count. wash-display publishes more than once
	// (geometry early, then the socket names once Xwayland is up), so "some
	// env.publish happened" tells a reader — or a test — nothing about whether
	// DISPLAY is in spawnEnv yet. Sorted for a stable line.
	keys := make([]string, 0, len(clean))
	for k := range clean {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	inst.router.log("env.publish from %s: %d key(s) keys=%s", inst.InstanceID, len(clean), strings.Join(keys, ","))
	return nil
}

func (inst *AppInstance) handleWindowCreate(m wire.EvtWindowCreate) error {
	if !inst.Manifest.HasCapability(CapWindows) {
		return inst.WriteEvt(wire.NewEvtWindowCreateErr(m.ReqID, wire.ErrCodeForbidden, "windows capability not declared"))
	}
	inst.winMu.Lock()
	n := len(inst.extraWins)
	inst.winMu.Unlock()
	if n >= maxWindowsPerInstance {
		return inst.WriteEvt(wire.NewEvtWindowCreateErr(m.ReqID, wire.ErrCodeForbidden, "window limit reached"))
	}

	win := inst.router.allocWindowID()
	title := m.Title
	if title == "" {
		title = inst.Manifest.Name
	}

	inst.router.mu.Lock()
	inst.router.byWin[win] = inst
	inst.router.mu.Unlock()

	inst.winMu.Lock()
	if inst.extraWins == nil {
		inst.extraWins = make(map[uint32]bool)
	}
	inst.extraWins[win] = true
	inst.winMu.Unlock()

	// A window may name its own custom-element tag (e.g. a video window
	// asking for the built-in "wash-app-display" decoder). Empty falls
	// back to the instance's manifest element — the common case.
	element := m.Element
	if element == "" {
		element = inst.Manifest.Element
	}
	inst.router.log("window.create instance=%s win=%d role=%q element=%q", inst.InstanceID, win, m.Role, element)
	// Per-window chromeless (m.Chromeless, set by wash-display for CSD
	// guests) OR the app-wide manifest hint.
	chromeless := m.Chromeless || (inst.Manifest.Window != nil && inst.Manifest.Window.Chromeless)
	inst.router.broadcastPatches(inst.router.winSession.createWindow(
		win, inst.InstanceID, element, inst.Manifest.Icon,
		inst.Manifest.Accent, title, m.W, m.H, m.MinW, m.MinH, m.MaxW, m.MaxH, inst.IsRoot(), chromeless))
	return inst.WriteEvt(wire.NewEvtWindowCreated(m.ReqID, win))
}

// handleWindowDestroy tears down one window the instance created via
// window.create. The primary handshake window can't be dropped this
// way (it dies with the instance); destroying a window the instance
// doesn't own is a no-op. Channels rooted at the window are closed.
func (inst *AppInstance) handleWindowDestroy(m wire.EvtWindowDestroy) error {
	if m.Win == inst.WindowID || !inst.ownsWindow(m.Win) {
		return nil
	}
	inst.winMu.Lock()
	delete(inst.extraWins, m.Win)
	inst.winMu.Unlock()

	inst.router.mu.Lock()
	delete(inst.router.byWin, m.Win)
	inst.router.mu.Unlock()

	inst.router.closeChannelsForWindow(m.Win, "window destroyed")
	inst.router.broadcastPatches(inst.router.winSession.destroyWindow(m.Win))
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

// handleOpenRequest resolves m.Path's extension to a registered handler app
// (manifest Opens) and spawns it with `--open <path>`. Gated on CapOpen.
// Fire-and-forget: an unhandled extension, protocol mismatch, or spawn
// failure is logged, not replied — the caller keeps its own fallback (e.g.
// fm's inline preview).
func (inst *AppInstance) handleOpenRequest(m wire.EvtOpenRequest) error {
	if !inst.Manifest.HasCapability(CapOpen) {
		inst.router.log("open: instance=%s lacks CapOpen", inst.InstanceID)
		return nil
	}
	target := inst.router.resolveOpen(m.Path)
	if target == nil {
		inst.router.log("open: no handler for %q", m.Path)
		return nil
	}
	if target.Manifest.ProtocolVersion != ProtocolVersion {
		inst.router.log("open: handler %s protocol mismatch", target.Manifest.ID)
		return nil
	}
	go inst.router.spawnForOpen(target, m.Path)
	return nil
}

// handleIdleInhibit records this instance's request that the router not
// self-exit for idleness.
//
// A release is honoured even without the capability: refusing to let an
// app STOP inhibiting could only ever pin a session longer, which is the
// failure mode with no upside.
func (inst *AppInstance) handleIdleInhibit(m wire.EvtIdleInhibit) error {
	if m.On && !inst.Manifest.HasCapability(CapIdleInhibit) {
		inst.router.log("idle-inhibit: instance=%s app=%s lacks CapIdleInhibit", inst.InstanceID, inst.Manifest.ID)
		return nil
	}
	inst.router.SetIdleInhibit(inst.InstanceID, m.On, m.Reason)
	if m.On {
		inst.router.log("idle-inhibit: held by app=%s instance=%s reason=%q", inst.Manifest.ID, inst.InstanceID, m.Reason)
	} else {
		inst.router.log("idle-inhibit: released by app=%s instance=%s", inst.Manifest.ID, inst.InstanceID)
	}
	return nil
}

// handlePrepareSpawn enforces the prepare_spawn capability, mints an
// instance id + attach token, and replies with a prepared
// EvtSpawnOk so the calling app can fork+exec the registered binary
// itself. The router does NOT spawn the binary — that's the
// caller's job, by design (the caller may want to wrap the exec in
// sudo, a uid switch, or a cgroup move). The dial-back is matched
// in handleAttach by AttachToken, and /proc/<pid>/exe is still
// checked against the registered binary path.
func (inst *AppInstance) handlePrepareSpawn(m wire.EvtSpawnRequest) error {
	if !inst.Manifest.HasCapability(CapPrepareSpawn) {
		return inst.WriteEvt(wire.NewEvtSpawnErrPrepared(m.ReqID, wire.ErrCodeForbidden, "prepare_spawn capability not declared"))
	}
	target := inst.router.reg.ByID(m.AppID)
	if target == nil || !target.Enabled() {
		return inst.WriteEvt(wire.NewEvtSpawnErrPrepared(m.ReqID, wire.ErrCodeNotFound, "unknown app id"))
	}
	if target.Manifest.ProtocolVersion != ProtocolVersion {
		return inst.WriteEvt(wire.NewEvtSpawnErrPrepared(m.ReqID, wire.ErrCodeIncompatibleProtocol, "protocol mismatch"))
	}
	tok, err := mintAttachToken()
	if err != nil {
		return inst.WriteEvt(wire.NewEvtSpawnErrPrepared(m.ReqID, wire.ErrCodeInternal, err.Error()))
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
	return inst.WriteEvt(wire.NewEvtSpawnOkPrepared(m.ReqID, target.Manifest.ID, instanceID, tok, target.Path))
}

// handleAppRestart enforces the restart capability and cycles a
// background singleton service: terminate the running instance, wait
// for its teardown (which GCs windows/channels/ingress), then spawn a
// fresh one. The reply (app.restart.ok / app.restart.err) rides this
// instance's event channel, keyed by ReqID. Runs in a goroutine so the
// terminate-wait + respawn-attach (each up to several seconds) doesn't
// block the reader. See docs/SETTINGS.md §5.
func (inst *AppInstance) handleAppRestart(m wire.EvtAppRestart) error {
	if !inst.Manifest.HasCapability(CapRestart) {
		inst.router.log("app %s: restart denied (no capability)", inst.AppID)
		return inst.WriteEvt(wire.NewEvtAppRestartErr(m.ReqID, wire.ErrCodeForbidden, "restart capability not declared"))
	}
	go func() {
		newID, code, err := inst.router.restartBackgroundApp(m.AppID)
		if err != nil {
			if werr := inst.WriteEvt(wire.NewEvtAppRestartErr(m.ReqID, code, err.Error())); werr != nil {
				inst.router.log("restart %s: err reply to instance=%s lost: %v (restart error was: %v)", m.AppID, inst.InstanceID, werr, err)
			}
			return
		}
		if werr := inst.WriteEvt(wire.NewEvtAppRestartOk(m.ReqID, newID)); werr != nil {
			inst.router.log("restart %s: ok reply to instance=%s lost: %v", m.AppID, inst.InstanceID, werr)
		}
	}()
	return nil
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
// EvtSpawnErr. Backed by the shared launchOrRaise, so re-launching an
// already-open single-window app raises it instead of duplicating it.
func (r *Router) spawnChild(target *Entry, requester *AppInstance) {
	inst, err := r.launchOrRaise(context.Background(), target)
	if err != nil {
		r.log("spawn %s: %v", target.Manifest.ID, err)
		if werr := requester.WriteEvt(wire.NewEvtSpawnErr(target.Manifest.ID, wire.ErrCodeInternal, err.Error())); werr != nil {
			r.log("spawn %s: err reply to instance=%s lost: %v", target.Manifest.ID, requester.InstanceID, werr)
		}
		return
	}
	if werr := requester.WriteEvt(wire.NewEvtSpawnOk(target.Manifest.ID, inst.InstanceID)); werr != nil {
		r.log("spawn %s: ok reply to instance=%s lost: %v (spawned instance=%s)", target.Manifest.ID, requester.InstanceID, werr, inst.InstanceID)
	}
}

// spawnForOpen launches an open-request target with `--open <path>`. Unlike
// spawnChild it sends no spawn ok/err back to the requester — open is
// fire-and-forget — so a failure is only logged.
func (r *Router) spawnForOpen(target *Entry, path string) {
	if _, err := r.spawnAndRun(context.Background(), target, false, "--open", path); err != nil {
		r.log("open: spawn %s for %q: %v", target.Manifest.ID, path, err)
	}
}

// requestClose initiates the X-style close handshake (WIRE.md §10).
// Returns when the app confirms (allow=true/false) or grace expires
// (force-close).
// win is the specific window being closed. For a multi-window instance
// (wash-display) this is the clicked window, not the instance's primary —
// a background instance's primary WindowID is 0, so sending the primary
// would mis-target the close and the guest would never get it (then the
// grace timer force-kills the whole instance). Falls back to the primary
// when win is 0 (ordinary single-window apps).
func (inst *AppInstance) requestClose(ctx context.Context, win uint32) (allowed bool, err error) {
	if win == 0 {
		win = inst.WindowID
	}
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

	if err := inst.WriteEvt(wire.NewEvtWindowCloseRequested(win)); err != nil {
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

// deliverCloseConfirm routes a window.confirm_close.
//
// Solicited — a close handshake is pending — hands the answer to
// requestClose's channel, and that's the whole story.
//
// UNSOLICITED with allow=true is the app asking to close ITSELF: wash-term
// when its last shell exits, or any app that vetoed the handshake to put a
// confirmation dialog on screen and now has its answer. This used to be
// dropped on the floor, so `exit` in a terminal's last tab left a dead empty
// window, and an app that wanted to ask before closing had no way to finish
// the job — apps/ai/be/app.go worked around it by ending its own process.
// Run the same teardown a confirmed titlebar click runs.
//
// Unsolicited allow=false is a no-op: "don't close me" answers nothing when
// nothing asked. (wash-display's guest vetoes are the only such traffic
// today, so nothing depends on it doing more.)
func (inst *AppInstance) deliverCloseConfirm(win uint32, allow bool) {
	inst.closeMu.Lock()
	ch := inst.closeConfirm
	inst.closeMu.Unlock()
	if ch != nil {
		select {
		case ch <- allow:
		default:
		}
		return
	}
	if !allow || inst.router == nil {
		return
	}
	if win == 0 {
		win = inst.WindowID
	}
	inst.router.approveWindowClose(inst, win)
}

// writeCtrl encodes m as JSON and writes a control-channel frame.
func (inst *AppInstance) writeCtrl(m any) error {
	data, err := wire.EncodeCtrl(m)
	if err != nil {
		return err
	}
	return inst.writeFrame(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: data})
}

// WriteEvt encodes m as JSON and writes an event-channel frame at
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

// appWriteTimeout bounds a single router→app frame write. A var (not const)
// so tests can shorten it. Matches wsWriteTimeout: generous enough that a
// merely slow app never trips it, tight enough that a genuinely wedged one
// (deadlocked, SIGSTOP'd, NFS-hung — not reading its socket) can't block the
// shell dispatch loop for long (REVIEW-DATAPATH F5/F6 / REVIEW-RECONNECT H3).
var appWriteTimeout = wsWriteTimeout

// deadlineFrameWriter is a transport that can bound a write (StreamTransport
// over the app's unix socket). In-process test pipes don't implement it and
// fall back to the plain, unbounded WriteFrame.
type deadlineFrameWriter interface {
	WriteFrameDeadline(wire.Frame, time.Time) error
}

func (inst *AppInstance) writeFrame(f wire.Frame) error {
	inst.writeMu.Lock()
	dw, ok := inst.Transport.(deadlineFrameWriter)
	if !ok {
		err := inst.Transport.WriteFrame(f)
		inst.writeMu.Unlock()
		return err
	}
	err := dw.WriteFrameDeadline(f, time.Now().Add(appWriteTimeout))
	inst.writeMu.Unlock()
	if err == nil {
		return nil
	}
	// The write timed out (or the conn is dead): treat the app as wedged and
	// tear the instance down the same way a dead conn is — close its transport
	// so its read loop exits and HandleApp runs tearDown. Crucially we DON'T
	// propagate the error to the caller: a stuck app must not reap the healthy
	// shell connection that was merely relaying to it (the shell dispatch loop
	// would otherwise return this error and tear itself down). The frame is
	// dropped, which is fine — the app is going away.
	if inst.router != nil {
		inst.router.log("app %s inst=%s: write wedged (%v) — tearing down instance", inst.AppID, inst.InstanceID, err)
	}
	_ = inst.Transport.Close()
	return nil
}

// writeRawFrame forwards bare bytes back to the app on a dynamic
// channel. Mirror of ShellSession.WriteRawFrame.
func (inst *AppInstance) writeRawFrame(channelID uint32, payload []byte) error {
	return inst.writeFrame(wire.Frame{Flags: wire.FlagEnd, Channel: channelID, Payload: payload})
}
