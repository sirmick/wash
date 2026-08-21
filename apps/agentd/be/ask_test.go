package agentd

import (
	"sync"
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
		Ask:   Ask{ID: id, RowKey: rowKey, Tool: tool, SourceApp: "com.wash.term", SourceInstance: "i-1"},
		asked: asked,
		reply: func(string, string) error { return nil },
	}
	asks[id] = p
	return p
}

// withState stands in for the live StateService: `subs` subscribers, and a
// Mutate that runs the function under a lock. The lock is not incidental —
// the real StateService.Mutate serializes every touch of the `asks` and
// `rows` maps, and a stub without it would let a test race on state that
// production never races on. Restores the real hooks on cleanup.
func withState(t *testing.T, subs int) {
	t.Helper()
	oldSubs, oldMutate := stateSubscribers, mutateState
	var st State
	var mu sync.Mutex
	stateSubscribers = func() int { return subs }
	mutateState = func(fn func(*State)) {
		mu.Lock()
		defer mu.Unlock()
		fn(&st)
	}
	t.Cleanup(func() { stateSubscribers, mutateState = oldSubs, oldMutate })
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
	if n := countForRow("i-1:1"); n < maxPendingPerRow {
		t.Errorf("a tab at %d pending is under the cap of %d — the guard would not fire", n, maxPendingPerRow)
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

// The queue's contract with every producer: exactly one answer, on every
// path out. A producer that had to reason about which branch answered it
// would eventually get one wrong and hang an agent.
func TestEnqueueAskAnswersExactlyOnce(t *testing.T) {
	cases := []struct {
		name     string
		subs     int
		prefill  int
		wantEnq  bool
		wantWhy  string
		wantCall int
	}{
		{name: "nobody home", subs: 0, wantEnq: false, wantWhy: "no desktop", wantCall: 1},
		{name: "row at cap", subs: 1, prefill: maxPendingPerRow, wantEnq: false, wantWhy: "too many pending", wantCall: 1},
		{name: "queued", subs: 1, wantEnq: true, wantCall: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetAsks()
			withState(t, tc.subs)
			for i := 0; i < tc.prefill; i++ {
				addAsk("p"+itoa(uint64(i)), "row-1", "Bash", t0)
			}

			var calls int
			var gotDecision, gotWhy string
			ok := enqueueAsk(askSpec{Agent: "codex", Tool: "Bash", RowKey: "row-1"},
				func(decision, why string) error {
					calls++
					gotDecision, gotWhy = decision, why
					return nil
				})

			if ok != tc.wantEnq {
				t.Errorf("enqueued = %v, want %v", ok, tc.wantEnq)
			}
			if calls != tc.wantCall {
				t.Fatalf("reply called %d times, want %d", calls, tc.wantCall)
			}
			if tc.wantCall > 0 {
				if gotDecision != DecisionDefer {
					t.Errorf("decision = %q, want %q — an unanswerable question must never allow", gotDecision, DecisionDefer)
				}
				if gotWhy != tc.wantWhy {
					t.Errorf("why = %q, want %q", gotWhy, tc.wantWhy)
				}
			}
		})
	}
}

// The reply route is the closure, not a field on the wire: a question from
// a session agentd hosts itself has no instance to send an app_msg to.
// Expiry must reach it just the same.
func TestExpireUsesTheProducersReplyRoute(t *testing.T) {
	resetAsks()
	withState(t, 1)

	answered := make(chan string, 1)
	if ok := enqueueAsk(askSpec{Agent: "codex", Tool: "Bash", RowKey: "row-1"},
		func(decision, _ string) error { answered <- decision; return nil }); !ok {
		t.Fatal("enqueue refused with a subscriber present")
	}
	var id string
	for k := range asks {
		id = k
	}

	expireAsk(id)

	select {
	case got := <-answered:
		if got != DecisionDefer {
			t.Errorf("expiry decision = %q, want %q", got, DecisionDefer)
		}
	default:
		t.Fatal("expiry did not answer the producer — its agent would hang")
	}
	if _, still := asks[id]; still {
		t.Error("expired question left in the queue")
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

// withVaryingState is withState for tests that need the desktop to come and
// go mid-test — the case the pause exists for. The returned setter changes
// what stateSubscribers() reports.
func withVaryingState(t *testing.T, subs int) func(int) {
	t.Helper()
	oldSubs, oldMutate := stateSubscribers, mutateState
	var st State
	var mu sync.Mutex
	n := subs
	stateSubscribers = func() int { mu.Lock(); defer mu.Unlock(); return n }
	mutateState = func(fn func(*State)) {
		mu.Lock()
		defer mu.Unlock()
		fn(&st)
	}
	t.Cleanup(func() { stateSubscribers, mutateState = oldSubs, oldMutate })
	return func(v int) { mu.Lock(); n = v; mu.Unlock() }
}

// enqueueOne queues a question and returns its id and a channel carrying
// whatever the producer is eventually told.
func enqueueOne(t *testing.T) (string, chan [2]string) {
	t.Helper()
	answered := make(chan [2]string, 4)
	if ok := enqueueAsk(askSpec{Agent: "codex", Tool: "Bash", RowKey: "row-1"},
		func(decision, why string) error { answered <- [2]string{decision, why}; return nil }); !ok {
		t.Fatal("enqueue refused with a subscriber present")
	}
	var id string
	for k := range asks {
		id = k
	}
	return id, answered
}

// The bug this file's askTTL comment describes: a question asked just
// before someone shut their laptop must still be there when they open it.
// Expiry while nobody is attached is a denial nobody chose, delivered to
// an agent that cannot tell it from a human clicking cancel.
func TestExpiryPausesWhileNoDesktopIsAttached(t *testing.T) {
	resetAsks()
	setSubs := withVaryingState(t, 1)
	id, answered := enqueueOne(t)

	setSubs(0) // the lid closes
	expireAsk(id)

	if _, still := asks[id]; !still {
		t.Fatal("question dropped while nobody could answer it — that is the auto-reject bug")
	}
	select {
	case got := <-answered:
		t.Fatalf("producer was told %v with no desktop attached", got)
	default:
	}

	setSubs(1) // and opens again
	expireAsk(id)

	select {
	case got := <-answered:
		if got[0] != DecisionDefer || got[1] != ReasonTimeout {
			t.Errorf("expiry reported %q/%q, want %q/%q", got[0], got[1], DecisionDefer, ReasonTimeout)
		}
	default:
		t.Fatal("expiry with a desktop attached did not answer — the agent would hang")
	}
	if _, still := asks[id]; still {
		t.Error("expired question left in the queue")
	}
}

// The pause is bounded. RequestPermission blocks the agent for as long as
// the question lives, so "wait for a desktop" cannot mean "wait forever".
func TestExpiryHonoursTheHardCeiling(t *testing.T) {
	resetAsks()
	setSubs := withVaryingState(t, 1)
	id, answered := enqueueOne(t)

	asks[id].hardDeadline = time.Now().Add(-time.Second)
	setSubs(0)
	expireAsk(id)

	select {
	case got := <-answered:
		if got[0] != DecisionDefer {
			t.Errorf("decision = %q, want %q", got[0], DecisionDefer)
		}
	default:
		t.Fatal("past the hard ceiling the question must resolve even with nobody watching")
	}
	if _, still := asks[id]; still {
		t.Error("question survived its hard deadline")
	}
}

// Three producers of DecisionDefer, three meanings. They shared one code
// path and one silent outcome, which is how a timer came to look like a
// policy; keeping them distinguishable is what lets the transcript say
// which one happened.
func TestDeferReasonsStayDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, why := range []string{ReasonTimeout, ReasonNoDesktop, ReasonTooMany, reasonAskOff} {
		text := unansweredReason(why)
		if text == "" || text == why {
			t.Errorf("reason %q has no human explanation (%q)", why, text)
		}
		if prev, dup := seen[text]; dup {
			t.Errorf("reasons %q and %q both explain themselves as %q", prev, why, text)
		}
		seen[text] = why
	}
	// An unmapped reason must still say something rather than vanish.
	if got := unansweredReason("weather"); got != "weather" {
		t.Errorf("unmapped reason = %q, want it passed through verbatim", got)
	}
	if got := unansweredReason(""); got == "" {
		t.Error("empty reason produced an empty explanation — a refusal with no words is the bug")
	}
}

// withRuleCount stands in for the policy file's rule table.
func withRuleCount(t *testing.T, n int) {
	t.Helper()
	old := policyRuleCount
	policyRuleCount = func() int { return n }
	t.Cleanup(func() { policyRuleCount = old })
}

// A machine with no agents.json asks about everything, because
// agentpolicy matches nothing. That is the session where a 30-second
// budget per question is least survivable and the human has the least
// practice answering — so it gets a longer one, which tightens as soon as
// they have taught wash anything.
func TestFirstRunGetsALongerWindow(t *testing.T) {
	cases := []struct {
		name  string
		rules int
		want  time.Duration
	}{
		{name: "no rules yet", rules: 0, want: askFirstRunTTL},
		{name: "one rule taught", rules: 1, want: askTTL},
		{name: "well trained", rules: 20, want: askTTL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetAsks()
			withState(t, 1)
			withRuleCount(t, tc.rules)

			id, _ := enqueueOne(t)
			if got := asks[id].softTTL; got != tc.want {
				t.Errorf("softTTL = %s, want %s", got, tc.want)
			}
		})
	}
	if askFirstRunTTL <= askTTL {
		t.Error("the first-run window must be the longer one")
	}
	if askFirstRunTTL >= askHardTTL {
		t.Error("the first-run window must still sit under the hard ceiling")
	}
}
