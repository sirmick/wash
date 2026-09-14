package agentd

import (
	"sync"
	"testing"
	"time"
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

	// A structural row change still publishes.
	h.detached = true
	h.republish()
	if *pushes == before {
		t.Error("a structural row change published nothing")
	}
}

// Usage is a hot telemetry path. Ten thousand notifications must update the
// canonical state but collapse to one small latest-value patch and zero full
// roster pushes.
func TestUsageCoalescesLatestWithoutRosterPush(t *testing.T) {
	oldRows := rows
	rows = map[string]*row{"acp:1": {Row: Row{Key: "acp:1"}}}
	var st State
	fullPushes := 0
	oldMutate := mutateStateIf
	mutateStateIf = func(fn func(*State) bool) {
		if fn(&st) {
			fullPushes++
		}
	}

	stopUsagePatches()
	oldDelay, oldPublish := usageDelay, usagePublish
	usageDelay = time.Hour
	var patches []usagePatch
	usagePublish = func(p usagePatch) { patches = append(patches, p) }
	t.Cleanup(func() {
		stopUsagePatches()
		rows = oldRows
		usageDelay, usagePublish = oldDelay, oldPublish
		mutateStateIf = oldMutate
	})

	h := &hosted{key: "acp:1"}
	for i := int64(1); i <= 10_000; i++ {
		h.setUsage(i, 200_000)
	}
	if fullPushes != 0 {
		t.Fatalf("usage caused %d full roster pushes, want 0", fullPushes)
	}
	if len(st.Rows) != 1 || st.Rows[0].Used != 10_000 || st.Rows[0].Size != 200_000 {
		t.Fatalf("canonical row=%+v, want latest usage", st.Rows)
	}

	flushUsagePatches()
	if len(patches) != 1 || len(patches[0].Rows) != 1 {
		t.Fatalf("patches=%+v, want one patch with one row", patches)
	}
	got := patches[0].Rows[0]
	if got.Key != "acp:1" || got.Used != 10_000 || got.Size != 200_000 {
		t.Fatalf("patch row=%+v, want latest usage", got)
	}
}

// A slow downstream subscriber must not turn each 500ms tick into another
// blocked publisher. One send may be in flight; everything behind it folds
// into the single next patch.
func TestUsagePublisherIsSingleFlightAndLatestWins(t *testing.T) {
	stopUsagePatches()
	oldDelay, oldPublish := usageDelay, usagePublish
	usageDelay = 5 * time.Millisecond
	entered := make(chan usagePatch, 2)
	release := make(chan struct{})
	done := make(chan struct{}, 2)
	usagePublish = func(p usagePatch) {
		entered <- p
		<-release
		done <- struct{}{}
	}
	t.Cleanup(func() {
		stopUsagePatches()
		usageDelay, usagePublish = oldDelay, oldPublish
	})

	queueUsagePatch(usagePatchRow{Key: "acp:1", Used: 1, Size: 100})
	first := <-entered
	if first.Rows[0].Used != 1 {
		t.Fatalf("first patch=%+v, want used=1", first)
	}
	for i := int64(2); i <= 100; i++ {
		queueUsagePatch(usagePatchRow{Key: "acp:1", Used: i, Size: 100})
	}

	select {
	case p := <-entered:
		t.Fatalf("concurrent publisher escaped single-flight guard: %+v", p)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)

	select {
	case second := <-entered:
		if len(second.Rows) != 1 || second.Rows[0].Used != 100 {
			t.Fatalf("second patch=%+v, want latest used=100", second)
		}
	case <-time.After(time.Second):
		t.Fatal("coalesced patch did not follow completed send")
	}
	<-done
	<-done
	deadline := time.Now().Add(time.Second)
	for {
		usageMu.Lock()
		idle := !usageSending
		usageMu.Unlock()
		if idle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("usage publisher did not return to idle")
		}
		time.Sleep(time.Millisecond)
	}
}
