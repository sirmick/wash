package sdk

import (
	"context"
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

// jobsState is the per-test payload — a small struct exercising both
// a scalar (Count) and a slice (Jobs) field so we can confirm the
// generic encodes/decodes structured types correctly.
type jobsState struct {
	Count int      `json:"count"`
	Jobs  []string `json:"jobs"`
}

// readStateMsg reads the next EvtAppMsg from the router pipe, decodes
// its data, and returns the state field as a map. Asserts the
// envelope shape (kind:"state") before unpacking.
func readStateMsg(t *testing.T, router wire.FrameTransport) map[string]any {
	t.Helper()
	got := readEvt(t, router)
	m, ok := got.(wire.EvtAppMsg)
	if !ok {
		t.Fatalf("expected EvtAppMsg, got %T", got)
	}
	data := decodeAppMsgData(t, m.Data)
	if data["kind"] != StateServiceKindState {
		t.Fatalf("kind=%v, want %q", data["kind"], StateServiceKindState)
	}
	state, ok := data["state"].(map[string]any)
	if !ok {
		t.Fatalf("state field shape: %T (%v)", data["state"], data["state"])
	}
	return state
}

// readStateMsgToInstance is readStateMsg with an additional check
// that the outbound message is addressed to inst. The router relays
// cross-app sends to a specific instance, so the To field must match.
func readStateMsgToInstance(t *testing.T, router wire.FrameTransport, inst string) map[string]any {
	t.Helper()
	got := readEvt(t, router)
	m, ok := got.(wire.EvtAppMsg)
	if !ok {
		t.Fatalf("expected EvtAppMsg, got %T", got)
	}
	if m.To == nil || m.To.InstanceID != inst {
		t.Fatalf("recipient=%v, want InstanceID=%q", m.To, inst)
	}
	data := decodeAppMsgData(t, m.Data)
	if data["kind"] != StateServiceKindState {
		t.Fatalf("kind=%v, want %q", data["kind"], StateServiceKindState)
	}
	state, _ := data["state"].(map[string]any)
	return state
}

// TestStateServiceSubscribeReturnsSnapshot confirms a subscribe
// triggers an immediate state push of the current snapshot, even
// before any Mutate happens. This is the load-bearing semantic for
// quiescent state (a pending priv approval that won't tick again on
// its own — the subscriber must see it).
func TestStateServiceSubscribeReturnsSnapshot(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	svc := NewStateService(bus, jobsState{Count: 2, Jobs: []string{"a", "b"}})
	_ = svc

	go func() { _ = bus.conn.Run(context.Background()) }()

	// Router relays a cross-app subscribe from i-sub.
	writeEvt(t, router, wire.NewEvtAppMsgFrom(0, map[string]any{
		"kind": StateServiceKindSubscribe,
	}, wire.Sender{AppID: "com.wash.session", InstanceID: "i-sub"}))

	state := readStateMsgToInstance(t, router, "i-sub")
	if got, _ := state["count"].(float64); got != 2 {
		t.Fatalf("count=%v, want 2", state["count"])
	}
	jobs, _ := state["jobs"].([]any)
	if len(jobs) != 2 || jobs[0] != "a" || jobs[1] != "b" {
		t.Fatalf("jobs=%v, want [a b]", jobs)
	}
	if svc.SubscriberCount() != 1 {
		t.Fatalf("SubscriberCount=%d, want 1", svc.SubscriberCount())
	}
}

// TestStateServiceMutateFansToSubscribers confirms a Mutate after
// subscribe fans the updated state to every subscriber. The snapshot
// must reflect the post-Mutate state, not the pre-Mutate one.
func TestStateServiceMutateFansToSubscribers(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	svc := NewStateService(bus, jobsState{Count: 0})

	go func() { _ = bus.conn.Run(context.Background()) }()

	writeEvt(t, router, wire.NewEvtAppMsgFrom(0, map[string]any{
		"kind": StateServiceKindSubscribe,
	}, wire.Sender{InstanceID: "i-sub"}))
	// Drain the immediate snapshot.
	_ = readStateMsgToInstance(t, router, "i-sub")

	svc.Mutate(func(s *jobsState) {
		s.Count = 7
		s.Jobs = []string{"x"}
	})

	state := readStateMsgToInstance(t, router, "i-sub")
	if got, _ := state["count"].(float64); got != 7 {
		t.Fatalf("count=%v, want 7", state["count"])
	}
	jobs, _ := state["jobs"].([]any)
	if len(jobs) != 1 || jobs[0] != "x" {
		t.Fatalf("jobs=%v, want [x]", jobs)
	}
}

// TestStateServiceUnsubscribeStops confirms an unsubscribe removes the
// subscriber from the fan-out set. After unsubscribe, a Mutate must
// produce no outbound EvtAppMsg.
func TestStateServiceUnsubscribeStops(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	svc := NewStateService(bus, jobsState{})

	go func() { _ = bus.conn.Run(context.Background()) }()

	writeEvt(t, router, wire.NewEvtAppMsgFrom(0, map[string]any{
		"kind": StateServiceKindSubscribe,
	}, wire.Sender{InstanceID: "i-sub"}))
	_ = readStateMsgToInstance(t, router, "i-sub")

	writeEvt(t, router, wire.NewEvtAppMsgFrom(0, map[string]any{
		"kind": StateServiceKindUnsubscribe,
	}, wire.Sender{InstanceID: "i-sub"}))

	// Give the unsubscribe a beat to process — bus dispatch runs on
	// the read goroutine, so we need to let it land before Mutate.
	deadline := time.Now().Add(time.Second)
	for svc.SubscriberCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if svc.SubscriberCount() != 0 {
		t.Fatalf("SubscriberCount=%d after unsubscribe, want 0", svc.SubscriberCount())
	}

	svc.Mutate(func(s *jobsState) { s.Count = 99 })

	// Confirm nothing arrives within a short window. We can't easily
	// "expect no frame" against a blocking transport — read with a
	// short deadline via a goroutine.
	done := make(chan struct{})
	go func() {
		_, _ = router.ReadFrame()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Mutate after unsubscribe still produced a frame")
	case <-time.After(150 * time.Millisecond):
		// Expected: no frame.
	}
}

func TestStateServiceForgetSubscriberStops(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	svc := NewStateService(bus, jobsState{})

	go func() { _ = bus.conn.Run(context.Background()) }()

	writeEvt(t, router, wire.NewEvtAppMsgFrom(0, map[string]any{
		"kind": StateServiceKindSubscribe,
	}, wire.Sender{InstanceID: "i-sub"}))
	_ = readStateMsgToInstance(t, router, "i-sub")

	svc.ForgetSubscriber("i-sub")
	if svc.SubscriberCount() != 0 {
		t.Fatalf("SubscriberCount=%d after forget, want 0", svc.SubscriberCount())
	}

	svc.Mutate(func(s *jobsState) { s.Count = 99 })

	done := make(chan struct{})
	go func() {
		_, _ = router.ReadFrame()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Mutate after ForgetSubscriber still produced a frame")
	case <-time.After(150 * time.Millisecond):
	}
}

// TestStateServiceFansToMultipleSubscribers confirms two subscribers
// both receive each Mutate. Catches a regression where the iteration
// order or lock holding might drop a recipient.
func TestStateServiceFansToMultipleSubscribers(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	svc := NewStateService(bus, jobsState{})

	go func() { _ = bus.conn.Run(context.Background()) }()

	writeEvt(t, router, wire.NewEvtAppMsgFrom(0, map[string]any{
		"kind": StateServiceKindSubscribe,
	}, wire.Sender{InstanceID: "i-a"}))
	_ = readStateMsgToInstance(t, router, "i-a")

	writeEvt(t, router, wire.NewEvtAppMsgFrom(0, map[string]any{
		"kind": StateServiceKindSubscribe,
	}, wire.Sender{InstanceID: "i-b"}))
	_ = readStateMsgToInstance(t, router, "i-b")

	svc.Mutate(func(s *jobsState) { s.Count = 3 })

	// Each subscriber must receive the same snapshot, but the order
	// they arrive is implementation-defined (we iterate a map). Read
	// two frames and assert both recipients show up exactly once.
	seen := map[string]int{}
	for i := 0; i < 2; i++ {
		got := readEvt(t, router)
		m := got.(wire.EvtAppMsg)
		if m.To == nil {
			t.Fatalf("frame %d missing To", i)
		}
		seen[m.To.InstanceID]++
		data := decodeAppMsgData(t, m.Data)
		state, _ := data["state"].(map[string]any)
		if got, _ := state["count"].(float64); got != 3 {
			t.Fatalf("frame %d count=%v, want 3", i, state["count"])
		}
	}
	if seen["i-a"] != 1 || seen["i-b"] != 1 {
		t.Fatalf("recipients=%v, want i-a:1 i-b:1", seen)
	}
}

// TestStateServiceSnapshotIsCurrent confirms Snapshot() returns the
// current state after Mutate. Used by services that want to read
// their own state inline (e.g. for a list reply handler).
func TestStateServiceSnapshotIsCurrent(t *testing.T) {
	bus, _, cleanup := busTestConn(t)
	defer cleanup()

	svc := NewStateService(bus, jobsState{Count: 1})
	if svc.Snapshot().Count != 1 {
		t.Fatalf("initial Snapshot.Count=%d, want 1", svc.Snapshot().Count)
	}

	svc.Mutate(func(s *jobsState) { s.Count = 42 })
	if svc.Snapshot().Count != 42 {
		t.Fatalf("post-Mutate Snapshot.Count=%d, want 42", svc.Snapshot().Count)
	}
}

// TestStateServiceDoubleSubscribeIsIdempotent confirms a subscriber
// that re-subscribes (e.g. after losing FE state and re-attaching)
// doesn't double-fan on the next Mutate.
func TestStateServiceDoubleSubscribeIsIdempotent(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	svc := NewStateService(bus, jobsState{})

	go func() { _ = bus.conn.Run(context.Background()) }()

	for i := 0; i < 2; i++ {
		writeEvt(t, router, wire.NewEvtAppMsgFrom(0, map[string]any{
			"kind": StateServiceKindSubscribe,
		}, wire.Sender{InstanceID: "i-sub"}))
		_ = readStateMsgToInstance(t, router, "i-sub")
	}
	if svc.SubscriberCount() != 1 {
		t.Fatalf("SubscriberCount=%d after double-subscribe, want 1", svc.SubscriberCount())
	}

	svc.Mutate(func(s *jobsState) { s.Count = 5 })

	// Exactly one frame should arrive. Read one, then assert nothing
	// else within a short window.
	_ = readStateMsgToInstance(t, router, "i-sub")
	done := make(chan struct{})
	go func() {
		_, _ = router.ReadFrame()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("double-subscribe produced a duplicate fan")
	case <-time.After(150 * time.Millisecond):
	}
}
