package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirmick/wash/internal/wire"
)

// ShellSession is the per-shell connection state. The router keeps
// many (v0.0 in practice keeps one).
type ShellSession struct {
	Transport FrameTransport

	router  *Router
	writeMu sync.Mutex

	// declared is guarded by writeMu (set when announcing, cleared
	// when undeclaring) — kept under the same lock as writes so a
	// declare and any follow-on relay are observed in order by the
	// receiver.
	declared map[string]bool

	// bundleSent is guarded by writeMu — which instances' app bundles
	// have already been shipped to THIS shell. Both the snapshot replay
	// (HandleShell) and declareAppToAllShells can target the same
	// shell+instance, and delivering the bundle twice runs the app's
	// customElements.define() twice (a thrown "already defined"). Deliver
	// once per shell+instance; reconnect is a fresh ShellSession.
	bundleSent map[string]bool

	// scheduler holds frames bound for the FE in per-class queues
	// (see internal/router/qos.go and docs/QOS.md). Producers
	// (writeCtrlLocked, WriteRawFrame, broadcastPatches) call
	// Submit; one drainer goroutine pulls in strict-priority order
	// and writes to Transport. nil before HandleShell wires it up.
	scheduler *Scheduler
	// drainerDone is closed by the drainer goroutine on exit so
	// HandleShell can wait for it during teardown.
	drainerDone chan struct{}

	// peerChannels tracks this shell's remote-apps relay channels
	// (docs/REMOTE.md), channel id → binding, so they're torn down (socket
	// closed, pump unblocked) when the shell disconnects. Guarded by peerMu.
	peerMu       sync.Mutex
	peerChannels map[uint32]*channelBinding

	// lastReadAtNanos is the wall-clock time (UnixNano) of the most recent
	// inbound frame of any kind — keystrokes, window intents, pings. The
	// read-idle watchdog (readIdleLoop) reaps the connection when this goes
	// stale. Atomic: written by the read goroutine (dispatch), read by the
	// watchdog goroutine.
	lastReadAtNanos atomic.Int64
	// sawPing arms the watchdog. It flips true on the first wire.TShellPing,
	// so a heartbeat-less legacy FE is never reaped for idleness. Atomic for
	// the same cross-goroutine reason as lastReadAtNanos.
	sawPing atomic.Bool
	// connID is a short per-connection id for correlating the connect /
	// disconnect / reap log lines of one browser session.
	connID uint64
}

// declareInstance sends ShellAppDeclared (and ShellWindowCreate for
// windowed apps) for inst, exactly once per ShellSession. Concurrent
// callers race safely — the second is a no-op.
//
// The dedupe and the writes both run under writeMu, so a parallel
// declareExistingAppsTo holding the lock keeps relays from squeezing
// in between declared+create and any follow-on title/focus relay.
func (s *ShellSession) declareInstance(inst *AppInstance) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.declareInstanceLocked(inst)
}

// declareInstanceLocked assumes writeMu is held by the caller.
//
// Sends app.declared so the shell starts fetching the bundle. The
// window itself comes via session.snapshot / session.patch — the
// router owns geometry, not declare.
func (s *ShellSession) declareInstanceLocked(inst *AppInstance) error {
	if s.declared == nil {
		s.declared = make(map[string]bool)
	}
	if s.declared[inst.InstanceID] {
		return nil
	}
	s.declared[inst.InstanceID] = true

	manifestJSON, err := json.Marshal(inst.Manifest)
	if err != nil {
		return err
	}
	surface := inst.Manifest.Surface
	if inst.Kiosk {
		surface = SurfaceDesktop
	}
	return s.writeCtrlLocked(wire.NewShellAppDeclared(
		inst.InstanceID,
		inst.Manifest.Element,
		surface,
		manifestJSON,
	))
}

// undeclareInstance forgets inst so a future declare can fire again
// (e.g. if instancing logic permits a fresh handshake).
func (s *ShellSession) undeclareInstance(instanceID string) {
	s.writeMu.Lock()
	delete(s.declared, instanceID)
	delete(s.bundleSent, instanceID)
	s.writeMu.Unlock()
}

