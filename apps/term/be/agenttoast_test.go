package term

import (
	"testing"
	"time"

	"github.com/sirmick/wash/internal/pty"
)

// drive applies one OSC event and returns the toast it would produce (if
// any) — the same sequence onAgentEvent runs.
func drive(r *agentRec, ev string, reason string, now time.Time) (agentToast, bool) {
	if !r.applyOSC(pty.AgentEvent{Event: ev, Agent: "claude", Reason: reason, Cwd: "/home/mick/wash"}, now) {
		return agentToast{}, false
	}
	v, ok := r.view(now)
	if !ok {
		return agentToast{}, false
	}
	return r.toastFor(v, now)
}

// The two moments worth interrupting a human for.
func TestToastForTransitions(t *testing.T) {
	var r agentRec
	now := t0

	// A turn starts: nothing to say, the dot already said it.
	if _, ok := drive(&r, pty.AgentEvWorking, "", now); ok {
		t.Error("working produced a toast")
	}

	// It wants permission.
	now = now.Add(20 * time.Second)
	toast, ok := drive(&r, pty.AgentEvNeedsInput, "permission", now)
	if !ok {
		t.Fatal("needs-input produced no toast")
	}
	if toast.Level != "warn" {
		t.Errorf("level = %q, want warn", toast.Level)
	}
	if toast.Title != "Claude needs your input" {
		t.Errorf("title = %q", toast.Title)
	}
	if toast.Body != "wash · permission request" {
		t.Errorf("body = %q", toast.Body)
	}

	// Answered; back to work, then done after 43s.
	now = now.Add(10 * time.Second)
	drive(&r, pty.AgentEvWorking, "", now)
	now = now.Add(43 * time.Second)
	toast, ok = drive(&r, pty.AgentEvDone, "", now)
	if !ok {
		t.Fatal("done produced no toast")
	}
	if toast.Level != "info" {
		t.Errorf("level = %q, want info", toast.Level)
	}
	if toast.Title != "Claude finished after 43s" {
		t.Errorf("title = %q", toast.Title)
	}
	if toast.Body != "wash" {
		t.Errorf("body = %q", toast.Body)
	}
}

// done that didn't follow a turn is a session tidying up, not a result
// anyone was waiting for.
func TestToastForDoneWithoutWorkingIsSilent(t *testing.T) {
	var r agentRec
	drive(&r, pty.AgentEvStart, "", t0)
	if toast, ok := drive(&r, pty.AgentEvDone, "", t0.Add(time.Second)); ok {
		t.Errorf("start→done toasted %+v, want silence", toast)
	}
}

// The rate limit, stated as the three cases that matter.
func TestToastRateLimit(t *testing.T) {
	t.Run("second toast inside the gap is dropped", func(t *testing.T) {
		var r agentRec
		drive(&r, pty.AgentEvWorking, "", t0)
		if _, ok := drive(&r, pty.AgentEvNeedsInput, "permission", t0.Add(time.Second)); !ok {
			t.Fatal("first toast dropped")
		}
		if _, ok := drive(&r, pty.AgentEvNeedsInput, "idle", t0.Add(2*time.Second)); ok {
			t.Error("second warn inside the gap was not dropped")
		}
	})

	t.Run("after the gap it toasts again", func(t *testing.T) {
		var r agentRec
		drive(&r, pty.AgentEvWorking, "", t0)
		if _, ok := drive(&r, pty.AgentEvNeedsInput, "permission", t0.Add(time.Second)); !ok {
			t.Fatal("first toast dropped")
		}
		later := t0.Add(time.Second).Add(agentToastGap)
		if _, ok := drive(&r, pty.AgentEvNeedsInput, "idle", later); !ok {
			t.Error("toast after the gap was dropped")
		}
	})

	t.Run("needs-input beats a fresh done", func(t *testing.T) {
		// The case the rule exists for: a turn finishes, the next one
		// immediately asks for permission. The human is now blocked, and
		// "it finished" must not swallow "it needs you".
		var r agentRec
		drive(&r, pty.AgentEvWorking, "", t0)
		if _, ok := drive(&r, pty.AgentEvDone, "", t0.Add(time.Second)); !ok {
			t.Fatal("done toast dropped")
		}
		drive(&r, pty.AgentEvWorking, "", t0.Add(2*time.Second))
		toast, ok := drive(&r, pty.AgentEvNeedsInput, "permission", t0.Add(3*time.Second))
		if !ok {
			t.Fatal("needs-input inside the gap was dropped; it must win over done")
		}
		if toast.Level != "warn" {
			t.Errorf("level = %q", toast.Level)
		}
		// …but it does not then license a THIRD toast inside the gap.
		drive(&r, pty.AgentEvWorking, "", t0.Add(4*time.Second))
		if _, ok := drive(&r, pty.AgentEvDone, "", t0.Add(5*time.Second)); ok {
			t.Error("done inside the gap after a warn was not dropped")
		}
	})
}

