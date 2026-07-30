package agentd

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)

// reset gives each test a clean roster (the package's state is process-
// global, exactly like the other wash services').
func reset() {
	rows = map[string]*row{}
}

// put is what the agent_status handler does to the roster, minus the SDK.
func put(key string, r Row, lastSeen, stateSince time.Time) {
	rows[key] = &row{Row: r, lastSeen: lastSeen, stateSince: stateSince}
}

// The roster is a queue of things to attend to, so the order it publishes
// in IS the feature: whoever is blocked on a human comes first, and within
// a state the longest wait wins.
func TestPublishSortsByAttention(t *testing.T) {
	reset()
	put("a:1", Row{Key: "a:1", Agent: "claude", State: "working"}, t0, t0.Add(-30*time.Second))
	put("a:2", Row{Key: "a:2", Agent: "claude", State: "done"}, t0, t0.Add(-5*time.Second))
	put("a:3", Row{Key: "a:3", Agent: "aider", State: "needs-input"}, t0, t0.Add(-10*time.Second))
	put("a:4", Row{Key: "a:4", Agent: "codex", State: "needs-input"}, t0, t0.Add(-90*time.Second))
	put("a:5", Row{Key: "a:5", Agent: "amp", State: "running"}, t0, t0.Add(-time.Second))

	got := publish(t0)
	want := []string{"a:4", "a:3", "a:1", "a:5", "a:2"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, key := range want {
		if got[i].Key != key {
			t.Errorf("row %d = %s (%s), want %s", i, got[i].Key, got[i].State, key)
		}
	}
	// The longest-waiting needs-input row leads, and its elapsed is
	// computed at publish time.
	if got[0].SinceMS != 90_000 {
		t.Errorf("since_ms = %d, want 90000", got[0].SinceMS)
	}
}

// A stale row is published as such (grey in the sidebar), not silently
// dropped — "the terminal stopped answering" is information.
func TestPublishRendersStale(t *testing.T) {
	reset()
	put("a:1", Row{Key: "a:1", Agent: "claude", State: "working", Stale: true}, t0, t0.Add(-time.Minute))
	got := publish(t0)
	if len(got) != 1 || got[0].State != "stale" {
		t.Fatalf("got %+v, want one stale row", got)
	}
	// …and it sorts last, behind everything live.
	put("a:2", Row{Key: "a:2", Agent: "claude", State: "done"}, t0, t0)
	got = publish(t0)
	if got[len(got)-1].Key != "a:1" {
		t.Errorf("stale row is not last: %+v", got)
	}
}

// Liveness: a terminal that crashes never says goodbye. The sweep is what
// stops a dead window leaving a ghost row forever.
func TestSweepAges(t *testing.T) {
	cases := []struct {
		name      string
		age       time.Duration
		wantStale bool
		wantGone  bool
	}{
		{"fresh", time.Second, false, false},
		{"just inside the stale window", staleAfter - time.Second, false, false},
		{"past stale", staleAfter + time.Second, true, false},
		{"past drop", dropAfter + time.Second, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reset()
			now := t0.Add(c.age)
			put("a:1", Row{Key: "a:1", Agent: "claude", State: "working"}, t0, t0)

			// The sweep's body, without the ticker/StateService around it.
			for key, r := range rows {
				switch age := now.Sub(r.lastSeen); {
				case age > dropAfter:
					delete(rows, key)
				case age > staleAfter && !r.Stale:
					r.Stale = true
				}
			}
			r, present := rows["a:1"]
			if c.wantGone {
				if present {
					t.Fatalf("row survived %v of silence", c.age)
				}
				return
			}
			if !present {
				t.Fatalf("row dropped after only %v", c.age)
			}
			if r.Stale != c.wantStale {
				t.Errorf("stale = %v, want %v after %v", r.Stale, c.wantStale, c.age)
			}
		})
	}
}

// A keepalive for an unchanged state must not restart the elapsed clock —
// otherwise "waiting 5 minutes" would read as "waiting 15 seconds"
// forever, which is exactly the number the roster exists to show.
func TestKeepaliveKeepsTheClock(t *testing.T) {
	reset()
	put("a:1", Row{Key: "a:1", Agent: "claude", State: "needs-input"}, t0, t0)

	// Simulate the handler's decision for a same-state re-statement.
	now := t0.Add(2 * time.Minute)
	r := rows["a:1"]
	if r.State == "needs-input" && r.Reason == "" {
		// unchanged → stateSince untouched, lastSeen refreshed
		r.lastSeen = now
	}
	if got := publish(now)[0].SinceMS; got != 120_000 {
		t.Errorf("since_ms = %d, want 120000 (the clock restarted)", got)
	}
}

func TestRowKeyIsPerTab(t *testing.T) {
	if rowKey("i-7", 3) == rowKey("i-7", 4) {
		t.Error("two tabs of one terminal share a key")
	}
	if rowKey("i-7", 3) == rowKey("i-8", 3) {
		t.Error("two terminals' tabs share a key")
	}
	if got := rowKey("i-7", 42); got != "i-7:42" {
		t.Errorf("rowKey = %q", got)
	}
}

func TestDirLabel(t *testing.T) {
	cases := map[string]string{
		"/home/mick/wash":  "wash",
		"/home/mick/wash/": "wash",
		"/":                "/",
		"":                 "/",
		"relative/path":    "path",
	}
	for in, want := range cases {
		if got := dirLabel(in); got != want {
			t.Errorf("dirLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStatePriorityCoversEveryState(t *testing.T) {
	// Every state the terminal can report must sort ahead of stale, and
	// needs-input ahead of all of them.
	states := []string{"needs-input", "working", "running", "done", "stale"}
	for i := 1; i < len(states); i++ {
		if statePriority(states[i-1]) >= statePriority(states[i]) {
			t.Errorf("%s does not sort ahead of %s", states[i-1], states[i])
		}
	}
	// A state a newer terminal invents lands at the back rather than
	// jumping the queue.
	if statePriority("teleporting") < statePriority("stale") {
		t.Error("an unknown state outranks stale")
	}
}

func TestElapsedMS(t *testing.T) {
	if got := elapsedMS(time.Time{}, t0); got != 0 {
		t.Errorf("zero since → %d", got)
	}
	if got := elapsedMS(t0, t0.Add(1500*time.Millisecond)); got != 1500 {
		t.Errorf("got %d, want 1500", got)
	}
	if got := elapsedMS(t0.Add(time.Second), t0); got != 0 {
		t.Errorf("backwards clock → %d, want 0", got)
	}
}

func TestItoa(t *testing.T) {
	cases := map[uint64]string{0: "0", 7: "7", 42: "42", 1234567890: "1234567890"}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Errorf("itoa(%d) = %q, want %q", in, got, want)
		}
	}
}
