package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// connectNotify mirrors the SDK's busTestConn pattern (internal/sdk/
// bus_test.go) but binds the notify service's onReady to the
// freshly-handshaken Conn. Returns the router pipe end for injecting
// inbound app_msgs and the live State pointer for assertions.
//
// The router-side end MUST be drained by the test to prevent the
// notify service's outbound writes from blocking on a full pipe. In
// particular, each intake re-emits an EvtNotify (for the shell
// broadcast path) — under burst, those frames pile up. Helpers below
// (drainUntilAppMsg, drainAllOutbound) keep the queue moving.
func connectNotify(t *testing.T) (wire.FrameTransport, func() State, func()) {
	t.Helper()
	pp := wiretest.NewPipePair()

	type connResult struct {
		c   *sdk.Conn
		err error
	}
	ch := make(chan connResult, 1)
	go func() {
		c, err := sdk.ConnectWith(pp.EndA(), def)
		ch <- connResult{c, err}
	}()

	// Drain the identity frame the SDK sends on connect; reply with
	// the IdentityAck so handshake completes. (Same shape as
	// internal/sdk/bus_test.go.)
	if _, err := pp.EndB().ReadFrame(); err != nil {
		t.Fatalf("read identity: %v", err)
	}
	ackPayload, err := wire.EncodeCtrl(wire.NewIdentityAck("i-notify", 0))
	if err != nil {
		t.Fatalf("encode ack: %v", err)
	}
	if err := pp.EndB().WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 0, Payload: ackPayload}); err != nil {
		t.Fatalf("write ack: %v", err)
	}

	res := <-ch
	if res.err != nil {
		t.Fatalf("connect: %v", res.err)
	}

	// onReady ran during ConnectWith → svc is wired up.
	if svc == nil {
		t.Fatal("svc not initialised by onReady")
	}

	go func() { _ = res.c.Run(context.Background()) }()

	snap := func() State { return svc.Snapshot() }
	cleanup := func() {
		res.c.Close()
		// Reset state for the next test in the same package.
		svc = nil
		nextID.Store(0)
	}
	return pp.EndB(), snap, cleanup
}

// drainAllOutbound spawns a goroutine that consumes every frame on
// router until the test ends. Use for tests that don't care about
// outbound shape but generate enough re-emit traffic to overflow the
// pipe buffer (HistoryCap-scale bursts). Returns a stop function the
// test calls if it later wants to read specific frames.
func drainAllOutbound(t *testing.T, router wire.FrameTransport) (stop func()) {
	t.Helper()
	stopCh := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopCh:
				return
			default:
				if _, err := router.ReadFrame(); err != nil {
					return
				}
			}
		}
	}()
	return func() { close(stopCh) }
}

// writeInboundNotify ships an app_msg{kind:"notify"} as if the
// router had forwarded one from the named producer.
func writeInboundNotify(t *testing.T, router wire.FrameTransport, fromAppID, fromInst string, title, body, level string) {
	t.Helper()
	payload := map[string]any{
		"kind":  "notify",
		"title": title,
		"body":  body,
		"level": level,
	}
	msg := wire.NewEvtAppMsgFrom(0, payload, wire.Sender{AppID: fromAppID, InstanceID: fromInst})
	b, err := wire.EncodeEvt(msg)
	if err != nil {
		t.Fatalf("encode evt: %v", err)
	}
	if err := router.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 1, Payload: b}); err != nil {
		t.Fatalf("write evt: %v", err)
	}
}

// readAppMsg reads frames from router until an EvtAppMsg appears,
// skipping the EvtNotify re-emits that the intake handler produces.
// Fails the test if timeout elapses before an EvtAppMsg arrives.
func readAppMsg(t *testing.T, router wire.FrameTransport, timeout time.Duration) wire.EvtAppMsg {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f, err := router.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if f.Channel != 1 {
			continue
		}
		m, err := wire.DecodeEvt(f.Payload)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if msg, ok := m.(wire.EvtAppMsg); ok {
			return msg
		}
		// Other frame kinds (e.g. EvtNotify re-emits) are dropped.
	}
	t.Fatalf("no EvtAppMsg arrived within %v", timeout)
	return wire.EvtAppMsg{}
}

// waitFor polls fn until it returns true or the deadline expires. Cheap
// alternative to deadline-aware bus dispatch in tests.
func waitFor(t *testing.T, label string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", label)
}

