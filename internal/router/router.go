package router

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirmick/wash/internal/wire"
)

// Channel IDs (WIRE.md §3). v0.0 fixes them; OPEN-time discipline
// negotiation is post-v0.0.
const (
	ChannelControl = 0 // JSON: handshake/asset on app socket; shell vocabulary on WS
	ChannelEvent   = 1 // CBOR: app event channel
)

// Config is the runtime configuration the caller (cmd/wash-router)
// derives from env + flags.
type Config struct {
	Listen       string   // host:port
	AppsDirs     []string // ordered; first occurrence wins
	SessionAppID string   // which manifest is the desktop surface

	// NoSession suppresses the autoboot session app. Combine with
	// InitialAppID to run a single app full-screen ("kiosk" mode).
	NoSession bool

	// InitialAppID is the app spawned on first shell connect (instead
	// of, or alongside, the session). Its FE element mounts at the
	// root surface regardless of its manifest's declared surface.
	InitialAppID string

	// ShowHidden includes apps marked manifest.Hidden in the catalog
	// the shell sees. Off by default — used by e2e tests and
	// debugging to surface the test app in the chrome launcher.
	ShowHidden bool

	// ControlSocket is the Unix-socket path the router listens on for
	// the wash-launch CLI and similar tools. Empty disables. The
	// router exports this as WASH_CONTROL_SOCKET in the env of every
	// app it spawns, so children (including shells in wash-term) can
	// invoke launches without re-discovering it.
	ControlSocket string

	// ScreenshotDir is where POST /screenshot writes PNG files. Empty
	// disables the endpoint. Files are named with the timestamp at
	// upload time.
	ScreenshotDir string
}

// Logger is a minimal sink; cmd/wash-router supplies a real one.
type Logger func(format string, args ...any)

// Router is the central coordinator. Construct with NewRouter, then
// either call Run(ctx) for the full server lifecycle or feed it
// transports directly via HandleApp/HandleShell for tests.
type Router struct {
	cfg Config
	reg *Registry
	log Logger

	mu      sync.Mutex
	apps    map[string]*AppInstance // by instance id
	byWin   map[uint32]*AppInstance // by window id (0 keys are skipped)
	shells  map[*ShellSession]struct{}

	nextWindow   atomic.Uint32
	nextInstance atomic.Uint64
	nextChannel  atomic.Uint32 // starts at 1, returns 2+ via allocChannelID

	channelsMu sync.Mutex
	channels   map[uint32]*channelBinding

	// appMsgWatchers lets a non-shell caller (e.g. the control
	// socket's `msg` op) wait for a specific outbound app_msg keyed
	// by the request-id convention in the payload. Stored as
	// instanceID → matchID → one-shot channel.
	appMsgMu       sync.Mutex
	appMsgWatchers map[string]map[string]chan map[string]any

	// singletons maps a singleton manifest's app_id to its currently-
	// running instance, or absent if none. Used to (a) refuse a
	// second spawn of a singleton, and (b) resolve sentinel addresses
	// in EvtAppMsgSendTo / ShellAppMsgSendTo. Mutations always happen
	// while r.mu is held — same lock that guards r.apps — so the
	// lookup stays consistent with the live instance map.
	singletons map[string]*AppInstance

	// pendingAttach tracks router-spawned children awaiting their
	// dial-back. Keyed by pid; the spawn caller blocks on the
	// channel, and the control-socket's attach handler resolves it
	// when the matching Identity arrives.
	pendingMu     sync.Mutex
	pendingAttach map[int]chan attachResult

	clipboard clipboardState

	sessionMu sync.Mutex
	session   sessionAppState

	initialMu sync.Mutex
	initial   sessionAppState

	// windowSession is the canonical state for windows the shell
	// renders. Sent as a snapshot on shell connect; mutated by router
	// or shell actions, with patches broadcast to every shell.
	winSession windowSession
}

// NewRouter constructs a router; cfg.AppsDirs are expected to already
// be split. The Registry is populated by the caller via Scan before
// Run.
func NewRouter(cfg Config, reg *Registry, log Logger) *Router {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Router{
		cfg:            cfg,
		reg:            reg,
		log:            log,
		apps:           make(map[string]*AppInstance),
		byWin:          make(map[uint32]*AppInstance),
		shells:         make(map[*ShellSession]struct{}),
		channels:       make(map[uint32]*channelBinding),
		appMsgWatchers: make(map[string]map[string]chan map[string]any),
		singletons:     make(map[string]*AppInstance),
		pendingAttach:  make(map[int]chan attachResult),
	}
}

