package agentd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
)

func boolPtr(b bool) *bool { return &b }

func withPolicy(t *testing.T, p agentpolicy.Policy) {
	t.Helper()
	old := hostedPolicy
	hostedPolicy = func() agentpolicy.Policy { return p }
	t.Cleanup(func() { hostedPolicy = old })
}

var stdOptions = []acp.PermissionOption{
	{OptionID: "once", Name: "Allow once", Kind: acp.OptionAllowOnce},
	{OptionID: "always", Name: "Always allow", Kind: acp.OptionAllowAlways},
	{OptionID: "no", Name: "Deny", Kind: acp.OptionRejectOnce},
}

// One rule language across both tiers: an existing Bash(...) rule must
// govern an ACP session, or a user's policy silently stops applying the
// day their session moves to the managed path.
func TestToolRequestSpeaksTheRuleLanguage(t *testing.T) {
	cases := []struct {
		name        string
		tc          acp.ToolCall
		wantTool    string
		wantSubject string
	}{
		{
			name:        "execute carries the command from rawInput",
			tc:          acp.ToolCall{Kind: acp.ToolKindExecute, Title: "Run git push", RawInput: json.RawMessage(`{"command":"git push origin main"}`)},
			wantTool:    "Bash",
			wantSubject: "git push origin main",
		},
		{
			name:        "title is the fallback when rawInput has nothing usable",
			tc:          acp.ToolCall{Kind: acp.ToolKindExecute, Title: "git status"},
			wantTool:    "Bash",
			wantSubject: "git status",
		},
		{
			name:        "read maps onto the file-path tools",
			tc:          acp.ToolCall{Kind: acp.ToolKindRead, RawInput: json.RawMessage(`{"file_path":"/etc/shadow"}`)},
			wantTool:    "Read",
			wantSubject: "/etc/shadow",
		},
		{
			name:     "an unmapped kind gets a name no rule can match",
			tc:       acp.ToolCall{Kind: "teleport", Title: "do something new"},
			wantTool: "Acp:teleport",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toolRequest(c.tc, "/w")
			if got.ToolName != c.wantTool {
				t.Errorf("tool = %q, want %q", got.ToolName, c.wantTool)
			}
			if c.wantSubject != "" {
				if s := agentpolicy.ToolSubject(got.ToolName, got.ToolInput); s != c.wantSubject {
					t.Errorf("subject = %q, want %q", s, c.wantSubject)
				}
			}
		})
	}
}

// A kind wash has never heard of must fall through to asking. If it landed
// on a tool name an allow rule matched, a new ACP kind would silently
// widen the user's policy.
func TestUnmappedKindCannotBeAllowedByAnExistingRule(t *testing.T) {
	p := agentpolicy.Policy{Enabled: true, Default: agentpolicy.DecisionAsk, Rules: []agentpolicy.Rule{
		{Match: "Bash", Decision: agentpolicy.DecisionAllow},
		{Match: "Read", Decision: agentpolicy.DecisionAllow},
	}}
	got := agentpolicy.Evaluate(p, toolRequest(acp.ToolCall{Kind: "teleport", Title: "rm -rf /"}, "/w"))
	if got.Decision != agentpolicy.DecisionAsk {
		t.Errorf("unmapped kind → %+v, want ask", got)
	}
}

// wash records consent in its own agents.json, so it must never also pick
// the agent's durable option — that would put the same consent in two
// places that can disagree, only one of which the Agents pane shows.
func TestPickPrefersTheOneShotOption(t *testing.T) {
	got := pick(stdOptions, acp.OptionAllowOnce, acp.OptionAllowAlways)
	if got.Outcome.OptionID != "once" {
		t.Errorf("picked %q, want the one-shot option", got.Outcome.OptionID)
	}

	// Only a durable option offered: take it rather than stalling.
	only := []acp.PermissionOption{{OptionID: "always", Kind: acp.OptionAllowAlways}}
	if got := pick(only, acp.OptionAllowOnce, acp.OptionAllowAlways); got.Outcome.OptionID != "always" {
		t.Errorf("picked %+v with only a durable option offered", got.Outcome)
	}

	// Nothing we can express: say so rather than guessing.
	none := []acp.PermissionOption{{OptionID: "weird", Kind: "some_future_kind"}}
	if got := pick(none, acp.OptionAllowOnce, acp.OptionAllowAlways); got.Outcome.Outcome != acp.OutcomeCancelled {
		t.Errorf("picked %+v from options we cannot express, want cancelled", got.Outcome)
	}
}