// TestNotifyIntakeAppendsToHistory confirms an inbound notify from
// some app produces a Notification entry in the service's state, with
// router-attested source fields preserved.
func TestNotifyIntakeAppendsToHistory(t *testing.T) {
	router, snap, cleanup := connectNotify(t)
	defer cleanup()

	writeInboundNotify(t, router, "com.wash.test", "i-test-1", "Hello", "from test", "info")

	waitFor(t, "first notification", func() bool {
		return len(snap().Notifications) == 1
	})
	n := snap().Notifications[0]
	if n.Title != "Hello" || n.Body != "from test" || n.Level != "info" {
		t.Fatalf("bad notification: %+v", n)
	}
	if n.SourceApp != "com.wash.test" || n.SourceInst != "i-test-1" {
		t.Fatalf("source not preserved: %+v", n)
	}
	if n.ID == "" {
		t.Fatal("ID is empty")
	}
	if n.Read {
		t.Fatal("new notification should not be marked read")
	}
}

// TestNotifyNewestFirst confirms history is ordered newest-first.
// Critical for the sidebar widget — it renders top-down.
func TestNotifyNewestFirst(t *testing.T) {
	router, snap, cleanup := connectNotify(t)
	defer cleanup()

	writeInboundNotify(t, router, "com.app.a", "i-1", "first", "", "info")
	waitFor(t, "first", func() bool { return len(snap().Notifications) == 1 })
	writeInboundNotify(t, router, "com.app.a", "i-1", "second", "", "info")
	waitFor(t, "second", func() bool { return len(snap().Notifications) == 2 })
	writeInboundNotify(t, router, "com.app.a", "i-1", "third", "", "info")
	waitFor(t, "third", func() bool { return len(snap().Notifications) == 3 })

	list := snap().Notifications
	if list[0].Title != "third" || list[1].Title != "second" || list[2].Title != "first" {
		t.Fatalf("ordering wrong: [%s, %s, %s], want [third, second, first]",
			list[0].Title, list[1].Title, list[2].Title)
	}
}

// TestNotifyHistoryCap confirms the service drops the oldest entry
// when crossing HistoryCap so memory doesn't grow unbounded under
// chatty producers.
func TestNotifyHistoryCap(t *testing.T) {
	router, snap, cleanup := connectNotify(t)
	defer cleanup()
	// HistoryCap+1 intakes each spawn a re-emit goroutine; without a
	// drainer they'd overflow the 32-frame pipe buffer and pin those
	// goroutines forever.
	stopDrain := drainAllOutbound(t, router)
	defer stopDrain()

	// Drop one over the cap. After the (cap+1)-th the oldest must be gone.
	for i := 0; i < HistoryCap+1; i++ {
		writeInboundNotify(t, router, "com.app.spammy", "i-9", "msg", "", "info")
	}
	waitFor(t, "cap reached", func() bool {
		return len(snap().Notifications) == HistoryCap
	})
	// Hold for a beat to be sure no extra is queued.
	time.Sleep(50 * time.Millisecond)
	if got := len(snap().Notifications); got != HistoryCap {
		t.Fatalf("len=%d, want %d", got, HistoryCap)
	}
}

// TestNotifyLevelDefaultsToInfo confirms an unknown / empty level
// gets normalised to "info" rather than passed through verbatim.
// Matches existing producer behaviour (sdk.Conn.Notify maps empty to
// info before sending) and keeps the consumer simple.
func TestNotifyLevelDefaultsToInfo(t *testing.T) {
	router, snap, cleanup := connectNotify(t)
	defer cleanup()

	writeInboundNotify(t, router, "com.app.a", "i-1", "no-level", "", "")
	waitFor(t, "intake", func() bool { return len(snap().Notifications) == 1 })
	if got := snap().Notifications[0].Level; got != "info" {
		t.Fatalf("level=%q, want info", got)
	}

	writeInboundNotify(t, router, "com.app.a", "i-1", "bogus", "", "supernova")
	waitFor(t, "intake 2", func() bool { return len(snap().Notifications) == 2 })
	if got := snap().Notifications[0].Level; got != "info" {
		t.Fatalf("bogus level=%q, want info (normalised)", got)
	}
}