// attachResult is what spawnAndRun reads from a pending-attach
// channel: either the connected AppInstance (handshake complete,
// caller takes over the registerApp / declare / loop steps), or
// an error if the attach was refused (proto mismatch, app_id
// mismatch, validation failure).
type attachResult struct {
	inst *AppInstance
	err  error
}

// registerPendingAttach reserves the pid slot before Spawn returns
// — the child could in principle dial fast enough that the
// control-socket handler runs before spawnAndRun reaches its
// select. Caller MUST unregister via takePendingAttach (or via
// the deferred cleanup) once the spawn completes one way or the
// other so stale entries don't leak.
func (r *Router) registerPendingAttach(pid int) chan attachResult {
	ch := make(chan attachResult, 1)
	r.pendingMu.Lock()
	r.pendingAttach[pid] = ch
	r.pendingMu.Unlock()
	return ch
}

// takePendingAttach claims and removes the pending entry for pid.
// Returns (ch, true) if found. The caller is responsible for
// writing the attach result before another goroutine starts a
// new spawn that could reuse the pid (unlikely but possible).
func (r *Router) takePendingAttach(pid int) (chan attachResult, bool) {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	ch, ok := r.pendingAttach[pid]
	if ok {
		delete(r.pendingAttach, pid)
	}
	return ch, ok
}

// SubscribeAppMsg registers a one-shot watcher for the next outbound
// app_msg from instanceID whose data has "id" == matchID. The
// returned channel receives the (already JSON-normalized) data map;
// the watcher is removed after delivery or when cancel() is called.
// Used by the control socket's `msg` op to await BE replies; not on
// the hot path.
func (r *Router) SubscribeAppMsg(instanceID, matchID string) (<-chan map[string]any, func()) {
	ch := make(chan map[string]any, 1)
	r.appMsgMu.Lock()
	byMatch := r.appMsgWatchers[instanceID]
	if byMatch == nil {
		byMatch = make(map[string]chan map[string]any)
		r.appMsgWatchers[instanceID] = byMatch
	}
	// One-shot per (instance, matchID). A late re-subscribe replaces
	// any earlier waiter — the previous channel is closed so its
	// caller sees a nil receive and can tell the slot was stolen.
	if prev, ok := byMatch[matchID]; ok {
		close(prev)
	}
	byMatch[matchID] = ch
	r.appMsgMu.Unlock()
	cancel := func() {
		r.appMsgMu.Lock()
		if cur, ok := r.appMsgWatchers[instanceID][matchID]; ok && cur == ch {
			delete(r.appMsgWatchers[instanceID], matchID)
			if len(r.appMsgWatchers[instanceID]) == 0 {
				delete(r.appMsgWatchers, instanceID)
			}
		}
		r.appMsgMu.Unlock()
	}
	return ch, cancel
}

// dispatchAppMsgWatchers delivers data to any waiter registered for
// (instanceID, matchID) and removes the entry. Best-effort — if the
// channel is already gone (cancelled) or the buffer is full, the
// data is dropped. Returns true if a watcher was matched.
func (r *Router) dispatchAppMsgWatchers(instanceID, matchID string, data map[string]any) bool {
	r.appMsgMu.Lock()
	byMatch := r.appMsgWatchers[instanceID]
	if byMatch == nil {
		r.appMsgMu.Unlock()
		return false
	}
	ch, ok := byMatch[matchID]
	if !ok {
		r.appMsgMu.Unlock()
		return false
	}
	delete(byMatch, matchID)
	if len(byMatch) == 0 {
		delete(r.appMsgWatchers, instanceID)
	}
	r.appMsgMu.Unlock()
	select {
	case ch <- data:
	default:
	}
	return true
}

// dropAppMsgWatchers tears down all waiters for an instance. Called
// when an app exits so blocked subscribers don't hang. A nil-data
// send tells callers the instance is gone.
func (r *Router) dropAppMsgWatchers(instanceID string) {
	r.appMsgMu.Lock()
	byMatch := r.appMsgWatchers[instanceID]
	delete(r.appMsgWatchers, instanceID)
	r.appMsgMu.Unlock()
	for _, ch := range byMatch {
		close(ch)
	}
}

// Registry exposes the catalog for callers that want to surface it
// (e.g. cmd/wash-router for a /catalog endpoint).
func (r *Router) Registry() *Registry { return r.reg }

