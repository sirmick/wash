package router

import (
	"testing"

	"github.com/sirmick/wash/pkg/wire"
)

// stateOf reads a window's current state from the session under its lock.
func stateOf(r *Router, win uint32) string {
	r.winSession.mu.Lock()
	defer r.winSession.mu.Unlock()
	if w := r.winSession.windows[win]; w != nil {
		return w.State
	}
	return ""
}

// TestRelayWindowStateMinimize is the regression for REVIEW-X11-WAYLAND H7: an
// app (wash-display, on a guest CSD minimize button) can request a window
// state change via window.state, gated to windows it owns.
func TestRelayWindowStateMinimize(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	r.winSession.createWindow(5, "i-1", "el", "", "", "T",
		400, 300, 0, 0, 0, 0, false, false)

	owner := &AppInstance{InstanceID: "i-1", WindowID: 5, router: r}
	if err := owner.relayWindowState(wire.EvtWindowState{Win: 5, State: wire.WindowStateMinimized}); err != nil {
		t.Fatalf("relayWindowState: %v", err)
	}
	if got := stateOf(r, 5); got != wire.WindowStateMinimized {
		t.Fatalf("window state = %q, want minimized", got)
	}

	// An app that does NOT own the window can't change its state.
	other := &AppInstance{InstanceID: "i-2", WindowID: 9, router: r}
	if err := other.relayWindowState(wire.EvtWindowState{Win: 5, State: wire.WindowStateNormal}); err != nil {
		t.Fatalf("relayWindowState (non-owner): %v", err)
	}
	if got := stateOf(r, 5); got != wire.WindowStateMinimized {
		t.Fatalf("non-owner changed window state to %q — ownership gate failed", got)
	}
}