// TestNotifyMarkRead confirms a mark_read for an existing id flips
// Read on the right entry and leaves the rest alone.
func TestNotifyMarkRead(t *testing.T) {
	router, snap, cleanup := connectNotify(t)
	defer cleanup()

	writeInboundNotify(t, router, "com.app.a", "i-1", "a", "", "info")
	writeInboundNotify(t, router, "com.app.a", "i-1", "b", "", "info")
	waitFor(t, "intake", func() bool { return len(snap().Notifications) == 2 })

	// Pick the older one (last in the list) for the mark; assertion
	// verifies the right one flipped, not just any one.
	older := snap().Notifications[1]
	payload := map[string]any{"kind": "mark_read", "id": older.ID}
	msg := wire.NewEvtAppMsgFrom(0, payload, wire.Sender{AppID: "com.wash.session", InstanceID: "i-sub"})
	b, _ := wire.EncodeEvt(msg)
	_ = router.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 1, Payload: b})

	waitFor(t, "mark_read applied", func() bool {
		for _, n := range snap().Notifications {
			if n.ID == older.ID && n.Read {
				return true
			}
		}
		return false
	})
	// The newer one MUST still be unread.
	for _, n := range snap().Notifications {
		if n.ID != older.ID && n.Read {
			t.Fatalf("mark_read flipped wrong entry: %+v", n)
		}
	}
}

// TestNotifyClearAll confirms clear_all empties the history without
// affecting subsequent intakes.
func TestNotifyClearAll(t *testing.T) {
	router, snap, cleanup := connectNotify(t)
	defer cleanup()

	writeInboundNotify(t, router, "com.app.a", "i-1", "a", "", "info")
	waitFor(t, "first", func() bool { return len(snap().Notifications) == 1 })

	payload := map[string]any{"kind": "clear_all"}
	msg := wire.NewEvtAppMsgFrom(0, payload, wire.Sender{AppID: "com.wash.session", InstanceID: "i-sub"})
	b, _ := wire.EncodeEvt(msg)
	_ = router.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 1, Payload: b})

	waitFor(t, "cleared", func() bool { return len(snap().Notifications) == 0 })

	// Next intake repopulates with a fresh, monotonically-larger ID.
	writeInboundNotify(t, router, "com.app.a", "i-1", "after", "", "info")
	waitFor(t, "post-clear", func() bool { return len(snap().Notifications) == 1 })
}

// TestNotifyStateWireShape confirms the JSON shape of a state push is
// stable — newest-first ordering, required fields populated, snake_case
// keys. The FE widget relies on this contract.
func TestNotifyStateWireShape(t *testing.T) {
	router, _, cleanup := connectNotify(t)
	defer cleanup()

	writeInboundNotify(t, router, "com.app.x", "i-x", "live", "tick", "warn")
	// Wait for the intake to land before subscribing — otherwise
	// subscribe's snapshot races the intake's Mutate and we'd see an
	// empty state.
	waitFor(t, "intake", func() bool {
		return len(svc.Snapshot().Notifications) == 1
	})

	subscribePayload := map[string]any{"kind": "subscribe"}
	subMsg := wire.NewEvtAppMsgFrom(0, subscribePayload, wire.Sender{InstanceID: "i-sub"})
	subBytes, _ := wire.EncodeEvt(subMsg)
	_ = router.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 1, Payload: subBytes})

	// Skip the re-emit EvtNotify the intake produces (single-authority
	// toast path; the test isn't asserting on that frame).
	m := readAppMsg(t, router, 2*time.Second)
	var env map[string]any
	if err := json.Unmarshal(m.Data, &env); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if env["kind"] != "state" {
		t.Fatalf("kind=%v", env["kind"])
	}
	state, ok := env["state"].(map[string]any)
	if !ok {
		t.Fatalf("state shape: %T", env["state"])
	}
	notes, ok := state["notifications"].([]any)
	if !ok || len(notes) != 1 {
		t.Fatalf("notifications=%v", state["notifications"])
	}
	n := notes[0].(map[string]any)
	// snake_case keys; spot-check the ones the FE will read.
	for _, k := range []string{"id", "source_app", "source_instance", "title", "level", "created_sec", "read"} {
		if _, ok := n[k]; !ok {
			t.Fatalf("notification missing key %q (have: %s)", k, strings.Join(mapKeys(n), ", "))
		}
	}
	if n["title"] != "live" || n["level"] != "warn" {
		t.Fatalf("payload wrong: %v", n)
	}
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