// catalog builds the snapshot the shell sees on connect. Surface=
// desktop apps and Hidden apps are filtered out — they don't belong
// in launchers (the session app is autoboot; hidden apps are
// test/utility).
func (r *Router) catalog() []wire.ShellCatalogApp {
	entries := r.reg.Entries()
	out := make([]wire.ShellCatalogApp, 0, len(entries))
	for _, e := range entries {
		if e.Manifest == nil {
			// Listed-disabled with no parsable manifest: skip; we
			// have no name to render.
			continue
		}
		if e.Manifest.Surface == SurfaceDesktop {
			continue
		}
		if e.Manifest.Hidden && !r.cfg.ShowHidden {
			continue
		}
		out = append(out, wire.ShellCatalogApp{
			ID:         e.Manifest.ID,
			Name:       e.Manifest.Name,
			Icon:       e.Manifest.Icon,
			Surface:    e.Manifest.Surface,
			Instancing: e.Manifest.Instancing,
			Disabled:   !e.Enabled(),
			Reason:     e.Reason,
		})
	}
	return out
}

// Config returns the active configuration.
func (r *Router) Config() Config { return r.cfg }

// allocInstanceID returns a fresh per-process instance id. The format
// is intentionally opaque — apps must treat it as a string token.
func (r *Router) allocInstanceID() string {
	n := r.nextInstance.Add(1)
	return fmt.Sprintf("i-%d", n)
}

func (r *Router) allocWindowID() uint32 {
	return r.nextWindow.Add(1)
}

// allocChannelID returns a fresh raw-channel id. v0.1 uses the same
// id space on the app socket and the WS; the allocator starts at 2
// so it's safe on both (channel 0 = ctrl, channel 1 = app-side event).
func (r *Router) allocChannelID() uint32 {
	return r.nextChannel.Add(1) + 1
}

// spawnEnv builds the env additions every spawned app receives on top
// of the router's process environment.
func (r *Router) spawnEnv() []string {
	var env []string
	if r.cfg.ControlSocket != "" {
		env = append(env, "WASH_CONTROL_SOCKET="+r.cfg.ControlSocket)
	}
	return env
}

// ChannelScrollbackBytes is the per-channel ring-buffer capacity for
// bytes flowing app → shell. Sized to comfortably hold a few
// thousand lines of terminal output so a reattaching shell can replay
// the recent scrollback.
const ChannelScrollbackBytes = 256 * 1024

// channelBinding is a router-side raw channel — paired writers on
// each transport plus enough state to clean up when either end goes
// away. Bytes on channel id ChannelID flow app ↔ shell verbatim.
//
// shell may be nil after the bound shell disconnects (refresh, network
// drop). bytes from app are then captured in the ring buffer only;
// the next shell that attaches receives the buffered scrollback as
// a single replay frame, followed by live bytes.
//
// Kind=="bundle" gives the channel different semantics: bytes from
// app are accumulated into app.bundleBytes (no shell forwarding, no
// ring buffer), and on ChannelClose the bundle is marked ready.
// Per-shell bundle replay channels are spawned separately via
// reattachChannelsToShell.
type channelBinding struct {
	channelID uint32
	app       *AppInstance
	windowID  uint32 // the shell-side window the channel is rooted at
	kind      string // wire.ChannelKindGeneric or wire.ChannelKindBundle

	// shellMu guards shell + buf. Held briefly during forward and
	// rebind paths.
	shellMu sync.Mutex
	shell   *ShellSession
	buf     *ringBuffer
}

func (r *Router) registerChannel(b *channelBinding) {
	r.channelsMu.Lock()
	r.channels[b.channelID] = b
	r.channelsMu.Unlock()
}

func (r *Router) lookupChannel(id uint32) *channelBinding {
	r.channelsMu.Lock()
	defer r.channelsMu.Unlock()
	return r.channels[id]
}

// closeChannel removes the binding and (best-effort) tells both sides
// it is gone. Idempotent — repeated calls for the same id are no-ops.
func (r *Router) closeChannel(id uint32, reason string) {
	r.channelsMu.Lock()
	b := r.channels[id]
	delete(r.channels, id)
	r.channelsMu.Unlock()
	if b == nil {
		return
	}
	if b.app != nil {
		_ = b.app.writeCtrl(wire.NewChannelClosed(id, reason))
	}
	b.shellMu.Lock()
	sh := b.shell
	b.shellMu.Unlock()
	if sh != nil {
		_ = sh.WriteCtrl(wire.NewShellChannelUnbind(id, reason))
	}
}

