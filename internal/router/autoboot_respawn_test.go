package router

import (
	"testing"
)

// TestTearDown_CrashClearsSessionLatch — (REVIEW-RECONNECT M2) a crashed
// session app must be respawnable. tearDown on an unexpected exit clears
// r.session.started, which is exactly the precondition EnsureSessionRunning
// checks before spawning — so the next shell connect brings the desktop
// chrome back. An INTENTIONAL exit (expectedExit) must NOT clear it.
func TestTearDown_CrashClearsSessionLatch(t *testing.T) {
	const sessionID = "com.wash.session"
	r := NewRouter(Config{SessionAppID: sessionID}, NewRegistry(), func(string, ...any) {})

	// Crash: expectedExit stays false.
	r.sessionMu.Lock()
	r.session.started = true
	r.sessionMu.Unlock()
	crashed := &AppInstance{AppID: sessionID, Manifest: &Manifest{ID: sessionID, Surface: SurfaceDesktop}, router: r}
	r.tearDown(crashed)
	r.sessionMu.Lock()
	started := r.session.started
	r.sessionMu.Unlock()
	if started {
		t.Error("session.started not cleared after a crash — next EnsureSessionRunning would no-op (chromeless desktop)")
	}

	// Intentional exit: expectedExit set → latch preserved for the code that
	// manages the restart.
	r.sessionMu.Lock()
	r.session.started = true
	r.sessionMu.Unlock()
	intentional := &AppInstance{AppID: sessionID, Manifest: &Manifest{ID: sessionID, Surface: SurfaceDesktop}, router: r}
	intentional.expectedExit.Store(true)
	r.tearDown(intentional)
	r.sessionMu.Lock()
	started = r.session.started
	r.sessionMu.Unlock()
	if !started {
		t.Error("session.started cleared on an intentional exit — would race a managed restart")
	}
}

// TestTearDown_CrashClearsBackgroundLatch — (REVIEW-RECONNECT M3) same for a
// crashed background service (e.g. the mDNS advertiser): clear
// backgroundStarted[appID] so EnsureBackgroundAppsRunning respawns it.
func TestTearDown_CrashClearsBackgroundLatch(t *testing.T) {
	const bgID = "com.wash.remote"
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	r.backgroundMu.Lock()
	r.backgroundStarted[bgID] = true
	r.backgroundMu.Unlock()
	crashed := &AppInstance{AppID: bgID, Manifest: &Manifest{ID: bgID, Surface: SurfaceBackground}, router: r}
	r.tearDown(crashed)
	r.backgroundMu.Lock()
	_, still := r.backgroundStarted[bgID]
	r.backgroundMu.Unlock()
	if still {
		t.Error("backgroundStarted not cleared after a crash — service silently gone until router restart")
	}

	// Intentional exit preserves the latch (restartBackgroundApp manages it).
	r.backgroundMu.Lock()
	r.backgroundStarted[bgID] = true
	r.backgroundMu.Unlock()
	intentional := &AppInstance{AppID: bgID, Manifest: &Manifest{ID: bgID, Surface: SurfaceBackground}, router: r}
	intentional.expectedExit.Store(true)
	r.tearDown(intentional)
	r.backgroundMu.Lock()
	_, still = r.backgroundStarted[bgID]
	r.backgroundMu.Unlock()
	if !still {
		t.Error("backgroundStarted cleared on an intentional exit — would race restartBackgroundApp")
	}
}