// HandleShell takes ownership of t for the lifetime of a browser
// shell's connection: ensures the session app is running, declares
// already-running instances to it (so a late-connecting shell sees
// the world), then runs the frame loop.
func (r *Router) HandleShell(ctx context.Context, t FrameTransport) error {
	sess := &ShellSession{
		Transport:    t,
		router:       r,
		scheduler:    NewScheduler(),
		drainerDone:  make(chan struct{}),
		peerChannels: make(map[uint32]*channelBinding),
	}
	defer t.Close()
	// Close any remote-apps relay sockets this shell opened, so a browser
	// disconnect tears down its ssh -L'd peer connections (and unblocks
	// their pump goroutines) instead of leaking them.
	defer sess.closeAllPeers()
	sess.connID = r.connectCount.Add(1)
	sess.lastReadAtNanos.Store(time.Now().UnixNano())
	connStart := time.Now()
	r.log("shell: connect conn=%d", sess.connID)
	defer func() {
		// Stop the drainer first so it doesn't try to write to a
		// closing transport, then wait for it to exit.
		sess.scheduler.Close()
		<-sess.drainerDone
		// Bank this connection's counters into the session running totals
		// so the desktop info panel + About survive the disconnect.
		snap := sess.scheduler.StatsSnapshot()
		r.linkTotals.add(snap)
		// Disconnect summary: the single most useful line for "did my
		// laptop-suspend drop the connection, and was anything in flight?"
		r.log("shell: disconnect conn=%d dur=%s rx_frames=%d tx_frames=%d idle=%s",
			sess.connID, time.Since(connStart).Round(time.Millisecond),
			snap.RxFrames, sumU64(snap.TxFrames[:]),
			time.Since(time.Unix(0, sess.lastReadAtNanos.Load())).Round(time.Millisecond))
	}()
	go sess.drainLoop(ctx)
	go sess.readIdleLoop(ctx)

	// Hold writeMu for the whole setup. While we hold it, any
	// concurrent HandleApp.declareInstance blocks at the same mutex,
	// so the receiver sees catalog → declared → create in order
	// regardless of which goroutine got there first.
	sess.writeMu.Lock()
	if err := sess.writeCtrlLocked(wire.NewShellCatalog(r.catalog(), r.panelCatalog())); err != nil {
		sess.writeMu.Unlock()
		return err
	}
	r.registerShell(sess)
	defer r.unregisterShell(sess)

	// Snapshot apps under the router lock; declare them while we
	// still hold writeMu so any racing HandleApp is correctly
	// deduped. Order matters: app.declared (one per app) must arrive
	// before the session.snapshot so the shell has bundles in flight
	// by the time it sees window upserts.
	r.mu.Lock()
	snapshot := make([]*AppInstance, 0, len(r.apps))
	for _, inst := range r.apps {
		snapshot = append(snapshot, inst)
	}
	r.mu.Unlock()
	for _, inst := range snapshot {
		if err := sess.declareInstanceLocked(inst); err != nil {
			sess.writeMu.Unlock()
			return err
		}
	}
	wins, appState := r.winSession.snapshot()
	if err := sess.writeCtrlLocked(wire.NewShellSessionSnapshot(wins, appState)); err != nil {
		sess.writeMu.Unlock()
		return err
	}
	sess.writeMu.Unlock()

	// After the snapshot, take ownership of every still-detached raw
	// channel (PTYs from a previous shell session, etc.) and replay
	// the scrollback so the user lands on their pre-refresh terminal.
	r.reattachChannelsToShell(sess)

	// Replay any already-cached bundles to the new shell.
	for _, inst := range snapshot {
		r.replayBundleToShell(sess, inst)
	}

	if err := r.EnsureSessionRunning(ctx); err != nil {
		r.log("ensure session: %v", err)
	}
	r.EnsureBackgroundAppsRunning(ctx)
	if err := r.EnsureInitialAppRunning(ctx); err != nil {
		r.log("ensure initial: %v", err)
	}
	go sess.linkStatsLoop(ctx)
	return sess.loop(ctx)
}

// linkStatsLoop pushes a link.stats telemetry frame to the FE ~1/s while
// the connection is up (the desktop info panel + About read it). Control
// class via SubmitTelemetry: non-blocking, never counted as an app drop,
// and reliably delivered so health data still arrives under load.
func (s *ShellSession) linkStatsLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.drainerDone:
			return
		case <-t.C:
			s.emitLinkStats()
		}
	}
}

func (s *ShellSession) emitLinkStats() {
	if s.router == nil || s.scheduler == nil {
		return
	}
	live := s.scheduler.StatsSnapshot()
	msg := wire.NewShellLinkStats(
		live,
		s.router.sessionLinkTotals(live),
		s.router.connectCount.Load(),
		time.Since(s.router.started).Milliseconds(),
	)
	data, err := wire.EncodeCtrl(msg)
	if err != nil {
		return
	}
	f := wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: data}.WithClass(wire.ClassControl)
	s.scheduler.SubmitTelemetry(f)
}

// handlePing echoes a heartbeat. The pong rides ClassControl (like the
// link-stats telemetry) so it is delivered ahead of any bulk traffic and
// the FE's round-trip timing reflects liveness, not queueing depth. Seq is
// echoed verbatim; the router holds no per-ping state. The first ping also
// arms the read-idle watchdog (see readIdleLoop) — a connection that speaks
// the heartbeat is one we may reap for silence.
func (s *ShellSession) handlePing(m wire.ShellPing) error {
	if s.sawPing.CompareAndSwap(false, true) {
		s.router.log("shell: heartbeat armed conn=%d", s.connID)
	}
	return s.WriteCtrlClass(wire.NewShellPong(m.Seq), wire.ClassControl)
}