// closeChannelsForApp tears down every channel owned by inst. Called
// when an app exits.
func (r *Router) closeChannelsForApp(inst *AppInstance, reason string) {
	r.channelsMu.Lock()
	var ids []uint32
	for id, b := range r.channels {
		if b.app == inst {
			ids = append(ids, id)
		}
	}
	r.channelsMu.Unlock()
	for _, id := range ids {
		r.closeChannel(id, reason)
	}
}

// spawnAndRun spawns the given app, runs its handshake, registers it,
// declares it to attached shells, sends the initial mapped event for
// windowed apps, and runs the app's frame loop in a goroutine. The
// goroutine handles cleanup on exit (close channels, unregister, tell
// shells the window is gone, reap the process).
//
// All three caller paths — spawn.request from an app, the control
// socket, and the initial / session bootstraps — go through here.
//
// Singleton short-circuit: if entry's manifest declares
// instancing:"singleton" and an instance is already running, return
// THAT instance instead of starting a second one. This is how
// sentinel addressing converges every dispatch on the same process.
func (r *Router) spawnAndRun(ctx context.Context, entry *Entry, kiosk bool) (*AppInstance, error) {
	if entry.Manifest.Instancing == InstancingSingleton {
		if existing := r.singletonInstance(entry.Manifest.ID); existing != nil {
			return existing, nil
		}
	}
	if r.cfg.ControlSocket == "" {
		return nil, fmt.Errorf("spawn %s: control socket disabled (apps dial WASH_DISPLAY)", entry.Manifest.ID)
	}
	cmd, err := Spawn(entry.Path, entry.Manifest.ID, "", r.cfg.ControlSocket, r.spawnEnv())
	if err != nil {
		return nil, fmt.Errorf("spawn %s: %w", entry.Manifest.ID, err)
	}
	pid := cmd.Process.Pid
	// Register the pending channel BEFORE the spawn could plausibly
	// dial back; cmd.Start has already returned, so the child may
	// already be running. takePendingAttach in the control-socket
	// handler will only match this pid if registerPendingAttach
	// has been called first. The lock keeps the operations ordered.
	ch := r.registerPendingAttach(pid)
	defer func() {
		// Best-effort: in case of timeout/cancel the channel still
		// hangs around if we leave it. takePendingAttach is safe to
		// call even if it was already taken.
		_, _ = r.takePendingAttach(pid)
	}()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	var inst *AppInstance
	select {
	case res := <-ch:
		if res.err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, fmt.Errorf("attach %s: %w", entry.Manifest.ID, res.err)
		}
		inst = res.inst
		inst.Cmd = cmd
		inst.Kiosk = kiosk
	case <-timeout.C:
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("attach timeout for %s (pid %d)", entry.Manifest.ID, pid)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, ctx.Err()
	}
	transport := inst.Transport
	r.registerApp(inst)
	if inst.WindowID != 0 {
		var defW, defH uint32
		if inst.Manifest.Window != nil {
			defW = inst.Manifest.Window.DefaultWidth
			defH = inst.Manifest.Window.DefaultHeight
		}
		patches := r.winSession.createWindow(inst.WindowID, inst.InstanceID, inst.Manifest.Element, inst.Manifest.Name, defW, defH)
		// Declare the app to shells BEFORE the patch so they have the
		// bundle in flight when window.upsert lands.
		if err := r.declareAppToAllShells(ctx, inst); err != nil {
			r.log("declare: %v", err)
		}
		r.broadcastPatches(patches)
	} else {
		if err := r.declareAppToAllShells(ctx, inst); err != nil {
			r.log("declare: %v", err)
		}
	}
	if inst.WindowID != 0 {
		_ = inst.WriteEvt(wire.NewEvtWindowMapped(inst.WindowID))
	}
	go func() {
		if err := inst.loop(context.Background()); err != nil {
			r.log("app %s loop: %v", inst.AppID, err)
		}
		r.unregisterApp(inst)
		r.closeChannelsForApp(inst, "app exited")
		r.dropAppMsgWatchers(inst.InstanceID)
		r.winSession.dropAppState(inst.InstanceID)
		if inst.WindowID != 0 {
			r.broadcastPatches(r.winSession.destroyWindow(inst.WindowID))
		}
		_ = transport.Close()
		_ = cmd.Wait()
	}()
	return inst, nil
}

