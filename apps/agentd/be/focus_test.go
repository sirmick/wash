package agentd

import (
	"testing"
	"time"
)

// The desktop toast for a question (docs/AGENT_UX.md N2). The rule the
// tests hold to: a toast is raised exactly when a question actually
// reaches a human, once, and never for the paths that answered without
// putting anything on screen.

// withNotify records the toasts the ask queue raises.
func withNotify(t *testing.T) *[]Ask {
	t.Helper()
	old := notifyAsk
	var got []Ask
	notifyAsk = func(a Ask) { got = append(got, a) }
	t.Cleanup(func() { notifyAsk = old })
	return &got
}

func TestAskRaisesOneToast(t *testing.T) {
	resetAsks()
	withState(t, 1)
	toasts := withNotify(t)

	id, _ := enqueueOne(t)
	if len(*toasts) != 1 {
		t.Fatalf("toasts = %d, want 1", len(*toasts))
	}
	if (*toasts)[0].ID != id {
		t.Errorf("toast named ask %q, want %q", (*toasts)[0].ID, id)
	}
	if (*toasts)[0].RowKey != "row-1" {
		t.Errorf("toast lost the row key: %q", (*toasts)[0].RowKey)
	}
}

func TestNoToastWhenNobodyCouldSeeIt(t *testing.T) {
	// No desktop attached: the question is deferred immediately and never
	// shown, so a toast would be announcing something that did not happen.
	resetAsks()
	withState(t, 0)
	toasts := withNotify(t)

	enqueueAsk(askSpec{Agent: "codex", Tool: "Bash", RowKey: "row-1"},
		func(string, string) error { return nil })
	if len(*toasts) != 0 {
		t.Fatalf("toasts = %d with no desktop attached, want 0", len(*toasts))
	}
}

func TestNoToastForARowAtItsCap(t *testing.T) {
	resetAsks()
	withState(t, 1)
	for i := 0; i < maxPendingPerRow; i++ {
		addAsk("ask-"+itoa(uint64(i+1)), "row-1", "Bash", time.Now())
	}
	toasts := withNotify(t)

	enqueueAsk(askSpec{Agent: "codex", Tool: "Bash", RowKey: "row-1"},
		func(string, string) error { return nil })
	if len(*toasts) != 0 {
		t.Fatalf("toasts = %d for a rejected question, want 0", len(*toasts))
	}
}

func TestReArmingDoesNotReToast(t *testing.T) {
	// expireAsk re-arms an unanswered question for as long as nobody could
	// have answered it. A toast per extension would be the machine nagging
	// about its own patience.
	resetAsks()
	setSubs := withVaryingState(t, 1)
	toasts := withNotify(t)
	id, _ := enqueueOne(t)
	if len(*toasts) != 1 {
		t.Fatalf("toasts after enqueue = %d, want 1", len(*toasts))
	}

	setSubs(0)
	expireAsk(id)
	expireAsk(id)
	if len(*toasts) != 1 {
		t.Fatalf("toasts after two re-arms = %d, want 1", len(*toasts))
	}
}

// --- what the toast says, and whether its click can be honoured ---

func TestAskToastBody(t *testing.T) {
	cases := []struct {
		name string
		ask  Ask
		want string
	}{
		{"tool and subject", Ask{Tool: "Bash", Subject: "git push origin main"}, "Bash git push origin main"},
		{"tool alone", Ask{Tool: "WebFetch"}, "WebFetch"},
	}
	for _, c := range cases {
		if got := askToastBody(c.ask); got != c.want {
			t.Errorf("%s: body = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAskKeyOnlyForSessionsThisServiceCanOpen(t *testing.T) {
	// A key is a promise that activating the toast lands somewhere. This
	// service can open or raise a window for a hosted session; a
	// terminal-tier row belongs to wash-term, which has no handler yet, so
	// keying it would buy a dead click instead of the generic fallback.
	hostedMu.Lock()
	hostedAll["acp:1"] = &hosted{key: "acp:1"}
	hostedMu.Unlock()
	t.Cleanup(func() {
		hostedMu.Lock()
		delete(hostedAll, "acp:1")
		hostedMu.Unlock()
	})

	if got := askKey(Ask{RowKey: "acp:1"}); got != "acp:1" {
		t.Errorf("hosted ask key = %q, want %q", got, "acp:1")
	}
	if got := askKey(Ask{RowKey: "i-7:3"}); got != "" {
		t.Errorf("terminal-tier ask key = %q, want empty", got)
	}
	if got := askKey(Ask{RowKey: "acp:404"}); got != "" {
		t.Errorf("key for a session that is gone = %q, want empty", got)
	}
}
