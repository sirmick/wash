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

	sessionMu sync.Mutex
	session   sessionState

	initialMu sync.Mutex
	initial   sessionState
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

// channelBinding is a router-side raw channel — paired writers on
// each transport plus enough state to clean up when either end goes
// away. Bytes on channel id ChannelID flow app ↔ shell verbatim.
type channelBinding struct {
	channelID uint32
	app       *AppInstance
	shell     *ShellSession
	windowID  uint32 // the shell-side window the channel is rooted at
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
	if b.shell != nil {
		_ = b.shell.WriteCtrl(wire.NewShellChannelUnbind(id, reason))
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
	defer r.mu.Unlock()
	delete(r.shells, s)
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