// closeChannelsForWindow tears down every channel rooted at the given
// window id. Called from window.destroy paths.
func (r *Router) closeChannelsForWindow(windowID uint32, reason string) {
	r.channelsMu.Lock()
	var ids []uint32
	for id, b := range r.channels {
		if b.windowID == windowID {
			ids = append(ids, id)
		}
	}
	r.channelsMu.Unlock()
	for _, id := range ids {
		r.closeChannel(id, reason)
	}
}

// shellList returns a snapshot of attached shell sessions.
func (r *Router) shellList() []*ShellSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*ShellSession, 0, len(r.shells))
	for s := range r.shells {
		out = append(out, s)
	}
	return out
}

// registerApp inserts inst into the maps. It's the caller's job to
// have populated inst.WindowID (0 = no window).
func (r *Router) registerApp(inst *AppInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.apps[inst.InstanceID] = inst
	if inst.WindowID != 0 {
		r.byWin[inst.WindowID] = inst
	}
	if inst.Manifest != nil && inst.Manifest.Instancing == InstancingSingleton {
		r.singletons[inst.Manifest.ID] = inst
	}
}

// unregisterApp removes inst from the maps. Idempotent.
func (r *Router) unregisterApp(inst *AppInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.apps, inst.InstanceID)
	if inst.WindowID != 0 {
		delete(r.byWin, inst.WindowID)
	}
	if inst.Manifest != nil && inst.Manifest.Instancing == InstancingSingleton {
		if cur, ok := r.singletons[inst.Manifest.ID]; ok && cur == inst {
			delete(r.singletons, inst.Manifest.ID)
		}
	}
}

// singletonInstance returns the currently-running instance for a
// singleton app_id, or nil if none. Caller must NOT hold r.mu.
func (r *Router) singletonInstance(appID string) *AppInstance {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.singletons[appID]
}

// resolveRecipient turns a wire.Recipient into a concrete
// *AppInstance, spawning a singleton on demand when addressed by
// app_id. Returns a structured error code so callers can surface it
// without leaking error formatting.
//
//   recipient.instance_id set  →  direct lookup
//   recipient.app_id set       →  must be a singleton manifest;
//                                  spawned on first reference
//   both / neither set         →  bad_request
func (r *Router) resolveRecipient(ctx context.Context, rec wire.Recipient) (*AppInstance, string, error) {
	if (rec.InstanceID == "") == (rec.AppID == "") {
		return nil, wire.ErrCodeBadRequest, fmt.Errorf("recipient must set exactly one of instance_id or app_id")
	}
	if rec.InstanceID != "" {
		inst := r.appByInstance(rec.InstanceID)
		if inst == nil {
			return nil, wire.ErrCodeNotFound, fmt.Errorf("no instance %q", rec.InstanceID)
		}
		return inst, "", nil
	}
	// Sentinel: only valid for singletons.
	entry := r.reg.ByID(rec.AppID)
	if entry == nil || !entry.Enabled() {
		return nil, wire.ErrCodeNotFound, fmt.Errorf("no app %q", rec.AppID)
	}
	if entry.Manifest.Instancing != InstancingSingleton {
		return nil, wire.ErrCodeForbidden, fmt.Errorf("app %q is not singleton; address by instance_id", rec.AppID)
	}
	if inst := r.singletonInstance(rec.AppID); inst != nil {
		return inst, "", nil
	}
	// Spawn-on-demand. spawnAndRun will short-circuit if a sibling
	// spawn raced us and won, so the worst case is one extra check.
	inst, err := r.spawnAndRun(ctx, entry, false)
	if err != nil {
		return nil, wire.ErrCodeInternal, fmt.Errorf("spawn %s: %w", rec.AppID, err)
	}
	return inst, "", nil
}

// appByInstance returns the AppInstance for an instance id, or nil.
func (r *Router) appByInstance(id string) *AppInstance {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.apps[id]
}

// registerShell records a freshly-connected shell.
func (r *Router) registerShell(s *ShellSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shells[s] = struct{}{}
}

func (r *Router) unregisterShell(s *ShellSession) {
	r.mu.Lock()
	delete(r.shells, s)
	r.mu.Unlock()

	// Detach any channels owned by this shell. The channel itself
	// stays alive (the app keeps writing, bytes accumulate in the
	// ring buffer); a future shell that attaches gets a fresh bind
	// + scrollback replay.
	r.channelsMu.Lock()
	bindings := make([]*channelBinding, 0)
	for _, b := range r.channels {
		bindings = append(bindings, b)
	}
	r.channelsMu.Unlock()
	for _, b := range bindings {
		b.shellMu.Lock()
		if b.shell == s {
			b.shell = nil
		}
		b.shellMu.Unlock()
	}
}

