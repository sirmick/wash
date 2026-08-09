package router

import (
	"context"
	"testing"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

// multiWinManifest builds a surface=window manifest with the given
// capabilities. CapWindows opts the app into window.create.
func multiWinManifest(caps ...string) *Manifest {
	return &Manifest{
		ID:              "com.wash.disptest",
		Name:            "Disp Test",
		Version:         "0.9.0",
		ProtocolVersion: ProtocolVersion,
		Element:         "wash-app-disptest",
		Surface:         SurfaceWindow,
		Icon:            "data:,",
		Instancing:      InstancingMulti,
		Capabilities:    caps,
		Window:          &WindowHints{DefaultWidth: 480, DefaultHeight: 320},
	}
}

// readEvt reads one frame, asserts it's on the event channel, and
// decodes it. Mirror of readCtrl (spine_test.go) for channel 1.
func readEvt(t *testing.T, e wire.FrameTransport) any {
	t.Helper()
	f, err := e.ReadFrame()
	if err != nil {
		t.Fatalf("read evt: %v", err)
	}
	if f.Channel != ChannelEvent {
		t.Fatalf("expected event channel %d, got %d", ChannelEvent, f.Channel)
	}
	m, err := wire.DecodeEvt(f.Payload)
	if err != nil {
		t.Fatalf("decode evt: %v", err)
	}
	return m
}

func writeEvt(t *testing.T, e wire.FrameTransport, m any) {
	t.Helper()
	b, err := wire.EncodeEvt(m)
	if err != nil {
		t.Fatalf("encode evt: %v", err)
	}
	if err := e.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelEvent, Payload: b}); err != nil {
		t.Fatalf("write evt: %v", err)
	}
}

// waitWindowUpsert drains shell ctrl frames until it sees winID in a
// session snapshot or an upsert patch, returning that window. The
// snapshot-vs-patch race (shell connect vs app handshake) is handled
// like TestHandshakeAndAssetPull.
func waitWindowUpsert(t *testing.T, e wire.FrameTransport, winID uint32) wire.SessionWindow {
	t.Helper()
	for i := 0; i < 50; i++ {
		switch v := readCtrl(t, e).(type) {
		case wire.ShellSessionSnapshot:
			for _, w := range v.Windows {
				if w.WindowID == winID {
					return w
				}
			}
		case wire.ShellSessionPatch:
			for _, p := range v.Patches {
				if p.Op == wire.SessionPatchWindowUpsert && p.Window != nil && p.Window.WindowID == winID {
					return *p.Window
				}
			}
		}
	}
	t.Fatalf("no upsert for window %d within 50 frames", winID)
	return wire.SessionWindow{}
}

func waitWindowDelete(t *testing.T, e wire.FrameTransport, winID uint32) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if v, ok := readCtrl(t, e).(wire.ShellSessionPatch); ok {
			for _, p := range v.Patches {
				if p.Op == wire.SessionPatchWindowDelete && p.WindowID == winID {
					return
				}
			}
		}
	}
	t.Fatalf("no delete for window %d within 50 frames", winID)
}

