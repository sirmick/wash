package agentclient

import "testing"

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
