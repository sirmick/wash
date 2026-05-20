package router

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

func readCtrl(t *testing.T, e wire.FrameTransport) any {
	t.Helper()
	f, err := e.ReadFrame()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Channel != ChannelControl {
		t.Fatalf("expected channel %d, got %d", ChannelControl, f.Channel)
	}
	m, err := wire.DecodeCtrl(f.Payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func writeCtrl(t *testing.T, e wire.FrameTransport, m any) {
	t.Helper()
	b, err := wire.EncodeCtrl(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := e.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: b}); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func aboutManifest() *Manifest {
	return &Manifest{
		ID:              "com.wash.about",
		Name:            "About wash",
		Version:         "0.0.0",
		ProtocolVersion: ProtocolVersion,
		Element:         "wash-app-about",
		Surface:         SurfaceWindow,
		Icon:            "data:image/svg+xml,W",
		Instancing:      InstancingMulti,
		Window:          &WindowHints{DefaultWidth: 480, DefaultHeight: 320},
	}
}

// TestHandshakeAndAssetPull drives the full v0.0 spine in-memory:
//
//   - App side sends Identity → router replies IdentityAck and tells
//     the shell ShellAppDeclared + ShellWindowCreate.
//   - Shell sends ShellAssetFetch → router relays as AssetRead on the
//     app's channel 0.
//   - App responds with AssetReadOK then AssetData(end=true) → router
//     relays both as ShellAssetDeliver back to the shell.
func TestHandshakeAndAssetPull(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, func(format string, args ...any) {
		t.Logf("router: "+format, args...)
	})

	appPair := wiretest.NewPipePair()
	shellPair := wiretest.NewPipePair()

	manifest := aboutManifest()
	appDone := make(chan struct{})
	go func() {
		defer close(appDone)
		_ = r.HandleApp(context.Background(), appPair.EndA(), manifest, nil)
	}()

	shellDone := make(chan struct{})
	go func() {
		defer close(shellDone)
		_ = r.HandleShell(context.Background(), shellPair.EndA())
	}()

	// App side: send Identity, expect IdentityAck.
	writeCtrl(t, appPair.EndB(), wire.NewIdentity("com.wash.about", 1, "0.0.0"))
	ack, ok := readCtrl(t, appPair.EndB()).(wire.IdentityAck)
	if !ok {
		t.Fatalf("expected IdentityAck")
	}
	if ack.InstanceID == "" {
		t.Fatal("instance_id missing")
	}
	if ack.WindowID == 0 {
		t.Fatal("window_id should be set for surface=window")
	}

	// Shell side: catalog goes out first on connect (currently empty
	// in this test — no apps in the registry).
	if cat, ok := readCtrl(t, shellPair.EndB()).(wire.ShellCatalog); !ok {
		t.Fatalf("expected ShellCatalog first")
	} else if len(cat.Apps) != 0 {
		t.Fatalf("expected empty catalog, got %+v", cat.Apps)
	}

	// Drain frames until we have both app.declared and a window
	// upsert for our app. They can arrive in either order because the
	// shell-setup loop and the HandleApp broadcast race.
	var declared *wire.ShellAppDeclared
	var window *wire.SessionWindow
	for declared == nil || window == nil {
		switch m := readCtrl(t, shellPair.EndB()).(type) {
		case wire.ShellAppDeclared:
			d := m
			declared = &d
		case wire.ShellSessionSnapshot:
			for i := range m.Windows {
				if m.Windows[i].WindowID == ack.WindowID {
					w := m.Windows[i]
					window = &w
				}
			}
		case wire.ShellSessionPatch:
			for _, p := range m.Patches {
				if p.Op == wire.SessionPatchWindowUpsert && p.Window != nil && p.Window.WindowID == ack.WindowID {
					w := *p.Window
					window = &w
				}
			}
		}
	}
	if declared.InstanceID != ack.InstanceID || declared.Surface != SurfaceWindow {
		t.Fatalf("declared mismatch: %+v", declared)
	}
	if window.W != 480 || window.H != 320 {
		t.Fatalf("window geometry: %+v", window)
	}

	// App side: expect EvtWindowMapped on channel 1.
	if f, err := appPair.EndB().ReadFrame(); err != nil {
		t.Fatalf("read mapped: %v", err)
	} else if f.Channel != ChannelEvent {
		t.Fatalf("mapped on channel %d", f.Channel)
	} else {
		got, err := wire.DecodeEvt(f.Payload)
		if err != nil {
			t.Fatalf("decode evt: %v", err)
		}
		mapped, ok := got.(wire.EvtWindowMapped)
		if !ok || mapped.Win != ack.WindowID {
			t.Fatalf("mapped mismatch: %+v", got)
		}
	}

	// Shell side: send ShellAssetFetch.
	writeCtrl(t, shellPair.EndB(), wire.NewShellAssetFetch(ack.InstanceID, "index.js"))

	// App side: should receive AssetRead.
	readMsg := readCtrl(t, appPair.EndB())
	read, ok := readMsg.(wire.AssetRead)
	if !ok {
		t.Fatalf("expected AssetRead, got %T (%v)", readMsg, readMsg)
	}
	if read.Name != "index.js" {
		t.Fatalf("name mismatch: %s", read.Name)
	}

	// App side: reply with AssetReadOK + AssetData(end=true).
	writeCtrl(t, appPair.EndB(), wire.NewAssetReadOK(read.ID, 5, "application/javascript"))
	body := base64.StdEncoding.EncodeToString([]byte("hello"))
	writeCtrl(t, appPair.EndB(), wire.NewAssetData(read.ID, body, true))

	// Shell side: expect ShellAssetDeliver.
	delivered, ok := readCtrl(t, shellPair.EndB()).(wire.ShellAssetDeliver)
	if !ok {
		t.Fatalf("expected ShellAssetDeliver")
	}
	if delivered.InstanceID != ack.InstanceID || delivered.Name != "index.js" || delivered.Bytes != body || !delivered.End {
		t.Fatalf("deliver mismatch: %+v", delivered)
	}
	if delivered.MIME != "application/javascript" {
		t.Fatalf("mime not propagated: %q", delivered.MIME)
	}

	// Tear down by closing the app side; router will exit HandleApp.
	appPair.Close()
	shellPair.Close()
	waitClose(t, appDone)
	waitClose(t, shellDone)
}

// TestHandshakeRejectsBadAppID confirms the router refuses an identity
// frame whose app_id doesn't match what was spawned.
func TestHandshakeRejectsBadAppID(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, nil)
	pp := wiretest.NewPipePair()
	manifest := aboutManifest()

	errCh := make(chan error, 1)
	go func() { errCh <- r.HandleApp(context.Background(), pp.EndA(), manifest, nil) }()

	writeCtrl(t, pp.EndB(), wire.NewIdentity("com.evil.imposter", 1, "0.0.0"))

	// Router will close after sending Error. Drain.
	msg := readCtrl(t, pp.EndB())
	if e, ok := msg.(wire.Error); !ok || e.Code != wire.ErrCodeBadIdentity {
		t.Fatalf("expected bad_identity error, got %+v", msg)
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected non-nil error from HandleApp")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HandleApp didn't return")
	}
}

// TestHandshakeRejectsBadProto checks ProtocolVersion mismatch is
// surfaced and the connection closes.
func TestHandshakeRejectsBadProto(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, nil)
	pp := wiretest.NewPipePair()
	manifest := aboutManifest()

	errCh := make(chan error, 1)
	go func() { errCh <- r.HandleApp(context.Background(), pp.EndA(), manifest, nil) }()

	writeCtrl(t, pp.EndB(), wire.NewIdentity("com.wash.about", 99, "0.0.0"))

	msg := readCtrl(t, pp.EndB())
	if e, ok := msg.(wire.Error); !ok || e.Code != wire.ErrCodeProtoMismatch {
		t.Fatalf("expected proto_mismatch error, got %+v", msg)
	}

	select {
	case err := <-errCh:
		if !errIsFromHandshake(err) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HandleApp didn't return")
	}
}

func errIsFromHandshake(err error) bool {
	return err != nil && !errors.Is(err, io.EOF)
}

func waitClose(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine didn't finish")
	}
}