// readIdleLoop reaps a shell connection the OS froze without closing — the
// laptop-suspend zombie (docs/PTY_ROBUST.md, Fix D). Once the FE has proved
// it heartbeats (sawPing), a gap longer than readIdleTimeout since the last
// inbound frame means the socket is dead; we close the transport, which
// unblocks the read loop and runs the normal teardown. Until the first ping
// the watchdog is dormant, so a legacy FE is never falsely reaped.
func (s *ShellSession) readIdleLoop(ctx context.Context) {
	t := time.NewTicker(readIdleCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.drainerDone:
			return
		case <-t.C:
			if s.idleReapDue(time.Now(), readIdleTimeout) {
				idle := time.Since(time.Unix(0, s.lastReadAtNanos.Load()))
				s.router.log("shell: read-idle reap conn=%d idle=%s — closing zombie connection",
					s.connID, idle.Round(time.Millisecond))
				_ = s.Transport.Close()
				return
			}
		}
	}
}

// idleReapDue is the watchdog's decision, factored out so it can be tested
// without timing. False until the FE has proved it heartbeats (sawPing) —
// that guard is what keeps a legacy, ping-less FE from being reaped during a
// quiet stretch.
func (s *ShellSession) idleReapDue(now time.Time, timeout time.Duration) bool {
	if !s.sawPing.Load() {
		return false
	}
	last := s.lastReadAtNanos.Load()
	if last == 0 {
		return false
	}
	return now.Sub(time.Unix(0, last)) > timeout
}

// sumU64 totals a slice of counters (the per-class TxFrames array) for a
// single human-readable figure in the disconnect summary.
func sumU64(xs []uint64) uint64 {
	var n uint64
	for _, x := range xs {
		n += x
	}
	return n
}

