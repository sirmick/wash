package router

import (
	"context"
	"encoding/json"
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
	Listen        string   // host:port
	AppsDirs      []string // ordered; first occurrence wins
	SessionAppID  string   // which manifest is the desktop surface
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

	sessionMu sync.Mutex
	session   sessionState
}

// NewRouter constructs a router; cfg.AppsDirs are expected to already
// be split. The Registry is populated by the caller via Scan before
// Run.
func NewRouter(cfg Config, reg *Registry, log Logger) *Router {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Router{
		cfg:    cfg,
		reg:    reg,
		log:    log,
		apps:   make(map[string]*AppInstance),
		byWin:  make(map[uint32]*AppInstance),
		shells: make(map[*ShellSession]struct{}),
	}
}

// Registry exposes the catalog for callers that want to surface it
// (e.g. cmd/wash-router for a /catalog endpoint).
func (r *Router) Registry() *Registry { return r.reg }

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

// declareAppToAllShells emits ShellAppDeclared + (if windowed)
// ShellWindowCreate to every attached shell. Called after a successful
// handshake.
func (r *Router) declareAppToAllShells(ctx context.Context, inst *AppInstance) error {
	manifestJSON, err := json.Marshal(inst.Manifest)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	declared := wire.NewShellAppDeclared(
		inst.InstanceID,
		inst.Manifest.Element,
		inst.Manifest.Surface,
		json.RawMessage(manifestJSON),
	)
	var createMsg any
	if inst.Manifest.Surface == SurfaceWindow {
		w, h := uint32(0), uint32(0)
		if inst.Manifest.Window != nil {
			w = inst.Manifest.Window.DefaultWidth
			h = inst.Manifest.Window.DefaultHeight
		}
		createMsg = wire.NewShellWindowCreate(inst.WindowID, inst.InstanceID, inst.Manifest.Name, w, h)
	}
	var firstErr error
	for _, s := range r.shellList() {
		if err := s.WriteCtrl(declared); err != nil && firstErr == nil {
			firstErr = err
		}
		if createMsg != nil {
			if err := s.WriteCtrl(createMsg); err != nil && firstErr == nil {
				firstErr = err
			}
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

