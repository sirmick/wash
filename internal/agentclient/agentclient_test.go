package agentclient

import (
	"sync"
	"testing"
	"time"
)

// Handle is the half worth testing without a live conn: it decides what an
// agentd payload means and, crucially, WHICH session it belongs to. The
// send half is a one-line map literal per verb, exercised end to end by the
// agent e2e specs.

func TestHandleRoutesTranscriptByKey(t *testing.T) {
	var got []string
	cl := New(nil, Handlers{
		Event:    func(key string, _ any) { got = append(got, "event:"+key) },
		Snapshot: func(key string, _ any) { got = append(got, "snap:"+key) },
	})
	// Two sessions in one host — the case wash-ai never had.
	cl.keys["a"] = true
	cl.keys["b"] = true

	for _, m := range []map[string]any{
		{"kind": "transcript_event", "key": "a", "event": 1},
		{"kind": "transcript_snapshot", "key": "b", "events": []any{}},
		// Another host's session, on the same agentd: must not be painted
		// into ours.
		{"kind": "transcript_event", "key": "someone-else", "event": 2},
	} {
		if !cl.Handle(m) {
			t.Errorf("Handle(%v) = false, want true (agentd owns this kind)", m["kind"])
		}
	}

	want := []string{"event:a", "snap:b"}
	if len(got) != len(want) {
		t.Fatalf("delivered %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delivered %v, want %v", got, want)
		}
	}
}