func (s *ShellSession) loop(ctx context.Context) error {
	err := wire.ReadLoop(ctx, s.Transport, s.dispatch)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (s *ShellSession) dispatch(f wire.Frame) error {
	// Ingress accounting (every inbound frame: keystrokes, window intents,
	// uploaded file content) for the link-health rx figure.
	s.statsLink().recordRx(len(f.Payload))
	// Liveness: any inbound frame proves the socket is alive. Stamp it for
	// the read-idle watchdog (readIdleLoop) so a busy connection — even one
	// sending only bulk uploads — never trips the zombie reaper.
	s.lastReadAtNanos.Store(time.Now().UnixNano())
	if f.Channel != ChannelControl {
		// Channel ≥ 1 on WS: raw byte stream. Forward to the bound
		// app verbatim on the same channel id.
		b := s.router.lookupChannel(f.Channel)
		if b == nil {
			s.router.log("shell: drop raw frame on unbound channel %d", f.Channel)
			return nil
		}
		// Authoritative ownership (docs/PTY_ROBUST.md, Fix A; supersedes
		// the RECONNECT-AUDIT A4 band-aid): the foreground head shell is
		// the single driver of every non-peer terminal channel. A frame
		// from the head always routes — it adopts the channel on the spot
		// — so a stale/zombie owner (a lingering shell, or a remote-peer
		// attach that re-shuffled ownership) can never black-hole the
		// connection the user is looking at, the symptom behind the local
		// terminal "going black" on connect/disconnect churn. An orphaned
		// channel (owner==nil) is adopted by whoever drives it next. A
		// non-head background shell's input is dropped below: the head,
		// not a cached pointer, decides ownership. Peer (remote-relay)
		// channels are exempt — their pump is pinned to a specific shell
		// and must not be stolen. isHead() is sampled before shellMu
		// because it takes r.mu, and r.mu must never be acquired while
		// holding shellMu.
		head := s.isHead()
		b.shellMu.Lock()
		owner := b.shell
		if owner != s && b.peerConn == nil && (owner == nil || head) {
			b.shell = s
			owner = s
		}
		b.shellMu.Unlock()
		if owner != s {
			s.router.log("shell: drop raw frame on channel %d (owned by another shell, sender not head)", f.Channel)
			return nil
		}
		// Remote-apps relay (docs/REMOTE.md): a peer channel's endpoint is
		// the ssh -L'd socket, not an app. Write the browser's bytes (host
		// B's wire) verbatim — A never decodes them.
		if b.peerConn != nil {
			if _, err := b.peerConn.Write(f.Payload); err != nil {
				s.router.closeChannel(f.Channel, "peer write: "+err.Error())
			}
			return nil
		}
		return b.app.writeRawFrame(f.Channel, f.Payload)
	}
	msg, err := wire.DecodeCtrl(f.Payload)
	if err != nil {
		return fmt.Errorf("shell ctrl decode: %w", err)
	}
	switch m := msg.(type) {
	case wire.ShellWindowCloseClicked:
		return s.handleWindowCloseClicked(m)
	case wire.ShellWindowFocus:
		return s.handleWindowFocus(m)
	case wire.ShellWindowMove:
		return s.handleWindowMove(m)
	case wire.ShellWindowResize:
		return s.handleWindowResize(m)
	case wire.ShellWindowState:
		return s.handleWindowState(m)
	case wire.ShellAppMsgSend:
		// Deliberately not logged here: this is the FE→BE hot path.
		// Failures are logged in handleAppMsgSend; successes are noise.
		return s.handleAppMsgSend(m, f.Class())
	case wire.ShellLaunch:
		return s.handleLaunch(m)
	case wire.ShellPeerAttach:
		return s.handlePeerAttach(m)
	case wire.ShellPeerDetach:
		s.detachPeer(m.Origin)
		return nil
	case wire.ShellPing:
		return s.handlePing(m)
	case wire.ShellLog:
		return s.handleShellLog(m)
	case wire.ShellChannelCredit:
		return s.handleChannelCredit(m)
	case wire.ShellAssetRead:
		return s.handleAssetRead(m)
	case wire.ShellPanelRead:
		return s.handlePanelRead(m)
	case wire.ShellClipboardSet:
		return s.handleShellClipboardSet(m)
	case wire.ShellClipboardGet:
		return s.handleShellClipboardGet(m)
	}
	s.router.log("shell: unexpected ctrl msg %T", msg)
	return nil
}

// handleAssetRead serves a single file from the router's embedded
// asset FS (set by SetAssets) to the shell over a freshly-allocated
// Kind=ChannelKindAsset channel. Closes the channel with ChannelUnbind
// when the file is fully written. On error (no FS configured, path
// missing, traversal attempt, read failure) a ShellAssetReadErr is
// sent back and no channel is opened.
func (s *ShellSession) handleAssetRead(m wire.ShellAssetRead) error {
	if s.router.assets == nil {
		return s.WriteCtrl(wire.NewShellAssetReadErr(m.ReqID, wire.ErrCodeInternal, "no asset fs"))
	}
	// Normalise: strip leading slashes, reject any segment that's "..".
	// http.FileSystem.Open with a path containing ".." can still escape
	// http.Dir wrappers; we belt-and-braces it.
	clean := path.Clean("/" + strings.TrimLeft(m.Path, "/"))
	if clean == "/" || strings.Contains(clean, "/..") {
		return s.WriteCtrl(wire.NewShellAssetReadErr(m.ReqID, wire.ErrCodeBadRequest, "bad path"))
	}
	// loadAsset returns cached identity+gzip bytes (read + compressed once
	// per process, keyed by path + stat signature).
	entry, err := s.router.loadAsset(clean)
	if err != nil {
		if err == errAssetIsDir {
			return s.WriteCtrl(wire.NewShellAssetReadErr(m.ReqID, wire.ErrCodeBadRequest, "is a directory"))
		}
		if isNotExist(err) {
			return s.WriteCtrl(wire.NewShellAssetReadErr(m.ReqID, wire.ErrCodeNotFound, err.Error()))
		}
		return s.WriteCtrl(wire.NewShellAssetReadErr(m.ReqID, wire.ErrCodeInternal, err.Error()))
	}
	// Send compressed when the cache decided gzip is a win; else identity.
	// The FE inflates per the Encoding field (wash-fetch.ts).
	payload := entry.raw
	encoding := ""
	if entry.gz != nil {
		payload = entry.gz
		encoding = "gzip"
	}
	id := s.router.allocChannelID()
	if err := s.WriteCtrl(wire.NewShellAssetReadOK(m.ReqID, id, int64(len(payload)), entry.mime, encoding)); err != nil {
		return err
	}
	// Bind so the shell knows the channel id maps to an asset stream.
	if err := s.WriteCtrl(wire.ShellChannelBind{
		T:         wire.TShellChannelBind,
		ChannelID: id,
		Kind:      wire.ChannelKindAsset,
	}); err != nil {
		return err
	}
	// Stream in chunks, slicing the cached (immutable) buffer. Background
	// class: assets are behind the desktop's gradient fallback, so they
	// yield to keystrokes, control, and the bundles that gate a launching
	// window. The FE completes on byte-count (the Size above), so the
	// higher-priority Unbind overtaking these frames is harmless
	// (docs/QOS.md tc reclass).
	const chunkSize = 64 * 1024
	for off := 0; off < len(payload); off += chunkSize {
		end := off + chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		if werr := s.WriteRawFrameClass(id, payload[off:end], wire.ClassBackground); werr != nil {
			return werr
		}
	}
	// Account raw vs on-the-wire bytes for the compression-ratio readout.
	s.statsLink().recordCompression(len(entry.raw), len(payload))
	return s.WriteCtrl(wire.NewShellChannelUnbind(id, "asset complete"))
}

// handlePanelRead streams an app's cached settings-panel bundle
// (Entry.PanelBundle) to the shell over a freshly-allocated
// Kind=ChannelKindBundle channel — the same blob-import path app
// bundles take, so the shell can customElements.define the panel
// element without spawning the owning app. Mirrors handleAssetRead but
// keyed by app id and served from the registry. ErrCodeNotFound when
// the app is absent, disabled, or declares no panel.
func (s *ShellSession) handlePanelRead(m wire.ShellPanelRead) error {
	entry := s.router.reg.ByID(m.AppID)
	if entry == nil || !entry.Enabled() || entry.Manifest.SettingsPanel == nil {
		return s.WriteCtrl(wire.NewShellPanelReadErr(m.ReqID, wire.ErrCodeNotFound, "no such panel"))
	}
	bundle := entry.PanelBundle
	if len(bundle) == 0 {
		// Declared a panel but shipped no panel.js — a build error on
		// the app's side. Surface it rather than mounting an empty tag.
		return s.WriteCtrl(wire.NewShellPanelReadErr(m.ReqID, wire.ErrCodeInternal, "panel bundle empty"))
	}
	id := s.router.allocChannelID()
	if err := s.WriteCtrl(wire.NewShellPanelReadOK(m.ReqID, id, int64(len(bundle)))); err != nil {
		return err
	}
	// Bind as a bundle channel so the shell accumulates + blob-imports.
	if err := s.WriteCtrl(wire.ShellChannelBind{
		T:         wire.TShellChannelBind,
		ChannelID: id,
		Kind:      wire.ChannelKindBundle,
	}); err != nil {
		return err
	}
	// Same Interactive class as bundle/asset delivery so the strict-
	// priority scheduler can't let the Unbind overtake the data frames.
	const chunkSize = 256 * 1024
	for off := 0; off < len(bundle); off += chunkSize {
		end := off + chunkSize
		if end > len(bundle) {
			end = len(bundle)
		}
		if err := s.WriteRawFrameClass(id, bundle[off:end], wire.ClassInteractive); err != nil {
			return err
		}
	}
	return s.WriteCtrl(wire.NewShellChannelUnbind(id, "panel complete"))
}

// handleChannelCredit applies an FE-issued credit grant to the
// matching channel's ledger. Best-effort: an unknown channel id
// means the channel has been closed since the FE sent the grant,
// which is benign — log and ignore (the FE will stop sending
// credits once it sees the ChannelUnbind).
func (s *ShellSession) handleChannelCredit(m wire.ShellChannelCredit) error {
	b := s.router.lookupChannel(m.ChannelID)
	if b == nil || b.credit == nil {
		// Channel closed already; FE's grant is stale. Not an error.
		return nil
	}
	if err := b.credit.Grant(m.N); err != nil {
		// Overflow: FE is buggy or malicious. Close the channel
		// rather than tearing down the whole shell connection.
		s.router.log("channel %d: credit overflow, closing: %v", m.ChannelID, err)
		s.router.closeChannel(m.ChannelID, wire.ErrCodeCreditOverflow)
		return nil
	}
	// The FE granted credit — it's keeping up again. If this channel went
	// behind during a wedge (live output suppressed to avoid a torn
	// stream), resync it now: a clean reset + realigned snapshot, after
	// which live forwarding resumes. No-op if the channel isn't behind.
	// (docs/PTY_ROBUST.md, Fix B)
	s.router.resyncChannel(b)
	return nil
}

func (s *ShellSession) handleShellLog(m wire.ShellLog) error {
	src := m.Source
	if src == "" {
		src = "shell"
	}
	if m.Stack != "" {
		s.router.log("browser/%s [%s] %s\n%s", src, m.Level, m.Msg, m.Stack)
	} else {
		s.router.log("browser/%s [%s] %s", src, m.Level, m.Msg)
	}
	return nil
}

func (s *ShellSession) handleWindowCloseClicked(m wire.ShellWindowCloseClicked) error {
	s.router.mu.Lock()
	inst := s.router.byWin[m.WindowID]
	s.router.mu.Unlock()
	if inst == nil {
		return nil
	}
	// Drive the close handshake in a goroutine — the loop must keep
	// reading so confirm_close can arrive.
	go func() {
		allowed, err := inst.requestClose(context.Background(), m.WindowID)
		if err != nil {
			s.router.log("close window %d: %v", m.WindowID, err)
			return
		}
		if allowed {
			// Tell shells the window is gone now. The app's loop
			// teardown will also call destroyWindow when it exits;
			// the second call is a no-op (already deleted).
			s.router.broadcastPatches(s.router.winSession.destroyWindow(m.WindowID))
			// expectedExit suppresses the crash-broadcast in the
			// cleanup goroutine — a user-clicked close the app
			// confirmed is an orderly exit, not a tombstone-worthy
			// crash. Set BEFORE signalling.
			inst.expectedExit.Store(true)
			if inst.Cmd != nil && inst.Cmd.Process != nil {
				// Spawn-completion branch: router forked the child
				// directly and owns *exec.Cmd. SIGTERM gracefully;
				// the app's read loop sees EOF after the signal.
				_ = inst.Cmd.Process.Signal(stopSignal())
			} else {
				// Token-attach branch: the child was forked by an
				// external spawner (e.g. wash-priv under sudo). We
				// don't have an *exec.Cmd to signal, and in the
				// non-embedded case wouldn't have permission to
				// SIGTERM the root child anyway. Closing the
				// transport is the unprivileged equivalent: the
				// app's read loop sees EOF, sdk.Run returns, main()
				// returns, the process exits, and the spawner's
				// cmd.Wait unblocks (so wash-priv's queue row
				// transitions Running → Done).
				_ = inst.Transport.Close()
			}
		}
	}()
	return nil
}

// handleWindowFocus updates router state and tells the affected apps
// about focus/unfocus on their event channels. Focus state is global
// to the session — flipping it in one shell propagates to all shells
// via the broadcast patch.
func (s *ShellSession) handleWindowFocus(m wire.ShellWindowFocus) error {
	// Snapshot previous focused window before the mutation so we can
	// emit EvtWindowUnfocus to whoever lost it.
	prev := s.router.winSession.focusedWindowID()
	patches := s.router.winSession.focus(m.WindowID)
	if len(patches) == 0 {
		return nil
	}
	s.router.broadcastPatches(patches)
	if prev != 0 && prev != m.WindowID {
		s.router.mu.Lock()
		prevInst := s.router.byWin[prev]
		s.router.mu.Unlock()
		if prevInst != nil {
			if err := prevInst.WriteEvt(wire.NewEvtWindowUnfocus(prev)); err != nil {
				s.router.log("unfocus relay: %v", err)
			}
		}
	}
	s.router.mu.Lock()
	inst := s.router.byWin[m.WindowID]
	s.router.mu.Unlock()
	if inst == nil {
		return nil
	}
	return inst.WriteEvt(wire.NewEvtWindowFocus(m.WindowID))
}

func (s *ShellSession) handleWindowMove(m wire.ShellWindowMove) error {
	s.router.broadcastPatches(s.router.winSession.move(m.WindowID, m.X, m.Y))
	// No EvtWindowMove on the app side yet — apps that care about
	// position would need a new event; nothing requests it today.
	return nil
}

func (s *ShellSession) handleWindowResize(m wire.ShellWindowResize) error {
	s.router.broadcastPatches(s.router.winSession.resize(m.WindowID, m.W, m.H))
	s.router.mu.Lock()
	inst := s.router.byWin[m.WindowID]
	s.router.mu.Unlock()
	if inst == nil {
		return nil
	}
	return inst.WriteEvt(wire.NewEvtWindowResize(m.WindowID, m.W, m.H))
}

func (s *ShellSession) handleWindowState(m wire.ShellWindowState) error {
	switch m.State {
	case wire.WindowStateNormal, wire.WindowStateMinimized, wire.WindowStateMaximized:
	default:
		s.router.log("shell: invalid window.state %q", m.State)
		return nil
	}
	s.router.broadcastPatches(s.router.winSession.setState(m.WindowID, m.State))
	s.router.mu.Lock()
	inst := s.router.byWin[m.WindowID]
	s.router.mu.Unlock()
	if inst == nil {
		return nil
	}
	return inst.WriteEvt(wire.NewEvtWindowState(m.WindowID, m.State))
}

// handleAppMsgSend relays a shell-originated app_msg. Two cases:
//
//   - To is nil: relay to the FE's own BE half (look up by
//     InstanceID).
//   - To is set: cross-app send; resolve the recipient and relay as
//     that instance's normal EvtAppMsg event.
//
// Data ships verbatim as json.RawMessage — no decode hop. class is
// the priority class the FE stamped on its frame; we preserve it on
// the BE-bound event frame so the SDK sees the same class on read.
func (s *ShellSession) handleAppMsgSend(m wire.ShellAppMsgSend, class wire.Class) error {
	if m.To != nil {
		target, code, err := s.router.resolveRecipient(context.Background(), *m.To)
		if err != nil {
			s.router.log("shell app_msg cross-instance: %s: %v", code, err)
			return nil
		}
		return target.WriteEvtClass(wire.NewEvtAppMsg(target.WindowID, m.Data), class)
	}
	inst := s.router.appByInstance(m.InstanceID)
	if inst == nil {
		// FE message for an instance that's gone (or never existed) —
		// the sender gets no reply and no error, so this line is the
		// only trace.
		s.router.log("shell app_msg: dropping message for unknown instance=%s", m.InstanceID)
		return nil
	}
	return inst.WriteEvtClass(wire.NewEvtAppMsg(inst.WindowID, m.Data), class)
}

// handleLaunch spawns an app by id on behalf of the shell (docs/REMOTE.md
// §6.1). This is the no-session-BE launch path: host B runs --no-session,
// so wash-connect can't route a launcher click through a session app and
// asks B's router directly. Mirrors controlLaunch (control.go) — refuse
// desktop-surface (the autoboot session owns the desktop), route
// background singletons through resolveRecipient, spawn the rest — but
// fire-and-forget: success surfaces as the usual app.declared + window,
// and there is no response frame, so failures are logged here only.
func (s *ShellSession) handleLaunch(m wire.ShellLaunch) error {
	if m.AppID == "" {
		s.router.log("shell launch: missing app_id")
		return nil
	}
	entry := s.router.reg.ByID(m.AppID)
	if entry == nil || !entry.Enabled() {
		s.router.log("shell launch %s: unknown or disabled app", m.AppID)
		return nil
	}
	if entry.Manifest.ProtocolVersion != ProtocolVersion {
		s.router.log("shell launch %s: protocol mismatch", m.AppID)
		return nil
	}
	if entry.Manifest.Surface == SurfaceDesktop {
		s.router.log("shell launch %s: refusing desktop-surface app", m.AppID)
		return nil
	}
	if entry.Manifest.Surface == SurfaceBackground {
		// Singleton table is consulted first (returns the running one),
		// spawning on demand otherwise — same as controlLaunch.
		if _, code, err := s.router.resolveRecipient(context.Background(), wire.Recipient{AppID: m.AppID}); err != nil {
			s.router.log("shell launch %s: %s: %v", m.AppID, code, err)
		}
		return nil
	}
	go func() {
		// launchOrRaise: a launcher click on an already-open single-window
		// app surfaces it instead of spawning a duplicate.
		if _, err := s.router.launchOrRaise(context.Background(), entry); err != nil {
			s.router.log("shell launch %s: %v", m.AppID, err)
		}
	}()
	return nil
}

// WriteCtrl encodes m as JSON and writes a shell control-channel frame.
//
// No writeMu: the scheduler's channel provides ordering for concurrent
// callers, and holding the declared-map mutex across a potentially
// blocking Submit would head-of-line block every other producer (in
// particular, a Bulk flood under WriteRawFrame would freeze all
// Interactive control traffic). The mutex only protects the
// declared map's mutations in declareInstance.
//
// Default class is Interactive — see WriteCtrlClass for the explicit
// variant used when relaying app→shell traffic that originated as
// Bulk on the app side.
func (s *ShellSession) WriteCtrl(m any) error {
	return s.writeCtrlLocked(m)
}

// WriteCtrlClass is WriteCtrl with an explicit priority class. Used
// when relaying EvtAppMsg / EvtAppMsgFrom payloads so the class the
// originating app stamped on its event channel frame propagates
// through to the FE-bound delivery.
func (s *ShellSession) WriteCtrlClass(m any, class wire.Class) error {
	data, err := wire.EncodeCtrl(m)
	if err != nil {
		return err
	}
	f := wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: data}.WithClass(class)
	if s.scheduler == nil {
		return s.Transport.WriteFrame(f)
	}
	return s.scheduler.Submit(context.Background(), f)
}

