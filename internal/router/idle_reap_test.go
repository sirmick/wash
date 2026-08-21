package router

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newTestContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// The split exists because one timer served two populations with opposite
// needs, and the leak case set the policy for both.
func TestIdlePolicyPicksThresholdByPopulation(t *testing.T) {
	pol := IdlePolicy{Unattached: 2 * time.Minute, Established: 0}

	got, pop := pol.threshold(0)
	if got != 2*time.Minute || pop != "never-attached" {
		t.Errorf("a router nobody reached got (%s, %s), want (2m0s, never-attached)", got, pop)
	}
	// One attachment, ever, is enough — the shell that has since gone is
	// the whole difference between an abandoned spawn and a shut laptop.
	got, pop = pol.threshold(1)
	if got != 0 || pop != "established" {
		t.Errorf("a router that was attached to got (%s, %s), want (0s, established)", got, pop)
	}
}

// ShellCount answers "is anyone here now"; ShellsSeen answers "has anyone
// ever been here". Treating the second question as the first is what let
// a closed lid destroy a night's work.
func TestShellsSeenSurvivesDisconnect(t *testing.T) {
	r := &Router{shells: map[*ShellSession]struct{}{}}
	if r.ShellsSeen() != 0 || r.ShellCount() != 0 {
		t.Fatal("a fresh router has seen nothing")
	}
	s := &ShellSession{}
	r.registerShell(s)
	if r.ShellsSeen() != 1 || r.ShellCount() != 1 {
		t.Fatalf("after attach: seen=%d count=%d, want 1/1", r.ShellsSeen(), r.ShellCount())
	}
	r.unregisterShell(s)
	if r.ShellCount() != 0 {
		t.Errorf("count = %d after disconnect, want 0", r.ShellCount())
	}
	if r.ShellsSeen() != 1 {
		t.Errorf("seen = %d after disconnect, want 1 — the lifetime figure must not decay", r.ShellsSeen())
	}
}

// An app doing work that outlives the browser can veto the reap, and a
// crashed one cannot pin the session forever.
func TestIdleInhibitLifecycle(t *testing.T) {
	r := &Router{shells: map[*ShellSession]struct{}{}, apps: map[string]*AppInstance{}, byWin: map[uint32]*AppInstance{}}
	if got := r.IdleInhibitors(); got != nil {
		t.Fatalf("fresh router already inhibited: %v", got)
	}

	r.SetIdleInhibit("i-agentd", true, "1 agent(s) working")
	held := r.IdleInhibitors()
	if len(held) != 1 || !strings.Contains(held[0], "1 agent(s) working") {
		t.Fatalf("inhibitors = %v, want one naming the reason", held)
	}

	// Level-triggered: re-sending the truth is not an error and does not
	// stack, so an app that lost track of itself can always re-assert.
	r.SetIdleInhibit("i-agentd", true, "2 agent(s) working")
	if held := r.IdleInhibitors(); len(held) != 1 {
		t.Errorf("re-send stacked into %v, want one entry", held)
	}

	// A hold with no stated reason is still a hold, but says so.
	r.SetIdleInhibit("i-other", true, "")
	if held := r.IdleInhibitors(); len(held) != 2 || !strings.Contains(strings.Join(held, " "), "unspecified") {
		t.Errorf("inhibitors = %v, want an unspecified second hold", held)
	}

	r.SetIdleInhibit("i-agentd", false, "")
	if held := r.IdleInhibitors(); len(held) != 1 {
		t.Errorf("after release: %v, want just the other hold", held)
	}

	// The veto must not outlive the thing it was vetoing for.
	r.unregisterApp(&AppInstance{InstanceID: "i-other"})
	if held := r.IdleInhibitors(); held != nil {
		t.Errorf("a gone instance still holds the session: %v", held)
	}
}

// Both thresholds zero means the reaper is off entirely, and must block
// on ctx rather than spinning or returning nil (which would exit).
func TestReapDisabledBlocksUntilCancel(t *testing.T) {
	r := &Router{shells: map[*ShellSession]struct{}{}}
	ctx, cancel := newTestContext()
	done := make(chan error, 1)
	go func() { done <- r.ReapWhenIdle(ctx, IdlePolicy{}) }()
	select {
	case err := <-done:
		t.Fatalf("returned %v with reaping disabled — the caller would exit the session", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("returned nil on cancel — nil means 'idle threshold hit, please exit'")
		}
	case <-time.After(time.Second):
		t.Error("did not return after cancel")
	}
}