func TestHandleStartedCarriesTheRequestID(t *testing.T) {
	type call struct{ req, key, sid, err string }
	var got []call
	cl := New(nil, Handlers{
		Started: func(req, key, sid, err string) { got = append(got, call{req, key, sid, err}) },
	})

	cl.Handle(map[string]any{"kind": "agent_started", "req_id": "s1", "key": "k1", "session_id": "sess1"})
	// A failed start has no key at all — the request id is the ONLY thing
	// tying it back to the tab that asked.
	cl.Handle(map[string]any{"kind": "agent_started", "req_id": "s2", "error": "no such adapter"})
	// A resume arrives as an attach: a key with no request behind it.
	cl.Handle(map[string]any{"kind": "attach", "key": "k3", "session_id": "sess3"})

	want := []call{
		{"s1", "k1", "sess1", ""},
		{"s2", "", "", "no such adapter"},
		{"", "k3", "sess3", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestHandleIgnoresForeignKinds(t *testing.T) {
	cl := New(nil, Handlers{})
	// Not agentd's vocabulary: the host must be free to handle its own
	// messages after Handle declines them.
	if cl.Handle(map[string]any{"kind": "open"}) {
		t.Error(`Handle("open") = true, want false`)
	}
	if cl.Handle("not a map") {
		t.Error("Handle(non-map) = true, want false")
	}
}

func TestNilHandlersDropRatherThanPanic(t *testing.T) {
	cl := New(nil, Handlers{})
	cl.keys["a"] = true
	// A host that only sends is legal; delivering to it must not panic.
	cl.Handle(map[string]any{"kind": "transcript_event", "key": "a", "event": 1})
	cl.Handle(map[string]any{"kind": "agent_started", "req_id": "s1", "key": "k"})
	cl.Handle(map[string]any{"kind": "state", "state": map[string]any{}})
}

func TestForgetStopsRouting(t *testing.T) {
	n := 0
	cl := New(nil, Handlers{Event: func(string, any) { n++ }})
	cl.keys["a"] = true
	cl.Handle(map[string]any{"kind": "transcript_event", "key": "a"})
	cl.Forget("a")
	// Closing a tab stops its events reaching us; the SESSION is untouched,
	// which is what makes Resume possible.
	cl.Handle(map[string]any{"kind": "transcript_event", "key": "a"})
	if n != 1 {
		t.Errorf("delivered %d events, want 1 (the one before Forget)", n)
	}
}

// The keepalive is the client's job, not each host's. agentd expires a
// transcript watcher it has not heard from within WatcherTTL, so a host
// that subscribes once and goes quiet stops receiving events after a
// minute — the transcript freezes while the roster (kept alive by the
// StateService itself) carries on. wash-edit's tabs never re-affirmed;
// wash-ai did so only on two of its four attach paths.
func TestWatchKeepsReaffirmingEveryWatchedKey(t *testing.T) {
	var mu sync.Mutex
	var sent []map[string]any
	cl := New(nil, Handlers{})
	cl.sendTo = func(m map[string]any) error {
		mu.Lock()
		sent = append(sent, m)
		mu.Unlock()
		return nil
	}
	cl.refresh = 5 * time.Millisecond
	defer cl.Close()

	// Two tabs, both watched: an editor hosts several at once, and every
	// one of them must stay subscribed — not just the most recent.
	if err := cl.Watch("a"); err != nil {
		t.Fatal(err)
	}
	if err := cl.Watch("b"); err != nil {
		t.Fatal(err)
	}

	count := func(key string) (n int) {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range sent {
			if m["kind"] == "transcript_subscribe" && m["key"] == key {
				// A keepalive must not ask for a replay: agentd answers a
				// repeat subscribe with nothing, which is the point.
				if r, _ := m["replay"].(bool); r {
					t.Errorf("keepalive for %s asked for a replay", key)
				}
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (count("a") < 3 || count("b") < 3) {
		time.Sleep(5 * time.Millisecond)
	}
	if count("a") < 3 || count("b") < 3 {
		t.Fatalf("keepalives a=%d b=%d after 2s, want the ticker to re-affirm both (refresh=%s)", count("a"), count("b"), cl.refresh)
	}

	// A forgotten key stops being re-affirmed: the session is untouched,
	// but this host no longer claims to be watching it.
	cl.Forget("b")
	// A tick copies the watched keys under the lock and sends after it, so
	// one that copied them just before Forget can still send "b" once. Let
	// that in-flight tick land before taking the baseline: the claim is
	// that no LATER tick re-affirms it.
	time.Sleep(3 * cl.refresh)
	base := count("b")
	time.Sleep(40 * time.Millisecond)
	if got := count("b"); got != base {
		t.Errorf("forgotten key kept being re-affirmed: %d → %d", base, got)
	}
}

// Close ends the ticker. One goroutine per client, stopped once — never
// one per Watch, which is the leak the old per-attach goroutine had.
func TestCloseStopsTheKeepalive(t *testing.T) {
	var mu sync.Mutex
	n := 0
	cl := New(nil, Handlers{})
	cl.sendTo = func(m map[string]any) error {
		mu.Lock()
		n++
		mu.Unlock()
		return nil
	}
	cl.refresh = 2 * time.Millisecond
	_ = cl.Watch("a")
	_ = cl.Watch("a") // a second Watch must not start a second ticker
	time.Sleep(30 * time.Millisecond)
	cl.Close()
	cl.Close() // idempotent
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	after := n
	mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	later := n
	mu.Unlock()
	if later != after {
		t.Errorf("keepalive kept sending after Close: %d → %d", after, later)
	}
	if after < 5 {
		t.Errorf("only %d sends in 30ms at a 2ms refresh — the ticker never ran", after)
	}
}

// The TTL and the refresh come from one place, so agentd and every host
// read the same clock; the env seam shrinks both together for tests.
func TestWatcherClockIsOneSeam(t *testing.T) {
	t.Setenv(watcherTTLEnv, "")
	if got := WatcherTTL(); got != defaultWatcherTTL {
		t.Errorf("default TTL = %s, want %s", got, defaultWatcherTTL)
	}
	if got := WatcherRefresh(); got != defaultWatcherTTL/4 {
		t.Errorf("default refresh = %s, want TTL/4", got)
	}
	t.Setenv(watcherTTLEnv, "2s")
	if got := WatcherTTL(); got != 2*time.Second {
		t.Errorf("TTL under env = %s, want 2s", got)
	}
	if got := WatcherRefresh(); got != 500*time.Millisecond {
		t.Errorf("refresh under env = %s, want 500ms", got)
	}
	t.Setenv(watcherTTLEnv, "garbage")
	if got := WatcherTTL(); got != defaultWatcherTTL {
		t.Errorf("TTL under a bad env = %s, want the default", got)
	}
}