// writeCtrlLocked is used by callers inside HandleShell setup that
// already hold writeMu (for declared-map atomicity). The Submit it
// performs does not itself require the mutex — safe under or without
// writeMu.
//
// Control-channel frames are router-originated lifecycle messages
// (app.declared, session.snapshot, session.patch, window.create,
// shell.reload, error envelopes) — Interactive class by default; the
// scheduler keeps them ahead of bulk traffic on the same connection.
func (s *ShellSession) writeCtrlLocked(m any) error {
	data, err := wire.EncodeCtrl(m)
	if err != nil {
		return err
	}
	f := wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: data}
	if s.scheduler == nil {
		// Pre-HandleShell setup (e.g. tests) or post-teardown
		// fallback. Direct write preserves the legacy semantics.
		return s.Transport.WriteFrame(f)
	}
	return s.scheduler.Submit(context.Background(), f)
}

// WriteRawFrame writes a bare-byte frame on a dynamic channel. The
// router calls this when forwarding raw bytes from an app to its
// bound shell — pty output, file content streams, anything large.
// Defaults to Bulk class so it yields to user-interactive frames
// from other channels.
//
// Bundle/replay-style transactional flows (Bind → raw … → Unbind)
// must use WriteRawFrameClass with ClassInteractive instead — Bulk
// would let the Interactive Unbind overtake the data frames under
// strict priority, breaking the transaction.
//
// No writeMu: see WriteCtrl. The scheduler's channel handles
// concurrent-producer ordering.
func (s *ShellSession) WriteRawFrame(channelID uint32, payload []byte) error {
	return s.WriteRawFrameClass(channelID, payload, wire.ClassBulk)
}

