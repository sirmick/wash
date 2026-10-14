package router

import (
	"context"
	"testing"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

// TestReconnectSnapshotKeepsIcons pins GH #22: a browser that drops and
// reconnects must get back windows carrying the manifest's icon and
// accent. The titlebar renders from SessionWindow.Icon alone, so a
// snapshot that rebuilt records from anything thinner than the manifest
// would show icon-less windows. Covers both the handshake window and one
// made later by window.create, and a retitle in between (an upsert that
// rewrites the record).
func TestReconnectSnapshotKeepsIcons(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, func(format string, args ...any) { t.Logf("router: "+format, args...) })

	m := multiWinManifest(CapWindows)
	m.Icon = "terminal"
	m.Accent = "#33aaff"

	appPair := wiretest.NewPipePair()
	app := appPair.EndB()
	appDone := make(chan struct{})
	go func() {
		defer close(appDone)
		_ = r.HandleApp(context.Background(), appPair.EndA(), m, nil)
	}()

	// First browser connection.
	shell1Pair := wiretest.NewPipePair()
	shell1 := shell1Pair.EndB()
	shell1Done := make(chan struct{})
	go func() { defer close(shell1Done); _ = r.HandleShell(context.Background(), shell1Pair.EndA()) }()

	writeCtrl(t, app, wire.NewIdentity("com.wash.disptest", ProtocolVersion, "0.9.0"))
	ack, ok := readCtrl(t, app).(wire.IdentityAck)
	if !ok || ack.WindowID == 0 {
		t.Fatalf("expected IdentityAck with a window, got %+v", ack)
	}
	primary := ack.WindowID
	if ev, ok := readEvt(t, app).(wire.EvtWindowMapped); !ok || ev.Win != primary {
		t.Fatalf("expected EvtWindowMapped(%d), got %+v", primary, ev)
	}
	if got := waitWindowUpsert(t, shell1, primary); got.Icon != "terminal" || got.Accent != "#33aaff" {
		t.Fatalf("first sight of primary: icon=%q accent=%q", got.Icon, got.Accent)
	}

	writeEvt(t, app, wire.NewEvtWindowCreate(1, wire.WindowRoleToplevel, 0, "Second", 300, 200, ""))
	created, ok := readEvt(t, app).(wire.EvtWindowCreated)
	if !ok || created.ReqID != 1 {
		t.Fatalf("expected EvtWindowCreated req=1, got %+v", created)
	}
	second := created.Win
	waitWindowUpsert(t, shell1, second)

	// The browser goes away. The app must outlive it (GH #23), and its
	// records with it.
	shell1Pair.Close()
	waitClose(t, shell1Done)

	// A retitle while no browser is attached rewrites the record.
	r.broadcastPatches(r.winSession.setTitle(primary, "renamed"))

	// The reconnect: a fresh shell connection gets the snapshot.
	shell2Pair := wiretest.NewPipePair()
	shell2 := shell2Pair.EndB()
	shell2Done := make(chan struct{})
	go func() { defer close(shell2Done); _ = r.HandleShell(context.Background(), shell2Pair.EndA()) }()

	var snap *wire.ShellSessionSnapshot
	for i := 0; i < 50 && snap == nil; i++ {
		if v, ok := readCtrl(t, shell2).(wire.ShellSessionSnapshot); ok {
			snap = &v
		}
	}
	if snap == nil {
		t.Fatal("no session.snapshot on reconnect within 50 frames")
	}
	got := map[uint32]wire.SessionWindow{}
	for _, w := range snap.Windows {
		got[w.WindowID] = w
	}
	for _, id := range []uint32{primary, second} {
		w, ok := got[id]
		if !ok {
			t.Fatalf("window %d missing from reconnect snapshot: %+v", id, snap.Windows)
		}
		if w.Icon != "terminal" || w.Accent != "#33aaff" {
			t.Errorf("window %d after reconnect: icon=%q accent=%q, want manifest's", id, w.Icon, w.Accent)
		}
		if w.InstanceID != ack.InstanceID {
			t.Errorf("window %d after reconnect: instance %q, want %q (the app was respawned)", id, w.InstanceID, ack.InstanceID)
		}
	}
	if got[primary].Title != "renamed" {
		t.Errorf("primary title = %q, want the retitle", got[primary].Title)
	}

	appPair.Close()
	shell2Pair.Close()
	waitClose(t, appDone)
	waitClose(t, shell2Done)
}