// The floor: every path that is not an explicit allow hands the decision
// back to the agent. This is the invariant the terminal tier established
// and the one the pivot must not lose.
func TestRequestPermissionNeverInventsAnAllow(t *testing.T) {
	cases := []struct {
		name   string
		policy agentpolicy.Policy
		subs   int
		want   string // option id, or "" for cancelled
	}{
		{
			// Nobody attached: the only honest answer. The agent hears
			// cancelled rather than blocking on a desktop that is not there.
			name:   "no policy, nobody watching",
			policy: agentpolicy.Policy{},
			subs:   0,
		},
		{
			name:   "policy on, no matching rule, nobody watching",
			policy: agentpolicy.Policy{Enabled: true, Default: agentpolicy.DecisionAsk},
			subs:   0,
		},
		{
			// An explicit opt-out still means what it says.
			name:   "policy on with ask_desktop off",
			policy: agentpolicy.Policy{Enabled: true, Default: agentpolicy.DecisionAsk, AskDesktop: boolPtr(false)},
			subs:   1,
		},
		{
			name: "a deny rule",
			policy: agentpolicy.Policy{Enabled: true, Rules: []agentpolicy.Rule{
				{Match: "Bash(git push*)", Decision: agentpolicy.DecisionDeny},
			}},
			subs: 1,
			want: "no",
		},
		{
			name: "an allow rule",
			policy: agentpolicy.Policy{Enabled: true, Rules: []agentpolicy.Rule{
				{Match: "Bash(git push*)", Decision: agentpolicy.DecisionAllow},
			}},
			subs: 1,
			want: "once",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetAsks()
			withState(t, c.subs)
			withPolicy(t, c.policy)

			h := &hosted{key: "acp:1", agent: "codex"}
			res, err := h.RequestPermission(context.Background(), acp.RequestPermissionRequest{
				SessionID: "s1",
				ToolCall:  acp.ToolCall{Kind: acp.ToolKindExecute, RawInput: json.RawMessage(`{"command":"git push origin main"}`)},
				Options:   stdOptions,
			})
			if err != nil {
				t.Fatalf("RequestPermission: %v", err)
			}
			if c.want == "" {
				if res.Outcome.Outcome != acp.OutcomeCancelled {
					t.Fatalf("outcome = %+v, want cancelled", res.Outcome)
				}
				return
			}
			if res.Outcome.Outcome != acp.OutcomeSelected || res.Outcome.OptionID != c.want {
				t.Fatalf("outcome = %+v, want selected %q", res.Outcome, c.want)
			}
		})
	}
}

