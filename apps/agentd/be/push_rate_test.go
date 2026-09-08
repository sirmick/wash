package agentd

import (
	"sync"
	"testing"
)

// countingState installs a mutateState that records how many times the
// state was published. Every mutateState call is one StateService.Mutate,
// and Mutate ALWAYS writes the whole snapshot to every subscriber — so
// this count is the number of full-state pushes that reach the browser.
func countingState(t *testing.T) *int {
	t.Helper()
	oldSubs, oldMutate := stateSubscribers, mutateStateIf
	var st State
	var mu sync.Mutex
	n := 0
	stateSubscribers = func() int { return 1 }
	// Counts PUSHES, not calls: a mutation that reports nothing changed
	// is exactly the one that must not reach a subscriber.
	mutateStateIf = func(fn func(*State) bool) {
		mu.Lock()
		defer mu.Unlock()
		if fn(&st) {
			n++
		}
	}
	t.Cleanup(func() { stateSubscribers, mutateStateIf = oldSubs, oldMutate })
	return &n
}

// A streaming turn narrates on every message chunk, thought chunk, tool
// call and plan update. None of those change the roster row after the
// first — the session is already "working" — so they must not each cost a
// full roster + history snapshot on the wire. This is the Interactive
// flood seen under agent use: one push per chunk, times every subscriber.
func TestNarrationDoesNotRepublishPerChunk(t *testing.T) {
	rows = map[string]*row{}
	pushes := countingState(t)
	h := &hosted{key: "acp:1", agent: "claude", sessionID: "s1"}

	h.beginTurn()
	first := *pushes
	if first == 0 {
		t.Fatal("opening a turn published nothing")
	}
	for i := 0; i < 200; i++ {
		h.narrated()
	}
	if extra := *pushes - first; extra != 0 {
		t.Errorf("200 chunks of narration cost %d extra full-state pushes, want 0", extra)
	}

	// A real transition must still publish — this is a dedupe, not a mute.
	h.endTurn("done", "")
	if *pushes <= first {
		t.Errorf("the turn ending published nothing (pushes=%d, was %d)", *pushes, first)
	}
}

// The dedupe keys on what subscribers actually receive, so a row field
// that changed must publish even when the state string has not.
func TestARowChangeStillPublishes(t *testing.T) {
	rows = map[string]*row{}
	pushes := countingState(t)
	h := &hosted{key: "acp:1", agent: "claude", sessionID: "s1"}
	h.beginTurn()
	before := *pushes

	// Token usage arriving mid-turn is a visible change (the roster shows
	// context used), so it publishes.
	h.used, h.size = 1200, 200000
	h.republish()
	if *pushes == before {
		t.Error("a usage update published nothing")
	}
	// Republishing the same values again must not.
	after := *pushes
	h.republish()
	if *pushes != after {
		t.Errorf("an unchanged republish cost %d pushes, want 0", *pushes-after)
	}
}