// WriteRawFrameClass is WriteRawFrame with an explicit class. Used
// for raw channels whose framing is transactional (bundle delivery,
// scrollback replay on reattach) so all frames in the transaction
// drain at the same priority and no Interactive control frame slips
// between them.
//
// Bulk-class writes consume per-channel credit (docs/QOS.md §5):
// the call blocks until the FE has granted enough bytes via
// channel.credit. Interactive-class writes bypass the credit check
// — transactional flows aren't paced by the FE.
func (s *ShellSession) WriteRawFrameClass(channelID uint32, payload []byte, class wire.Class) error {
	// Credit gate (Bulk only; Interactive is transactional).
	if class == wire.ClassBulk && s.router != nil {
		if b := s.router.lookupChannel(channelID); b != nil && b.credit != nil {
			n := uint64(len(payload))
			// Fast path: credit available now. Otherwise the FE hasn't
			// granted enough bytes yet — record the credit stall and time
			// how long the producer is parked (the backpressure signal).
			if !b.credit.TryReserve(n) {
				t0 := time.Now()
				if err := b.credit.Reserve(context.Background(), n); err != nil {
					return err
				}
				s.statsLink().recordCreditStall(time.Since(t0).Nanoseconds())
			}
		}
	}
	f := wire.Frame{Flags: wire.FlagEnd, Channel: channelID, Payload: payload}.WithClass(class)
	if s.scheduler == nil {
		return s.Transport.WriteFrame(f)
	}
	return s.scheduler.Submit(context.Background(), f)
}