// Toasts are per tab: one noisy agent must not mute another tab's.
func TestToastRateLimitIsPerTab(t *testing.T) {
	var a, b agentRec
	drive(&a, pty.AgentEvWorking, "", t0)
	drive(&b, pty.AgentEvWorking, "", t0)
	if _, ok := drive(&a, pty.AgentEvNeedsInput, "permission", t0.Add(time.Second)); !ok {
		t.Fatal("tab A toast dropped")
	}
	if _, ok := drive(&b, pty.AgentEvNeedsInput, "permission", t0.Add(time.Second)); !ok {
		t.Error("tab B toast dropped because tab A had just toasted")
	}
}

func TestFmtDur(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{950 * time.Millisecond, "1s"},
		{43 * time.Second, "43s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m"},
		{90 * time.Second, "1m 30s"},
		{10 * time.Minute, "10m"},
		{time.Hour, "1h"},
		{90 * time.Minute, "1h 30m"},
		{-5 * time.Second, "0s"},
	}
	for _, c := range cases {
		if got := fmtDur(c.d); got != c.want {
			t.Errorf("fmtDur(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestToastBody(t *testing.T) {
	cases := []struct {
		cwd, why, want string
	}{
		{"/home/mick/wash", "permission request", "wash · permission request"},
		{"/home/mick/wash/", "", "wash"},
		{"", "waiting for you", "waiting for you"},
		{"", "", ""},
		{"/", "permission request", "permission request"},
	}
	for _, c := range cases {
		if got := toastBody(c.cwd, c.why); got != c.want {
			t.Errorf("toastBody(%q, %q) = %q, want %q", c.cwd, c.why, got, c.want)
		}
	}
}

func TestNeedsInputWhy(t *testing.T) {
	cases := map[string]string{
		"permission": "permission request",
		"idle":       "waiting for you",
		"":           "",
		"something":  "something",
	}
	for in, want := range cases {
		if got := needsInputWhy(in); got != want {
			t.Errorf("needsInputWhy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentDisplayName(t *testing.T) {
	cases := map[string]string{"claude": "Claude", "codex": "Codex", "": "Agent", "amp": "Amp"}
	for in, want := range cases {
		if got := agentDisplayName(in); got != want {
			t.Errorf("agentDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A hook that never reports a slug still produces a sane sentence.
func TestToastWithNoAgentName(t *testing.T) {
	var r agentRec
	r.applyOSC(pty.AgentEvent{Event: pty.AgentEvWorking}, t0)
	r.applyOSC(pty.AgentEvent{Event: pty.AgentEvNeedsInput, Reason: "idle"}, t0.Add(time.Second))
	v, _ := r.view(t0.Add(time.Second))
	toast, ok := r.toastFor(v, t0.Add(time.Second))
	if !ok {
		t.Fatal("no toast")
	}
	if toast.Title != "Agent needs your input" {
		t.Errorf("title = %q", toast.Title)
	}
}
