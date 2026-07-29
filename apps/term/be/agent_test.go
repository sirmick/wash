package term

import (
	"testing"
	"time"

	"github.com/sirmick/wash/internal/pty"
)

var t0 = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

func poll(agent string) pty.ForegroundUser {
	return pty.ForegroundUser{State: "user", User: "mick", Agent: agent}
}

// The tier the acceptance test turns on: a plain program in the
// foreground is not an agent, and a tab with nothing to say sends
// nothing.
func TestAgentRecNoAgent(t *testing.T) {
	var r agentRec
	r.applyPoll(poll(""), t0)
	if v, ok := r.view(t0); ok {
		t.Errorf("view = %+v, want no agent", v)
	}
	if key := agentKey(agentView{}, false); key != "" {
		t.Errorf("dedupe key for 'no agent' = %q, want empty", key)
	}
}

// T0 alone: we know an agent is there, not what it's doing.
func TestAgentRecPollOnly(t *testing.T) {
	var r agentRec
	r.applyPoll(poll("claude"), t0)
	v, ok := r.view(t0.Add(5 * time.Second))
	if !ok {
		t.Fatal("no view")
	}
	if v.Agent != "claude" || v.State != agentStateRunning {
		t.Errorf("view = %+v, want claude/running", v)
	}
	if !v.Since.Equal(t0) {
		t.Errorf("Since = %v, want first sighting %v", v.Since, t0)
	}
	// A second identical sample must not restart the clock.
	r.applyPoll(poll("claude"), t0.Add(9*time.Second))
	if v, _ := r.view(t0.Add(9 * time.Second)); !v.Since.Equal(t0) {
		t.Errorf("Since moved to %v on an unchanged poll", v.Since)
	}
	// The agent exits.
	r.applyPoll(poll(""), t0.Add(20*time.Second))
	if _, ok := r.view(t0.Add(20 * time.Second)); ok {
		t.Error("agent still reported after it left the foreground")
	}
}

// T1 refines T0: the hook knows the state, the poll only knows presence.
func TestAgentRecOSCWinsOverPoll(t *testing.T) {
	var r agentRec
	r.applyPoll(poll("claude"), t0)
	if !r.applyOSC(pty.AgentEvent{Event: pty.AgentEvWorking, Agent: "claude", Session: "s-1"}, t0.Add(time.Second)) {
		t.Fatal("working event reported no change")
	}
	v, ok := r.view(t0.Add(2 * time.Second))
	if !ok {
		t.Fatal("no view")
	}
	if v.State != agentStateWorking || v.Session != "s-1" {
		t.Errorf("view = %+v, want working/s-1", v)
	}
	if !v.Since.Equal(t0.Add(time.Second)) {
		t.Errorf("Since = %v, want the state transition time", v.Since)
	}
}

func TestAgentRecEventSequence(t *testing.T) {
	var r agentRec
	r.applyPoll(poll("claude"), t0)
	steps := []struct {
		ev     string
		want   string
		reason string
	}{
		{pty.AgentEvStart, agentStateRunning, ""},
		{pty.AgentEvWorking, agentStateWorking, ""},
		{pty.AgentEvNeedsInput, agentStateNeedsInput, "permission"},
		{pty.AgentEvWorking, agentStateWorking, ""},
		{pty.AgentEvDone, agentStateDone, ""},
	}
	now := t0
	for i, s := range steps {
		now = now.Add(time.Second)
		if !r.applyOSC(pty.AgentEvent{Event: s.ev, Agent: "claude", Reason: s.reason}, now) {
			t.Fatalf("step %d (%s) reported no change", i, s.ev)
		}
		v, ok := r.view(now)
		if !ok || v.State != s.want || v.Reason != s.reason {
			t.Fatalf("step %d (%s): view = %+v, want %s/%q", i, s.ev, v, s.want, s.reason)
		}
		if !v.Since.Equal(now) {
			t.Errorf("step %d: Since = %v, want %v", i, v.Since, now)
		}
	}
	// ev=end hands the tab back to T0 — the process is still in the
	// foreground until it actually exits.
	if !r.applyOSC(pty.AgentEvent{Event: pty.AgentEvEnd}, now.Add(time.Second)) {
		t.Fatal("end reported no change")
	}
	v, ok := r.view(now.Add(time.Second))
	if !ok || v.State != agentStateRunning {
		t.Errorf("after end: view = %+v, want the T0 running state", v)
	}
	if v.Session != "" {
		t.Errorf("after end: session %q survived", v.Session)
	}
}

