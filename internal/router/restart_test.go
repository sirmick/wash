package router

import (
	"context"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// bgManifest builds a surface=background singleton manifest — the shape
// a restartable service (wash-display) declares.
func bgManifest(id string) *Manifest {
	return &Manifest{
		ID:              id,
		Name:            "BG " + id,
		Version:         "0.8.0",
		ProtocolVersion: ProtocolVersion,
		Surface:         SurfaceBackground,
		Instancing:      InstancingSingleton,
	}
}

// handshakeApp spawns HandleApp for manifest m over a pipe, runs the
// identity→ack handshake, and drains the primary EvtWindowMapped (for
// window-surface apps). Returns the app-side transport and a done chan.
func handshakeApp(t *testing.T, r *Router, m *Manifest) (wire.FrameTransport, chan struct{}) {
	t.Helper()
	pair := wiretest.NewPipePair()
	app := pair.EndB()
	done := make(chan struct{})
	go func() { defer close(done); _ = r.HandleApp(context.Background(), pair.EndA(), m, nil) }()
	writeCtrl(t, app, wire.NewIdentity(m.ID, ProtocolVersion, m.Version))
	ack, ok := readCtrl(t, app).(wire.IdentityAck)
	if !ok {
		t.Fatalf("expected IdentityAck, got %+v", ack)
	}
	if ack.WindowID != 0 {
		if mp, ok := readEvt(t, app).(wire.EvtWindowMapped); !ok || mp.Win != ack.WindowID {
			t.Fatalf("expected EvtWindowMapped(%d), got %+v", ack.WindowID, mp)
		}
	}
	return app, done
}

// An app without the restart capability gets app.restart.err forbidden,
// and the connection stays up. Mirrors TestWindowCreateRequiresCapability.
func TestAppRestartRequiresCapability(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(f string, a ...any) { t.Logf("router: "+f, a...) })
	app, _ := handshakeApp(t, r, multiWinManifest()) // no caps

	writeEvt(t, app, wire.NewEvtAppRestart(9, "com.wash.display"))
	got := readEvt(t, app)
	errEvt, ok := got.(wire.EvtAppRestartErr)
	if !ok {
		t.Fatalf("want EvtAppRestartErr, got %T (%+v)", got, got)
	}
	if errEvt.Code != wire.ErrCodeForbidden {
		t.Fatalf("want forbidden, got %q", errEvt.Code)
	}
	if errEvt.ReqID != 9 {
		t.Fatalf("want req_id 9, got %d", errEvt.ReqID)
	}
}

// With the capability, restarting an unknown app id replies not_found:
// the cap check passes, restartBackgroundApp's registry lookup fails.
func TestAppRestartUnknownApp(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(f string, a ...any) { t.Logf("router: "+f, a...) })
	app, _ := handshakeApp(t, r, multiWinManifest(CapRestart))

	writeEvt(t, app, wire.NewEvtAppRestart(3, "com.wash.nope"))
	got := readEvt(t, app)
	errEvt, ok := got.(wire.EvtAppRestartErr)
	if !ok {
		t.Fatalf("want EvtAppRestartErr, got %T (%+v)", got, got)
	}
	if errEvt.Code != wire.ErrCodeNotFound {
		t.Fatalf("want not_found, got %q", errEvt.Code)
	}
}

// restartBackgroundApp reports not_found for an unregistered id.
func TestRestartBackgroundAppNotFound(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(f string, a ...any) { t.Logf("router: "+f, a...) })
	_, code, err := r.restartBackgroundApp("com.wash.nope")
	if err == nil {
		t.Fatal("want error for unknown id")
	}
	if code != wire.ErrCodeNotFound {
		t.Fatalf("want not_found, got %q", code)
	}
}

// restartBackgroundApp refuses a non-background target with forbidden,
// before any terminate/respawn — a windowed app is the wrong surface.
func TestRestartBackgroundAppRejectsNonBackground(t *testing.T) {
	reg := NewRegistry()
	reg.appendEntry(&Entry{Path: "/fake/disptest", Manifest: multiWinManifest()})
	r := NewRouter(Config{}, reg, func(f string, a ...any) { t.Logf("router: "+f, a...) })
	_, code, err := r.restartBackgroundApp("com.wash.disptest")
	if err == nil {
		t.Fatal("want error for non-background target")
	}
	if code != wire.ErrCodeForbidden {
		t.Fatalf("want forbidden, got %q", code)
	}
}

// waitSingletonGone returns true immediately when no instance for the
// id is registered, false (after the timeout) while one remains, and
// true again once it's unregistered — the gate the respawn waits on.
func TestWaitSingletonGone(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(f string, a ...any) { t.Logf("router: "+f, a...) })
	if !r.waitSingletonGone("com.wash.absent", 50*time.Millisecond) {
		t.Fatal("want true for absent singleton")
	}

	mani := bgManifest("com.wash.svc")
	inst := &AppInstance{AppID: mani.ID, InstanceID: "i-svc", Manifest: mani, router: r}
	r.registerApp(inst)
	if r.waitSingletonGone("com.wash.svc", 50*time.Millisecond) {
		t.Fatal("want false while singleton registered")
	}
	r.unregisterApp(inst)
	if !r.waitSingletonGone("com.wash.svc", 50*time.Millisecond) {
		t.Fatal("want true after singleton unregistered")
	}
}