// The M3 acceptance criterion, minus the browser: an unmatched request
// becomes a question in the SAME queue a terminal's request lands in, and
// the human's answer reaches the agent as an ACP outcome.
func TestUnmatchedRequestReachesTheSharedQueue(t *testing.T) {
	resetAsks()
	withState(t, 1)
	withPolicy(t, agentpolicy.Policy{Enabled: true, Default: agentpolicy.DecisionAsk})

	h := &hosted{key: "acp:1", agent: "codex"}
	done := make(chan acp.RequestPermissionResponse, 1)
	go func() {
		res, _ := h.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			SessionID: "s1",
			ToolCall:  acp.ToolCall{Kind: acp.ToolKindExecute, RawInput: json.RawMessage(`{"command":"git push origin main"}`)},
			Options:   stdOptions,
		})
		done <- res
	}()

	// It should show up as a pending ask, keyed to this session's row.
	// Read the queue the way production does — inside the state lock.
	var p *pending
	deadline := time.Now().Add(2 * time.Second)
	for p == nil && time.Now().Before(deadline) {
		mutateState(func(*State) {
			for _, q := range asks {
				if q.RowKey == "acp:1" {
					p = q
				}
			}
		})
		if p == nil {
			time.Sleep(2 * time.Millisecond)
		}
	}
	if p == nil {
		t.Fatal("an ACP permission request never reached the shared ask queue")
	}
	if p.Subject != "git push origin main" {
		t.Errorf("queued subject = %q — the sidebar would show the wrong command", p.Subject)
	}

	// The human clicks Allow: the queue calls the producer's reply route,
	// which is this session's channel rather than a wire address.
	mutateState(func(*State) { delete(asks, p.ID) })
	if p.timer != nil {
		p.timer.Stop()
	}
	_ = p.reply(DecisionAllow, "desktop")

	select {
	case res := <-done:
		if res.Outcome.Outcome != acp.OutcomeSelected || res.Outcome.OptionID != "once" {
			t.Fatalf("agent received %+v, want selected once", res.Outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the answer never reached the agent — its turn would hang")
	}
}

// The launcher is a text field, and "~/wash" is what people type. Go's
// filepath does not expand it, so the first real run resolved it against
// the ROUTER's working directory and failed with a path nobody
// recognised: /home/mick/wash/branches/agent-app/~/wash.
func TestResolveCwdExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory in this environment")
	}

	if got, err := resolveCwd("~"); err != nil || got != home {
		t.Errorf("resolveCwd(\"~\") = %q, %v — want %q", got, err, home)
	}
	if got, err := resolveCwd(""); err != nil || got != home {
		t.Errorf("resolveCwd(\"\") = %q, %v — want %q", got, err, home)
	}

	// A tilde path that exists resolves under home, not under the cwd.
	dir := t.TempDir()
	if got, err := resolveCwd(dir); err != nil || got != dir {
		t.Errorf("resolveCwd(abs) = %q, %v", got, err)
	}

	// A path that does not exist must name what it RESOLVED to — the
	// original bug was unreadable precisely because that was hidden.
	_, err = resolveCwd("~/definitely-not-a-real-directory-xyzzy")
	if err == nil {
		t.Fatal("a missing folder was accepted")
	}
	if !strings.Contains(err.Error(), home) {
		t.Errorf("error does not name the resolved path: %v", err)
	}

	// A file is not a folder.
	f := filepath.Join(dir, "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCwd(f); err == nil {
		t.Error("a regular file was accepted as a working directory")
	}
}

// Asking is the FLOOR for a hosted session, not an opt-in.
//
// The terminal tier could decline to answer because deferring returned
// control to an agent with its own prompt in a pty. A hosted session has
// no such UI, so declining means the tool silently never runs. Observed
// on the first real Codex session: decision=defer reason="policy off"
// followed immediately by state=done, with nothing on screen to explain
// it.
func TestHostedSessionAsksEvenWithNoPolicyFile(t *testing.T) {
	resetAsks()
	withState(t, 1)
	withPolicy(t, agentpolicy.Policy{}) // exactly what an absent agents.json decodes to

	h := &hosted{key: "acp:1", agent: "codex"}
	go h.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		SessionID: "s1",
		ToolCall:  acp.ToolCall{Kind: acp.ToolKindExecute, RawInput: json.RawMessage(`{"command":"rm -rf /"}`)},
		Options:   stdOptions,
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var found bool
		mutateState(func(*State) {
			for _, q := range asks {
				if q.RowKey == "acp:1" {
					found = true
				}
			}
		})
		if found {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("with no policy file a hosted session did not ask — its tool call would silently never run")
}
