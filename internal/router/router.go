package router

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

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
	nextAsset    atomic.Uint64
	nextChannel  atomic.Uint32 // starts at 1, returns 2+ via allocChannelID

	channelsMu sync.Mutex
	channels   map[uint32]*channelBinding

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
		cfg:      cfg,
		reg:      reg,
		log:      log,
		apps:     make(map[string]*AppInstance),
		byWin:    make(map[uint32]*AppInstance),
		shells:   make(map[*ShellSession]struct{}),
		channels: make(map[uint32]*channelBinding),
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

func (r *Router) allocAssetID() uint64 {
	return r.nextAsset.Add(1)
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
type channelBinding struct {
	channelID uint32
	app       *AppInstance
	windowID  uint32 // the shell-side window the channel is rooted at

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
func (r *Router) spawnAndRun(ctx context.Context, entry *Entry, kiosk bool) (*AppInstance, error) {
	cmd, parent, err := Spawn(entry.Path, entry.Manifest.ID, "", r.spawnEnv())
	if err != nil {
		return nil, fmt.Errorf("spawn %s: %w", entry.Manifest.ID, err)
	}
	transport := NewStreamTransport(parent)
	inst := &AppInstance{
		Transport: transport,
		AppID:     entry.Manifest.ID,
		Manifest:  entry.Manifest,
		Cmd:       cmd,
		Kiosk:     kiosk,
		router:    r,
		pending:   make(map[uint64]*pendingAsset),
	}
	if err := inst.handshake(ctx); err != nil {
		_ = transport.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("handshake %s: %w", entry.Manifest.ID, err)
	}
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
}

// unregisterApp removes inst from the maps. Idempotent.
func (r *Router) unregisterApp(inst *AppInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.apps, inst.InstanceID)
	if inst.WindowID != 0 {
		delete(r.byWin, inst.WindowID)
	}
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
// declareExistingAppsTo run on the same shell.
func (r *Router) declareAppToAllShells(ctx context.Context, inst *AppInstance) error {
	var firstErr error
	for _, s := range r.shellList() {
		if err := s.declareInstance(inst); err != nil && firstErr == nil {
			firstErr = err
		}
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

