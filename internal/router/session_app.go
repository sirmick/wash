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

// sessionState tracks the (single) session app's lifecycle.
type sessionState struct {
	mu      sync.Mutex
	started bool
}

// EnsureSessionRunning spawns the configured session app if it has
// not yet been spawned. Idempotent — safe to call on every shell
// connect. Returns ErrNoSessionApp if the configured id is missing
// from the registry.
func (r *Router) EnsureSessionRunning(ctx context.Context) error {
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
	// Drive HandleApp in a goroutine; it owns t for its lifetime.
	go func() {
		if err := r.HandleApp(ctx, t, entry.Manifest, cmd); err != nil {
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