// tryWriteRawBulk attempts a non-blocking Bulk raw write: it reserves
// credit and enqueues to the scheduler without ever blocking. Returns
// false if the credit window is exhausted OR the scheduler queue is full
// — i.e. the FE is not keeping up. The forward path (docs/PTY_ROBUST.md,
// Fix B) treats false as "FE behind" and suppresses live output rather
// than blocking the per-app read goroutine (which would back-pressure
// into the child shell and hang the terminal). On a reserve-then-submit
// failure the credit is refunded so the ledger stays accurate.
func (s *ShellSession) tryWriteRawBulk(b *channelBinding, payload []byte) bool {
	n := uint64(len(payload))
	if b.credit != nil && !b.credit.TryReserve(n) {
		// FE behind: credit exhausted, so this live frame is suppressed
		// (docs/PTY_ROBUST.md Fix B). Count it as a Bulk drop.
		s.statsLink().recordDrop(wire.ClassBulk)
		return false
	}
	f := wire.Frame{Flags: wire.FlagEnd, Channel: b.channelID, Payload: payload}.WithClass(wire.ClassBulk)
	if s.scheduler == nil {
		if err := s.Transport.WriteFrame(f); err != nil {
			if b.credit != nil {
				b.credit.Refund(n)
			}
			return false
		}
		return true
	}
	if !s.scheduler.TrySubmit(f) {
		if b.credit != nil {
			b.credit.Refund(n)
		}
		return false
	}
	return true
}