// replayBundleToShell sends the cached JS bundle for inst over a
// freshly-allocated, per-shell raw channel. The shell uses kind=
// "bundle" on the bind to recognize the channel as a one-shot
// bundle upload, accumulates bytes until the unbind, and then
// blob-URL-imports the result.
//
// No-op if the bundle hasn't finished uploading yet. The caller is
// expected to invoke this again from the ChannelClose handler when
// the bundle becomes ready.
func (r *Router) replayBundleToShell(s *ShellSession, inst *AppInstance) {
	inst.bundleMu.Lock()
	bytes := inst.bundleBytes
	ready := inst.bundleReady
	inst.bundleMu.Unlock()
	if ready == nil {
		return
	}
	select {
	case <-ready:
	default:
		// Bundle still uploading; the ChannelClose handler will
		// re-fan-out to attached shells when it completes.
		return
	}
	if len(bytes) == 0 {
		return
	}
	id := r.allocChannelID()
	if err := s.WriteCtrl(wire.NewShellChannelBindBundle(id, inst.InstanceID)); err != nil {
		r.log("bundle bind %s: %v", inst.InstanceID, err)
		return
	}
	// Chunk the write so very large bundles don't pin a giant
	// allocation in the WS layer.
	const chunkSize = 256 * 1024
	for off := 0; off < len(bytes); off += chunkSize {
		end := off + chunkSize
		if end > len(bytes) {
			end = len(bytes)
		}
		if err := s.WriteRawFrame(id, bytes[off:end]); err != nil {
			r.log("bundle frame %s: %v", inst.InstanceID, err)
			return
		}
	}
	if err := s.WriteCtrl(wire.NewShellChannelUnbind(id, "bundle complete")); err != nil {
		r.log("bundle unbind %s: %v", inst.InstanceID, err)
	}
}

// fanOutBundleToAttachedShells sends the bundle for inst to every
// currently-connected shell. Called when the SDK finishes uploading
// the bundle (i.e., the bundle channel's ChannelClose arrives).
func (r *Router) fanOutBundleToAttachedShells(inst *AppInstance) {
	for _, s := range r.shellList() {
		r.replayBundleToShell(s, inst)
	}
}

// reattachChannelsToShell takes ownership of every currently-detached
// channel binding, sending ShellChannelBind + a scrollback replay
// frame so the new shell can rebuild any terminal-style state.
//
// Called once per shell session, immediately after the snapshot.
func (r *Router) reattachChannelsToShell(s *ShellSession) {
	r.channelsMu.Lock()
	bindings := make([]*channelBinding, 0, len(r.channels))
	for _, b := range r.channels {
		bindings = append(bindings, b)
	}
	r.channelsMu.Unlock()

	for _, b := range bindings {
		b.shellMu.Lock()
		if b.shell != nil {
			b.shellMu.Unlock()
			continue
		}
		b.shell = s
		var replay []byte
		if b.buf != nil {
			replay = b.buf.Snapshot()
		}
		id := b.channelID
		win := b.windowID
		b.shellMu.Unlock()
		if err := s.WriteCtrl(wire.NewShellChannelBind(id, win)); err != nil {
			r.log("reattach bind: %v", err)
			continue
		}
		if len(replay) > 0 {
			if err := s.WriteRawFrame(id, replay); err != nil {
				r.log("reattach replay channel %d: %v", id, err)
			}
		}
	}
}

// declareAppToAllShells announces inst to every attached shell via
// ShellSession.declareInstance, which dedupes against a parallel
// declareExistingAppsTo run on the same shell. Also replays the
// instance's bundle if it's already cached — for new apps spawned
// after the shell connected (e.g. via the launcher).
func (r *Router) declareAppToAllShells(ctx context.Context, inst *AppInstance) error {
	var firstErr error
	for _, s := range r.shellList() {
		if err := s.declareInstance(inst); err != nil && firstErr == nil {
			firstErr = err
		}
		r.replayBundleToShell(s, inst)
	}
	if firstErr != nil {
		return firstErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// broadcastPatches sends a single session.patch frame to every
// connected shell. No-op if patches is empty.
func (r *Router) broadcastPatches(patches []wire.SessionPatch) {
	if len(patches) == 0 {
		return
	}
	msg := wire.NewShellSessionPatch(patches...)
	for _, s := range r.shellList() {
		if err := s.WriteCtrl(msg); err != nil {
			r.log("broadcast patch: %v", err)
		}
	}
}