// A repeated state (a second permission prompt in one turn) must not
// restart the elapsed clock or put a message on the wire.
func TestAgentRecRepeatedStateIsNotAChange(t *testing.T) {
	var r agentRec
	r.applyOSC(pty.AgentEvent{Event: pty.AgentEvNeedsInput, Agent: "claude", Reason: "permission"}, t0)
	if r.applyOSC(pty.AgentEvent{Event: pty.AgentEvNeedsInput, Agent: "claude", Reason: "permission"}, t0.Add(time.Minute)) {
		t.Error("identical event reported a change")
	}
	if v, _ := r.view(t0.Add(time.Minute)); !v.Since.Equal(t0) {
		t.Errorf("Since = %v, want the original %v", v.Since, t0)
	}
	// A different reason IS a change.
	if !r.applyOSC(pty.AgentEvent{Event: pty.AgentEvNeedsInput, Agent: "claude", Reason: "idle"}, t0.Add(2*time.Minute)) {
		t.Error("changed reason reported no change")
	}
}

// An agent killed outright never fires SessionEnd. T0 is the liveness
// signal that eventually clears the dot.
func TestAgentRecOSCExpiresWhenTheAgentIsGone(t *testing.T) {
	var r agentRec
	r.applyPoll(poll("claude"), t0)
	r.applyOSC(pty.AgentEvent{Event: pty.AgentEvWorking, Agent: "claude"}, t0)
	// SIGKILL: the foreground goes back to the shell.
	r.applyPoll(poll(""), t0.Add(time.Second))
	if _, ok := r.view(t0.Add(time.Second)); !ok {
		t.Error("state cleared immediately; the grace window should still hold it")
	}
	if _, ok := r.view(t0.Add(agentOSCStale + 2*time.Second)); ok {
		t.Error("stale OSC state never expired — the tab would wear a frozen dot")
	}
}

// The exception that keeps remote agents working: over ssh, T0 only ever
// sees the ssh client, so its silence must not expire the hooks' state.
func TestAgentRecSSHNeverExpires(t *testing.T) {
	var r agentRec
	r.applyPoll(pty.ForegroundUser{State: "ssh", Target: "harbor"}, t0)
	r.applyOSC(pty.AgentEvent{Event: pty.AgentEvWorking, Agent: "claude"}, t0)
	v, ok := r.view(t0.Add(10 * agentOSCStale))
	if !ok || v.State != agentStateWorking {
		t.Errorf("view = %+v (ok=%v), want a still-working remote agent", v, ok)
	}
}

// A hook that reports no slug still means an agent is there; T0's name
// fills in when it has one.
func TestAgentRecAgentNameFallback(t *testing.T) {
	var r agentRec
	r.applyOSC(pty.AgentEvent{Event: pty.AgentEvWorking}, t0)
	if v, _ := r.view(t0); v.Agent != "agent" {
		t.Errorf("agent = %q, want the generic fallback", v.Agent)
	}
	r.applyPoll(poll("codex"), t0)
	if v, _ := r.view(t0); v.Agent != "codex" {
		t.Errorf("agent = %q, want T0's name", v.Agent)
	}
}

// An unknown event can't reach here (the parser drops it), but the record
// must ignore it rather than blanking the state if one ever does.
func TestAgentRecUnknownEventIgnored(t *testing.T) {
	var r agentRec
	r.applyOSC(pty.AgentEvent{Event: pty.AgentEvWorking, Agent: "claude"}, t0)
	if r.applyOSC(pty.AgentEvent{Event: "teleporting"}, t0.Add(time.Second)) {
		t.Error("unknown event reported a change")
	}
	if v, _ := r.view(t0.Add(time.Second)); v.State != agentStateWorking {
		t.Errorf("state = %q, want working", v.State)
	}
}

// The dedupe key is what keeps an idle tab off the wire: it must move on
// a real change and hold still while only time passes.
func TestAgentKeyDedupe(t *testing.T) {
	var r agentRec
	r.applyPoll(poll("claude"), t0)
	k1 := agentKey(r.view(t0))
	k2 := agentKey(r.view(t0.Add(30 * time.Second)))
	if k1 != k2 {
		t.Errorf("key changed with elapsed time alone: %q → %q", k1, k2)
	}
	r.applyOSC(pty.AgentEvent{Event: pty.AgentEvWorking, Agent: "claude"}, t0)
	if k3 := agentKey(r.view(t0)); k3 == k1 {
		t.Errorf("key did not change on a state change: %q", k3)
	}
}

func TestElapsedMS(t *testing.T) {
	if got := elapsedMS(time.Time{}, t0); got != 0 {
		t.Errorf("zero Since → %d, want 0", got)
	}
	if got := elapsedMS(t0, t0.Add(1500*time.Millisecond)); got != 1500 {
		t.Errorf("got %d, want 1500", got)
	}
	if got := elapsedMS(t0.Add(time.Second), t0); got != 0 {
		t.Errorf("clock went backwards → %d, want 0", got)
	}
}
