package router

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrNoSessionApp is returned when the configured session app is
// missing or disabled.
var ErrNoSessionApp = errors.New("session app not available")

// sessionAppState tracks the (single) session app's lifecycle.
type sessionAppState struct {
	mu      sync.Mutex
	started bool
}

// EnsureSessionRunning spawns the configured session app if it has
// not yet been spawned. Idempotent — safe to call on every shell
// connect. Returns ErrNoSessionApp if the configured id is missing
// from the registry. A no-op when Config.NoSession is set.
func (r *Router) EnsureSessionRunning(ctx context.Context) error {
	if r.cfg.NoSession {
		return nil
	}
	r.sessionMu.Lock()
	if r.session.started {
		r.sessionMu.Unlock()
		return nil
	}
	r.session.started = true
	r.sessionMu.Unlock()

	entry := r.reg.ByID(r.cfg.SessionAppID)
	if entry == nil || !entry.Enabled() {
		// reset so a future config change could retry
		r.sessionMu.Lock()
		r.session.started = false
		r.sessionMu.Unlock()
		return fmt.Errorf("%w: %s", ErrNoSessionApp, r.cfg.SessionAppID)
	}
	if entry.Manifest.Surface != SurfaceDesktop {
		r.sessionMu.Lock()
		r.session.started = false
		r.sessionMu.Unlock()
		return fmt.Errorf("session app %q has surface=%q (must be desktop)", r.cfg.SessionAppID, entry.Manifest.Surface)
	}

	cmd, parent, err := Spawn(entry.Path, entry.Manifest.ID, "", nil)
	if err != nil {
		r.sessionMu.Lock()
		r.session.started = false
		r.sessionMu.Unlock()
		return fmt.Errorf("spawn session: %w", err)
	}
	t := NewStreamTransport(parent)
	// Drive HandleApp on a router-lifetime context — the shell's ctx
	// would cancel on browser refresh and kill the session app,
	// leaving the next shell without a chrome to declare.
	go func() {
		if err := r.HandleApp(context.Background(), t, entry.Manifest, cmd); err != nil {
			r.log("session app: %v", err)
		}
		_ = cmd.Wait()
		// If the session exits we mark it down; a future shell
		// connect could (currently does not) restart it.
		r.sessionMu.Lock()
		r.session.started = false
		r.sessionMu.Unlock()
	}()
	return nil
}

// EnsureInitialAppRunning spawns Config.InitialAppID, if set, in kiosk
// mode. Idempotent. Called from HandleShell.
func (r *Router) EnsureInitialAppRunning(ctx context.Context) error {
	if r.cfg.InitialAppID == "" {
		return nil
	}
	r.initialMu.Lock()
	if r.initial.started {
		r.initialMu.Unlock()
		return nil
	}
	r.initial.started = true
	r.initialMu.Unlock()

	entry := r.reg.ByID(r.cfg.InitialAppID)
	if entry == nil || !entry.Enabled() {
		r.initialMu.Lock()
		r.initial.started = false
		r.initialMu.Unlock()
		return fmt.Errorf("initial app %q not registered or disabled", r.cfg.InitialAppID)
	}
	cmd, parent, err := Spawn(entry.Path, entry.Manifest.ID, "", nil)
	if err != nil {
		r.initialMu.Lock()
		r.initial.started = false
		r.initialMu.Unlock()
		return fmt.Errorf("spawn initial: %w", err)
	}
	t := NewStreamTransport(parent)
	go func() {
		// Router-lifetime context — kiosk apps must survive shell
		// reconnects, same reasoning as the session app above.
		if err := r.HandleAppKiosk(context.Background(), t, entry.Manifest, cmd); err != nil {
			r.log("initial app: %v", err)
		}
		_ = cmd.Wait()
		r.initialMu.Lock()
		r.initial.started = false
		r.initialMu.Unlock()
	}()
	return nil
}
