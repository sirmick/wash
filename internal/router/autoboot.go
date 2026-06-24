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

	// Spawn-and-run on a router-lifetime context — the shell's ctx
	// would cancel on browser refresh and kill the session app,
	// leaving the next shell without a chrome to declare.
	go func() {
		if _, err := r.spawnAndRun(context.Background(), entry, false); err != nil {
			r.log("session app: %v", err)
			r.sessionMu.Lock()
			r.session.started = false
			r.sessionMu.Unlock()
		}
	}()
	return nil
}

// EnsureBackgroundAppsRunning spawns every registered surface=background
// app that hasn't already been started. Idempotent — safe to call on
// every shell connect; only the first call per app id actually spawns.
// Failures don't abort the loop (one broken service shouldn't block
// the others), and they clear the per-app started flag so a retry on
// the next shell connect can succeed.
func (r *Router) EnsureBackgroundAppsRunning(ctx context.Context) {
	for _, entry := range r.reg.Entries() {
		if !entry.Enabled() || entry.Manifest.Surface != SurfaceBackground {
			continue
		}
		r.startBackgroundApp(entry)
	}
}

// EnsureBootAutostartApps spawns the surface=background apps that declare
// AutostartAtBoot — at router startup, before any shell connects. Today
// that's the mDNS advertiser (com.wash.remote), so an idle (unbrowsed) host
// stays discoverable on the LAN. Shares backgroundStarted with
// EnsureBackgroundAppsRunning, so the first shell connect never double-spawns
// what booted here.
func (r *Router) EnsureBootAutostartApps() {
	for _, entry := range r.reg.Entries() {
		if !entry.Enabled() || entry.Manifest.Surface != SurfaceBackground {
			continue
		}
		if !entry.Manifest.AutostartAtBoot {
			continue
		}
		r.startBackgroundApp(entry)
	}
}

// startBackgroundApp spawns one background entry on a router-lifetime context
// (background services must outlive any one shell), recording it in
// backgroundStarted so it spawns at most once; on failure it clears the flag
// so a later Ensure call can retry. A no-op if the app is already started.
func (r *Router) startBackgroundApp(entry *Entry) {
	appID := entry.Manifest.ID
	r.backgroundMu.Lock()
	if r.backgroundStarted[appID] {
		r.backgroundMu.Unlock()
		return
	}
	r.backgroundStarted[appID] = true
	r.backgroundMu.Unlock()

	e := entry
	go func() {
		if _, err := r.spawnAndRun(context.Background(), e, false); err != nil {
			r.log("background app %s: %v", e.Manifest.ID, err)
			r.backgroundMu.Lock()
			delete(r.backgroundStarted, e.Manifest.ID)
			r.backgroundMu.Unlock()
		}
	}()
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
	go func() {
		// Router-lifetime context — kiosk apps must survive shell
		// reconnects, same reasoning as the session app above.
		if _, err := r.spawnAndRun(context.Background(), entry, true); err != nil {
			r.log("initial app: %v", err)
			r.initialMu.Lock()
			r.initial.started = false
			r.initialMu.Unlock()
		}
	}()
	return nil
}
