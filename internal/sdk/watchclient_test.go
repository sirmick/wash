package sdk

import (
	"context"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
)

// readToService reads the next outbound EvtAppMsg, asserts it is addressed to
// the watch service, and returns its decoded payload.
func readToService(t *testing.T, router wire.FrameTransport) map[string]any {
	t.Helper()
	got := readEvt(t, router)
	m, ok := got.(wire.EvtAppMsg)
	if !ok {
		t.Fatalf("expected EvtAppMsg, got %T", got)
	}
	if m.To == nil || m.To.AppID != FsWatchAppID {
		t.Fatalf("recipient = %v, want AppID %q", m.To, FsWatchAppID)
	}
	return decodeAppMsgData(t, m.Data)
}

// TestWatchClientRelaysAndDispatches confirms Watch sends a watch to the
// service, an fs_event from the service reaches the callback (dispatched by
// parent dir), and Unwatch releases the watch.
func TestWatchClientRelaysAndDispatches(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	wc := NewWatchClient(bus.conn)
	go func() { _ = bus.conn.Run(context.Background()) }()

	got := make(chan WatchEvent, 4)
	if err := wc.Watch("/d", func(ev WatchEvent) { got <- ev }); err != nil {
		t.Fatalf("watch: %v", err)
	}

	// Watch must have relayed {kind:"watch", path:"/d"} to the service.
	data := readToService(t, router)
	if data["kind"] != "watch" || data["path"] != "/d" {
		t.Fatalf("relayed %v; want watch /d", data)
	}

	// A second watcher of the same path refcounts — no second relay.
	if err := wc.Watch("/d", func(WatchEvent) {}); err != nil {
		t.Fatalf("watch 2: %v", err)
	}

	// An fs_event from the service for a child of /d reaches the callback.
	writeEvt(t, router, wire.NewEvtAppMsgFrom(0, map[string]any{
		"kind": "fs_event", "op": "modified", "path": "/d/x",
	}, wire.Sender{AppID: FsWatchAppID, InstanceID: "i-svc"}))

	select {
	case ev := <-got:
		if ev.Path != "/d/x" || ev.Op != "modified" {
			t.Fatalf("callback got %+v; want modified /d/x", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callback not invoked for /d/x")
	}

	// An event from some other app must be ignored.
	writeEvt(t, router, wire.NewEvtAppMsgFrom(0, map[string]any{
		"kind": "fs_event", "op": "created", "path": "/d/evil",
	}, wire.Sender{AppID: "com.wash.attacker", InstanceID: "i-x"}))
	select {
	case ev := <-got:
		t.Fatalf("callback fired for untrusted sender: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}

	// First Unwatch only decrements (second watcher still holds it): no relay.
	wc.Unwatch("/d")
	// Last Unwatch releases: relays {kind:"unwatch", path:"/d"}.
	wc.Unwatch("/d")
	data = readToService(t, router)
	if data["kind"] != "unwatch" || data["path"] != "/d" {
		t.Fatalf("relayed %v; want unwatch /d", data)
	}
}
