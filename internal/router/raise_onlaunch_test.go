package router

import (
	"context"
	"testing"

	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// singleWinManifest builds a surface=window manifest with instancing=single
// — the de-facto one-window apps (net, washamp, music) whose re-launch
// should raise the open window rather than stack a duplicate.
func singleWinManifest() *Manifest {
	return &Manifest{
		ID:              "com.wash.net",
		Name:            "Network",
		Version:         "0.9.0",
		ProtocolVersion: ProtocolVersion,
		Element:         "wash-app-net",
		Surface:         SurfaceWindow,
		Icon:            "data:,",
		Instancing:      InstancingSingle,
		Window:          &WindowHints{DefaultWidth: 480, DefaultHeight: 320},
	}
}

// waitWindowUntil drains shell ctrl frames until the window matching winID
// satisfies pred, returning that window. Unlike waitWindowUpsert it skips
// non-matching states, so it tolerates the snapshot-vs-patch double-delivery
// race (a window can arrive in both the connect snapshot and a follow-up
// upsert) when asserting on a specific later state.
func waitWindowUntil(t *testing.T, e wire.FrameTransport, winID uint32, pred func(wire.SessionWindow) bool) wire.SessionWindow {
	t.Helper()
	for i := 0; i < 100; i++ {
		switch v := readCtrl(t, e).(type) {
		case wire.ShellSessionSnapshot:
			for _, w := range v.Windows {
				if w.WindowID == winID && pred(w) {
					return w
				}
			}
		case wire.ShellSessionPatch:
			for _, p := range v.Patches {
				if p.Op == wire.SessionPatchWindowUpsert && p.Window != nil && p.Window.WindowID == winID && pred(*p.Window) {
					return *p.Window
				}
			}
		}
	}
	t.Fatalf("window %d never satisfied predicate within 100 frames", winID)
	return wire.SessionWindow{}
}

// TestLaunchOrRaiseRaisesExisting exercises the "Open X" foreground fix:
// launching a single-window app that is already running must NOT spawn a
// second process — it raises the existing window to the foreground
// (restoring it from minimized) and returns that same instance. This is
// the raise branch shared by spawnChild / handleLaunch / controlLaunch;
// the spawn-new branch execs a binary and is covered by the apps e2e.
func TestLaunchOrRaiseRaisesExisting(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, func(format string, args ...any) { t.Logf("router: "+format, args...) })

	appPair := wiretest.NewPipePair()
	shellPair := wiretest.NewPipePair()
	app := appPair.EndB()
	shell := shellPair.EndB()

	appDone := make(chan struct{})
	go func() {
		defer close(appDone)
		_ = r.HandleApp(context.Background(), appPair.EndA(), singleWinManifest(), nil)
	}()
	shellDone := make(chan struct{})
	go func() { defer close(shellDone); _ = r.HandleShell(context.Background(), shellPair.EndA()) }()

	// Handshake: identity → ack (primary window) → EvtWindowMapped.
	writeCtrl(t, app, wire.NewIdentity("com.wash.net", ProtocolVersion, "0.9.0"))
	ack, ok := readCtrl(t, app).(wire.IdentityAck)
	if !ok || ack.WindowID == 0 {
		t.Fatalf("expected IdentityAck with a window, got %+v", ack)
	}
	win := ack.WindowID
	if m, ok := readEvt(t, app).(wire.EvtWindowMapped); !ok || m.Win != win {
		t.Fatalf("expected EvtWindowMapped(%d), got %+v", win, m)
	}
	if got := waitWindowUpsert(t, shell, win); got.Focused {
		// Fresh window is not auto-focused (the FE focuses on mount); the
		// raise below is what must flip it.
		t.Fatalf("fresh window should start unfocused, got %+v", got)
	}

	// Minimize it via the shell, so the raise also has to restore it. Poll
	// until the window reaches minimized: the window can arrive in both the
	// connect snapshot AND a follow-up create patch (the documented
	// snapshot-vs-patch race), so we can't assume the very next upsert is
	// the minimize.
	writeCtrl(t, shell, wire.NewShellWindowState(win, wire.WindowStateMinimized))
	waitWindowUntil(t, shell, win, func(w wire.SessionWindow) bool {
		return w.State == wire.WindowStateMinimized
	})
	// The shell-driven minimize also relays a state event to the app; drain
	// it so the next app event we read is the focus from the raise.
	if st, ok := readEvt(t, app).(wire.EvtWindowState); !ok || st.State != wire.WindowStateMinimized {
		t.Fatalf("expected EvtWindowState(minimized), got %+v", st)
	}

	// Re-launch the same app: instancing=single + already running → raise,
	// no second spawn. (spawnAndRun would exec singleWinManifest's empty
	// Path and fail; reaching it at all means the dedup didn't fire.)
	inst, err := r.launchOrRaise(context.Background(), &Entry{Manifest: singleWinManifest()})
	if err != nil {
		t.Fatalf("launchOrRaise: %v", err)
	}
	if inst == nil || inst.InstanceID != ack.InstanceID {
		t.Fatalf("expected the existing instance %s, got %+v", ack.InstanceID, inst)
	}

	// The app is told it gained focus.
	if f, ok := readEvt(t, app).(wire.EvtWindowFocus); !ok || f.Win != win {
		t.Fatalf("expected EvtWindowFocus(%d), got %+v", win, f)
	}

	// The shell sees the window restored to normal AND focused on top.
	// The restore emits two upserts (normal-unfocused, then focused), so
	// poll until the focused one arrives.
	if got := waitWindowUntil(t, shell, win, func(w wire.SessionWindow) bool {
		return w.Focused
	}); got.State != wire.WindowStateNormal {
		t.Fatalf("focused window should be restored to normal, got %+v", got)
	}

	appPair.Close()
	shellPair.Close()
	waitClose(t, appDone)
	waitClose(t, shellDone)
}