// tryWriteCtrl enqueues a control message non-blocking. Returns false if
// the control queue is full. Used by the resync path (docs/PTY_ROBUST.md)
// so recovery never blocks a producer holding shellMu.
func (s *ShellSession) tryWriteCtrl(m any) bool {
	data, err := wire.EncodeCtrl(m)
	if err != nil {
		return false
	}
	f := wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: data}.WithClass(wire.ClassControl)
	if s.scheduler == nil {
		return s.Transport.WriteFrame(f) == nil
	}
	return s.scheduler.TrySubmit(f)
}

// tryWriteRawInteractive enqueues a raw frame at Interactive class
// non-blocking, bypassing credit (the resync snapshot is a transactional
// flow, like reattach replay). Returns false if the queue is full.
func (s *ShellSession) tryWriteRawInteractive(channelID uint32, payload []byte) bool {
	f := wire.Frame{Flags: wire.FlagEnd, Channel: channelID, Payload: payload}.WithClass(wire.ClassInteractive)
	if s.scheduler == nil {
		return s.Transport.WriteFrame(f) == nil
	}
	return s.scheduler.TrySubmit(f)
}

// drainLoop is the single FE-bound writer goroutine. Pulls frames
// from the scheduler in strict-priority order and writes them to
// Transport. Exits on scheduler.Close (graceful) or transport write
// error (FE disconnect / network failure). On exit, drainerDone is
// closed so HandleShell's teardown can wait.
func (s *ShellSession) drainLoop(ctx context.Context) {
	defer close(s.drainerDone)
	count := 0
	for {
		f, err := s.scheduler.Next(ctx)
		if err != nil {
			// router is nil in the QoS integration tests that drive
			// a ShellSession in isolation; the guard keeps both the
			// exit log and the per-frame trace below from segfaulting.
			if s.router != nil {
				s.router.log("drainLoop exit: %v (wrote %d frames)", err, count)
			}
			// ErrSchedulerClosed (teardown) or ctx.Err — exit
			// quietly. Producer-side Submit calls returning
			// errors handle the upstream visibility.
			return
		}
		count++
		if err := s.Transport.WriteFrame(f); err != nil {
			// Transport write failed — FE gone. Close the
			// scheduler so any blocked producers unblock with
			// ErrSchedulerClosed and the shell tears down.
			s.scheduler.Close()
			return
		}
		// Frame is on the wire — account it against its class for the
		// link-health stats (per-class throughput).
		s.scheduler.Stats.recordTx(f.Class(), len(f.Payload))
		if s.router != nil && f.Channel != ChannelControl {
			if b := s.router.lookupChannel(f.Channel); b != nil && isDisplayChannelKind(b.kind) {
				s.scheduler.Stats.recordDisplayTx(len(f.Payload))
			}
		}
	}
}