// TestMultiWindowCreateChannelDestroy exercises the docs/DISPLAY.md §4
// contract: a CapWindows instance creates a second window beyond its
// primary, opens a per-window video channel on it (proving channel
// ownership accepts created windows), then destroys it.
func TestMultiWindowCreateChannelDestroy(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, func(format string, args ...any) { t.Logf("router: "+format, args...) })

	appPair := wiretest.NewPipePair()
	shellPair := wiretest.NewPipePair()
	app := appPair.EndB()
	shell := shellPair.EndB()

	appDone := make(chan struct{})
	go func() {
		defer close(appDone)
		_ = r.HandleApp(context.Background(), appPair.EndA(), multiWinManifest(CapWindows), nil)
	}()
	shellDone := make(chan struct{})
	go func() { defer close(shellDone); _ = r.HandleShell(context.Background(), shellPair.EndA()) }()

	// Handshake: identity → ack (primary window), then EvtWindowMapped.
	writeCtrl(t, app, wire.NewIdentity("com.wash.disptest", ProtocolVersion, "0.9.0"))
	ack, ok := readCtrl(t, app).(wire.IdentityAck)
	if !ok || ack.WindowID == 0 {
		t.Fatalf("expected IdentityAck with a window, got %+v", ack)
	}
	primary := ack.WindowID
	if m, ok := readEvt(t, app).(wire.EvtWindowMapped); !ok || m.Win != primary {
		t.Fatalf("expected EvtWindowMapped(%d), got %+v", primary, m)
	}
	waitWindowUpsert(t, shell, primary) // shell sees the primary window

	// Create a second window.
	writeEvt(t, app, wire.NewEvtWindowCreate(1, wire.WindowRoleToplevel, 0, "Second", 300, 200, ""))
	created, ok := readEvt(t, app).(wire.EvtWindowCreated)
	if !ok || created.ReqID != 1 {
		t.Fatalf("expected EvtWindowCreated req=1, got %+v", created)
	}
	w2 := created.Win
	if w2 == 0 || w2 == primary {
		t.Fatalf("second window id %d should be nonzero and distinct from primary %d", w2, primary)
	}
	if got := waitWindowUpsert(t, shell, w2); got.Title != "Second" || got.InstanceID != ack.InstanceID {
		t.Fatalf("second window upsert mismatch: %+v", got)
	} else if got.Element != "wash-app-disptest" {
		// Empty element override inherits the instance manifest element.
		t.Fatalf("inherited element want wash-app-disptest, got %q", got.Element)
	}

	// A per-window element override names a different decoder/view tag
	// (e.g. the built-in wash-app-display). The shell mounts that tag
	// instead of the instance's manifest element.
	writeEvt(t, app, wire.NewEvtWindowCreate(3, wire.WindowRoleToplevel, 0, "Video", 300, 200, "wash-app-display"))
	created3, ok := readEvt(t, app).(wire.EvtWindowCreated)
	if !ok || created3.ReqID != 3 {
		t.Fatalf("expected EvtWindowCreated req=3, got %+v", created3)
	}
	if got := waitWindowUpsert(t, shell, created3.Win); got.Element != "wash-app-display" {
		t.Fatalf("override element want wash-app-display, got %q", got.Element)
	}

	// Open a video channel on the created window — proves ownsWindow
	// accepts window.create'd ids, not just the primary.
	writeCtrl(t, app, wire.NewChannelOpenKind(2, w2, wire.ChannelKindVideo))
	if opened, ok := readCtrl(t, app).(wire.ChannelOpened); !ok || opened.ReqID != 2 {
		t.Fatalf("expected ChannelOpened req=2 for video channel, got %+v", opened)
	}

	// Destroy the created window; shell sees the delete.
	writeEvt(t, app, wire.NewEvtWindowDestroy(w2))
	waitWindowDelete(t, shell, w2)

	appPair.Close()
	shellPair.Close()
	waitClose(t, appDone)
	waitClose(t, shellDone)
}

// TestWindowCreateRequiresCapability: an app without CapWindows that
// asks for a window is refused with a forbidden error, not given one.
func TestWindowCreateRequiresCapability(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, func(format string, args ...any) { t.Logf("router: "+format, args...) })

	appPair := wiretest.NewPipePair()
	app := appPair.EndB()
	appDone := make(chan struct{})
	go func() {
		defer close(appDone)
		_ = r.HandleApp(context.Background(), appPair.EndA(), multiWinManifest(), nil)
	}()

	writeCtrl(t, app, wire.NewIdentity("com.wash.disptest", ProtocolVersion, "0.9.0"))
	ack, ok := readCtrl(t, app).(wire.IdentityAck)
	if !ok || ack.WindowID == 0 {
		t.Fatalf("expected IdentityAck with a window, got %+v", ack)
	}
	if m, ok := readEvt(t, app).(wire.EvtWindowMapped); !ok || m.Win != ack.WindowID {
		t.Fatalf("expected EvtWindowMapped, got %+v", m)
	}

	writeEvt(t, app, wire.NewEvtWindowCreate(1, wire.WindowRoleToplevel, 0, "Nope", 300, 200, ""))
	errEvt, ok := readEvt(t, app).(wire.EvtWindowCreateErr)
	if !ok || errEvt.ReqID != 1 || errEvt.Code != wire.ErrCodeForbidden {
		t.Fatalf("expected EvtWindowCreateErr req=1 forbidden, got %+v", errEvt)
	}

	appPair.Close()
	waitClose(t, appDone)
}
