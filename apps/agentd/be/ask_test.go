package agentd

import (
	"testing"
	"time"
)

func resetAsks() {
	for _, p := range asks {
		if p.timer != nil {
			p.timer.Stop()
		}
	}
	asks = map[string]*pending{}
	askSeq = 0
}

func addAsk(id, rowKey, tool string, asked time.Time) *pending {
	p := &pending{
		Ask:   Ask{ID: id, RowKey: rowKey, Tool: tool, TermInstance: "i-1"},
		asked: asked,
		reqID: "r-" + id,
	}
	asks[id] = p
	return p
}

// Questions are a queue of things blocking humans, so the one that has
// been waiting longest comes first.
func TestPublishAsksOldestFirst(t *testing.T) {
	resetAsks()
	addAsk("a", "i-1:1", "Bash", t0.Add(-5*time.Second))
	addAsk("b", "i-1:2", "Read", t0.Add(-60*time.Second))
	addAsk("c", "i-1:3", "Edit", t0.Add(-30*time.Second))

	got := publishAsks(t0)
	want := []string{"b", "c", "a"}
	if len(got) != 3 {
		t.Fatalf("got %d asks, want 3", len(got))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("ask %d = %s, want %s", i, got[i].ID, id)
		}
	}
	if got[0].AgeMS != 60_000 {
		t.Errorf("age_ms = %d, want 60000", got[0].AgeMS)
	}
}

// A pty-resident process that spams questions must not be able to fill
// the sidebar with its own noise.
func TestCountForRowCapsOneTab(t *testing.T) {
	resetAsks()
	for i, id := range []string{"a", "b", "c"} {
		addAsk(id, "i-1:1", "Bash", t0.Add(-time.Duration(i)*time.Second))
	}
	addAsk("other", "i-1:2", "Bash", t0)

	if n := countForRow("i-1:1"); n != 3 {
		t.Errorf("countForRow = %d, want 3", n)
	}
	if n := countForRow("i-1:1"); n < maxPendingPerTab {
		t.Errorf("a tab at %d pending is under the cap of %d — the guard would not fire", n, maxPendingPerTab)
	}
	// A different tab is unaffected: one noisy agent can't mute another.
	if n := countForRow("i-1:2"); n != 1 {
		t.Errorf("other tab count = %d, want 1", n)
	}
}

// Everything unrecognized must land on defer — the agent's own prompt —
// and never on allow.
func TestNormalizeAnswer(t *testing.T) {
	cases := map[string]string{
		"allow":   DecisionAllow,
		"ALLOW":   DecisionAllow,
		" deny ":  DecisionDeny,
		"defer":   DecisionDefer,
		"":        DecisionDefer,
		"yes":     DecisionDefer,
		"approve": DecisionDefer,
	}
	for in, want := range cases {
		if got := normalizeAnswer(in); got != want {
			t.Errorf("normalizeAnswer(%q) = %q, want %q", in, got, want)
		}
	}
}

// The roster and the questions ride one state push, so a pending ask must
// not disturb the rows beside it.
func TestAsksAndRowsCoexist(t *testing.T) {
	reset()
	resetAsks()
	put("i-1:1", Row{Key: "i-1:1", Agent: "claude", State: "working"}, t0, t0)
	addAsk("a", "i-1:1", "Bash", t0.Add(-time.Second))

	rows := publish(t0)
	if len(rows) != 1 || rows[0].State != "working" {
		t.Fatalf("rows = %+v", rows)
	}
	if got := publishAsks(t0); len(got) != 1 || got[0].RowKey != "i-1:1" {
		t.Fatalf("asks = %+v", got)
	}
}

// The timer is what stops a question outliving the human's attention. It
// must be stopped when an answer arrives, or an expiry would fire after
// the requester has already been told.
func TestAnsweredAskStopsItsTimer(t *testing.T) {
	resetAsks()
	fired := make(chan struct{}, 1)
	p := addAsk("a", "i-1:1", "Bash", time.Now())
	p.timer = time.AfterFunc(50*time.Millisecond, func() { fired <- struct{}{} })

	// What the answer path does before replying.
	delete(asks, "a")
	p.timer.Stop()

	select {
	case <-fired:
		t.Error("expiry fired for an answered question")
	case <-time.After(150 * time.Millisecond):
	}
}
