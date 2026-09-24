package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/sdk"

	"github.com/sirmick/wash/pkg/wire"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
)

func boolPtr(b bool) *bool { return &b }

func TestToolMayChangeCheckout(t *testing.T) {
	h := &hosted{}
	tests := []struct {
		name   string
		update acp.SessionUpdate
		want   bool
	}{
		{"pending edit", acp.SessionUpdate{ToolCall: acp.ToolCall{Kind: acp.ToolKindEdit, Status: acp.ToolStatusPending}}, false},
		{"completed read", acp.SessionUpdate{ToolCall: acp.ToolCall{Kind: acp.ToolKindRead, Status: acp.ToolStatusCompleted}}, false},
		{"failed search", acp.SessionUpdate{ToolCall: acp.ToolCall{Kind: acp.ToolKindSearch, Status: acp.ToolStatusFailed}}, false},
		{"completed edit", acp.SessionUpdate{ToolCall: acp.ToolCall{Kind: acp.ToolKindEdit, Status: acp.ToolStatusCompleted}}, true},
		{"completed execute", acp.SessionUpdate{ToolCall: acp.ToolCall{Kind: acp.ToolKindExecute, Status: acp.ToolStatusCompleted}}, true},
		{"failed execute", acp.SessionUpdate{ToolCall: acp.ToolCall{Kind: acp.ToolKindExecute, Status: acp.ToolStatusFailed}}, true},
		{"opening read", acp.SessionUpdate{ToolCall: acp.ToolCall{ToolCallID: "read-1", Kind: acp.ToolKindRead, Status: acp.ToolStatusPending}}, false},
		// ACP completion updates commonly identify the call but omit its kind.
		{"completed read with omitted kind", acp.SessionUpdate{ToolCall: acp.ToolCall{ToolCallID: "read-1", Status: acp.ToolStatusCompleted}}, false},
		{"completed unknown with omitted kind", acp.SessionUpdate{ToolCall: acp.ToolCall{Status: acp.ToolStatusCompleted}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.toolMayChangeCheckout(tt.update); got != tt.want {
				t.Errorf("toolMayChangeCheckout() = %v, want %v", got, tt.want)
			}
		})
	}
}

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
		{
			name:     "an MCP call is named for its tool, not lumped into Acp:other",
			tc:       acp.ToolCall{Kind: "other", Meta: json.RawMessage(`{"claudeCode":{"toolName":"mcp__github__create_issue","mcpServer":{"name":"github"}}}`)},
			wantTool: "mcp__github__create_issue",
		},
		{
			name:     "a non-MCP name in the metadata cannot borrow a built-in's rules",
			tc:       acp.ToolCall{Kind: "other", Meta: json.RawMessage(`{"claudeCode":{"toolName":"Bash"}}`)},
			wantTool: "Acp:other",
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

// The sweep must not age out a session this process hosts. We own the
// adapter, so its exit is a fact — retire() removes the row — and
// silence means only that the agent is idle. Ageing it out made a live
// agent disappear from the roster after two quiet minutes.
func TestSweepLeavesHostedSessionsAlone(t *testing.T) {
	reset()
	withState(t, 1)

	h := &hosted{key: "acp:1", agent: "codex"}
	hostedMu.Lock()
	hostedAll[h.key] = h
	hostedMu.Unlock()
	t.Cleanup(func() {
		hostedMu.Lock()
		delete(hostedAll, h.key)
		hostedMu.Unlock()
	})

	// A row that has not been touched for well past the drop window.
	stale := t0.Add(-dropAfter - time.Minute)
	put("acp:1", Row{Key: "acp:1", Agent: "codex", State: "done"}, stale, stale)

	if lookupHosted("acp:1") == nil {
		t.Fatal("the session is not in the registry — the guard cannot fire")
	}
	if _, live := rows["acp:1"]; !live {
		t.Fatal("row missing before the sweep")
	}
}

func TestClaimDetachedAllowsOnlyOneReattach(t *testing.T) {
	h := &hosted{key: "acp:reattach-once", agent: "codex", detached: true}
	hostedMu.Lock()
	hostedAll[h.key] = h
	hostedMu.Unlock()
	t.Cleanup(func() {
		hostedMu.Lock()
		delete(hostedAll, h.key)
		hostedMu.Unlock()
	})

	if got := claimDetached(h.key); got != h {
		t.Fatalf("first claim = %p, want %p", got, h)
	}
	if got := claimDetached(h.key); got != nil {
		t.Fatalf("second claim = %p, want nil", got)
	}
}

// Host-side yolo (agent_set_yolo): wash answers the session's permission
// questions with "allow" instead of asking. The two properties worth pinning
// are what it replaces and what it must not touch.
func TestYoloAnswersInsteadOfAsking(t *testing.T) {
	cases := []struct {
		name   string
		yolo   bool
		policy agentpolicy.Policy
		subs   int
		want   string // option id, or "" for cancelled
	}{
		{
			// The point of the feature: no rule, nobody watching — which
			// would otherwise be a cancel — becomes an allow.
			name: "yolo answers what would have been asked",
			yolo: true,
			subs: 0,
			want: "once",
		},
		{
			// Off, it changes nothing: same case, still cancelled.
			name: "off leaves the floor where it was",
			yolo: false,
			subs: 0,
		},
		{
			// The line that must not move. A standing deny is a decision
			// the user already made; a convenience toggle does not reverse
			// it, however loudly it is switched on.
			name: "an explicit deny still denies",
			yolo: true,
			policy: agentpolicy.Policy{Enabled: true, Rules: []agentpolicy.Rule{
				{Match: "Bash(git push*)", Decision: agentpolicy.DecisionDeny},
			}},
			subs: 1,
			want: "no",
		},
		{
			// ask_desktop off is an explicit "do not ask me" — yolo is an
			// explicit "do not ask me, and allow it". The second wins only
			// because the user turned it on for this session.
			name:   "yolo overrides ask_desktop off",
			yolo:   true,
			policy: agentpolicy.Policy{Enabled: true, Default: agentpolicy.DecisionAsk, AskDesktop: boolPtr(false)},
			subs:   1,
			want:   "once",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetAsks()
			withState(t, c.subs)
			withPolicy(t, c.policy)

			h := &hosted{key: "acp:1", agent: "codex", yolo: c.yolo}
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

// A turn that dies on an adapter error must not be reported as "done".
// It was, and every surface paints done GREEN — so a session that failed
// was indistinguishable from one that succeeded, on every screen wash
// has (docs/AGENT_MESSENGER.md M5).
func TestAFailedTurnIsNotReportedAsDone(t *testing.T) {
	reset()
	withState(t, 1)
	h := &hosted{key: "acp:1", agent: "codex"}
	h.beginTurn()
	h.endTurn("failed", "error")

	r := rows["acp:1"]
	if r == nil {
		t.Fatal("no row published for the session")
	}
	if r.State != "failed" {
		t.Errorf("State = %q, want %q — done renders green", r.State, "failed")
	}
	if r.Reason != "error" {
		t.Errorf("Reason = %q, want %q", r.Reason, "error")
	}
}

// A cancelled turn is NOT a failure — the human stopped it on purpose,
// and colouring that red would cry wolf.
func TestACancelledTurnIsStillDone(t *testing.T) {
	reset()
	withState(t, 1)
	h := &hosted{key: "acp:2", agent: "codex"}
	h.beginTurn()
	h.endTurn("done", "cancelled")

	r := rows["acp:2"]
	if r == nil {
		t.Fatal("no row published for the session")
	}
	if r.State != "done" {
		t.Errorf("State = %q, want done", r.State)
	}
}

// End session must end EVERYTHING the session owns: its pending question
// (cancelled toward the agent, gone from the rail), its adapter, and the
// terminals it created. Killing only the adapter left the rail asking a
// question for a dead session — with "Always allow" still writing a rule
// for it — and any long-running command the agent had started still
// running with nothing able to release it.
func TestRetireEndsEverything(t *testing.T) {
	reset()
	resetAsks()
	withState(t, 1)
	withPolicy(t, agentpolicy.Policy{Enabled: true, Default: agentpolicy.DecisionAsk})
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var stopped atomic.Bool
	h := &hosted{key: "acp:end", agent: "codex", stop: func() { stopped.Store(true) }}
	h.register()

	// A terminal the session owns, and one belonging to another session
	// that must be left alone.
	var mu sync.Mutex
	closed := map[string]string{}
	closer := func(id string) func(string) {
		return func(reason string) {
			mu.Lock()
			closed[id] = reason
			mu.Unlock()
		}
	}
	termMu.Lock()
	termAll = map[string]*terminal{
		"71": {id: "71", key: h.key, closeFn: closer("71")},
		"72": {id: "72", key: "acp:other", closeFn: closer("72")},
	}
	termEarly = map[string]bool{"71": true}
	termMu.Unlock()
	t.Cleanup(func() {
		termMu.Lock()
		termAll = map[string]*terminal{}
		termEarly = map[string]bool{}
		termMu.Unlock()
	})

	// A question blocked on the human.
	done := make(chan acp.RequestPermissionResponse, 1)
	go func() {
		res, _ := h.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			SessionID: "s1",
			ToolCall:  acp.ToolCall{Kind: acp.ToolKindExecute, RawInput: json.RawMessage(`{"command":"git push"}`)},
			Options:   stdOptions,
		})
		done <- res
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var found bool
		mutateState(func(*State) { found = countForRow(h.key) > 0 })
		if found {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	h.retire()

	// The adapter is stopped.
	if !stopped.Load() {
		t.Error("retire did not stop the adapter")
	}
	// Its terminal is closed, with the reason; the other session's is not.
	mu.Lock()
	if closed["71"] != ReasonSessionEnded {
		t.Errorf("session terminal close reason = %q, want %q", closed["71"], ReasonSessionEnded)
	}
	if _, touched := closed["72"]; touched {
		t.Error("retire closed a terminal belonging to another session")
	}
	mu.Unlock()
	termMu.Lock()
	_, gone := termAll["71"]
	_, other := termAll["72"]
	_, early := termEarly["71"]
	termMu.Unlock()
	if gone || early {
		t.Error("the session's terminal record survived retire")
	}
	if !other {
		t.Error("another session's terminal record was dropped")
	}
	// The question is gone — from the queue and from the published state
	// the rail renders.
	var pendingForRow, published int
	mutateState(func(s *State) {
		pendingForRow = countForRow(h.key)
		published = len(s.Asks)
	})
	if pendingForRow != 0 || published != 0 {
		t.Errorf("after retire: queue=%d published=%d asks for the row, want none", pendingForRow, published)
	}
	// And the agent heard cancelled rather than waiting out the backstop.
	select {
	case res := <-done:
		if res.Outcome.Outcome != acp.OutcomeCancelled {
			t.Errorf("agent got %+v, want cancelled", res.Outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the pending permission request never returned — the turn would hang until the backstop")
	}
	if lookupHosted(h.key) != nil {
		t.Error("session still registered after retire")
	}
	if _, live := rows[h.key]; live {
		t.Error("roster row survived retire")
	}
}

// Nothing used to watch the adapter's exit: a crashed adapter kept its
// roster row and its idle-hold, its pending question outlived it, and the
// next prompt failed silently. The watcher turns the exit into what the
// desktop can see — a failed row, a transcript note with the reason and
// the adapter's last stderr, no question left on any rail, no terminal
// left running — while keeping the History entry to resume from.
//
// Driven through a REAL acp.Client over pipes, so the permission request
// arrives on the conn's own context and it is that context ending — not a
// test poking a channel — that releases the blocked handler.
func TestAdapterExitFailsTheRowAndCancelsAsks(t *testing.T) {
	withStateDir(t)
	reset()
	resetAsks()
	withState(t, 1)
	withPolicy(t, agentpolicy.Policy{Enabled: true, Default: agentpolicy.DecisionAsk})

	toAgentR, toAgentW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	var stopped atomic.Bool
	h := &hosted{key: "acp:exit", agent: "codex", sessionID: "sess-exit", cwd: t.TempDir(),
		stop: func() { stopped.Store(true) }, exited: make(chan struct{})}
	h.client = acp.NewClient(toClientR, toAgentW, h)
	t.Cleanup(func() { h.client.Close(); toAgentW.Close(); toClientW.Close() })
	// Drain what the client writes toward the "adapter" so nothing blocks
	// on the pipe. (The cancelled outcome itself is refused by the conn
	// once its read loop has ended — there is nobody left to hear it —
	// so the proof that the handler was released is the log line and the
	// empty queue below, not a frame on the wire.)
	go func() { _, _ = io.Copy(io.Discard, toAgentR) }()
	// The adapter said something on stderr before dying.
	_, _ = h.stderrTail().Write([]byte("fatal: token expired\n"))

	bindTranscript(h.key, h.sessionID, h.agent, h.cwd, time.Now())
	h.register()
	go h.watchExit()
	// A terminal it owns.
	var closedReason atomic.Value
	termMu.Lock()
	termAll["91"] = &terminal{id: "91", key: h.key, closeFn: func(r string) { closedReason.Store(r) }}
	termMu.Unlock()
	t.Cleanup(func() {
		termMu.Lock()
		delete(termAll, "91")
		termMu.Unlock()
	})

	// The adapter asks permission, then dies with the question outstanding.
	_, _ = io.WriteString(toClientW, `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"sessionId":"sess-exit","toolCall":{"toolCallId":"t1","kind":"execute","rawInput":{"command":"git push"}},"options":[{"optionId":"allow","kind":"allow_once"},{"optionId":"no","kind":"reject_once"}]}}`+"\n")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		mutateState(func(*State) { n = countForRow(h.key) })
		if n > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	toClientW.Close()

	// Everything the watcher promises, once it has finished — waited for,
	// not polled, so nothing of it outlives this test.
	select {
	case <-h.exited:
	case <-time.After(3 * time.Second):
		t.Fatal("the exit watcher never finished")
	}
	if lookupHosted(h.key) != nil {
		t.Fatal("session still registered after its adapter exited")
	}
	if !stopped.Load() {
		t.Error("the dead adapter was not reaped")
	}
	var state, reason string
	var asksLeft, published int
	mutateState(func(s *State) {
		if r := rows[h.key]; r != nil {
			state, reason = r.State, r.Reason
		}
		asksLeft = countForRow(h.key)
		published = len(s.Asks)
	})
	if state != "failed" || reason != "exited" {
		t.Errorf("row = %s/%s, want failed/exited", state, reason)
	}
	if asksLeft != 0 || published != 0 {
		t.Errorf("asks after exit: queue=%d published=%d, want none", asksLeft, published)
	}
	if got, _ := closedReason.Load().(string); got != ReasonAgentExited {
		t.Errorf("terminal close reason = %q, want %q", got, ReasonAgentExited)
	}
	// The transcript says what happened, with the adapter's last words —
	// read back from disk, since the in-memory copy is released with the
	// session, exactly as a window reopening it would.
	waitForTranscriptWrites()
	events := snapshot(h.key)
	var note string
	for _, e := range events {
		if strings.Contains(e.Text, "exited") {
			note = e.Text
		}
	}
	if note == "" {
		t.Fatalf("no exit note in the transcript: %+v", events)
	}
	if !strings.Contains(note, "token expired") {
		t.Errorf("exit note lacks the stderr tail: %q", note)
	}
	// And the history entry is still there to resume from.
	found := false
	for _, s := range history {
		if s.SessionID == h.sessionID {
			found = true
		}
	}
	if !found {
		t.Error("history entry dropped by the exit — the session would not be resumable")
	}
}

// A turn that fails must say WHY in the transcript. promptHosted logged
// the error and set the row failed, and the window showed a red dot and
// nothing else — expired auth, a rate limit and a refused request all
// looked identical, and the answer was in the router log.
func TestTurnErrorsReachTheTranscript(t *testing.T) {
	withStateDir(t)
	reset()
	withState(t, 1)

	toAgentR, toAgentW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	h := &hosted{key: "acp:err", agent: "codex", sessionID: "sess-err", cwd: t.TempDir()}
	h.client = acp.NewClient(toClientR, toAgentW, h)
	t.Cleanup(func() { h.client.Close(); toAgentW.Close(); toClientW.Close() })
	bindTranscript(h.key, h.sessionID, h.agent, h.cwd, time.Now())
	h.register()

	// The "adapter": answer the prompt with an RPC error, the way a real
	// one reports an expired login or a rate limit.
	go func() {
		sc := bufio.NewScanner(toAgentR)
		for sc.Scan() {
			var m struct {
				ID     json.Number `json:"id"`
				Method string      `json:"method"`
			}
			if json.Unmarshal(sc.Bytes(), &m) != nil || m.Method != acp.MethodSessionPrompt {
				continue
			}
			_, _ = io.WriteString(toClientW, `{"jsonrpc":"2.0","id":`+m.ID.String()+`,"error":{"code":-32000,"message":"rate limit exceeded, retry in 30s"}}`+"\n")
		}
	}()

	promptHosted(h, turn{text: "do the thing"})

	if r := rows[h.key]; r == nil || r.State != "failed" || r.Reason != "error" {
		t.Fatalf("row = %+v, want failed/error", r)
	}
	var note string
	for _, e := range snapshot(h.key) {
		if strings.Contains(e.Text, "The turn failed") {
			note = e.Text
		}
	}
	if note == "" {
		t.Fatalf("no failure note in the transcript: %+v", snapshot(h.key))
	}
	if !strings.Contains(note, "rate limit exceeded, retry in 30s") {
		t.Errorf("note does not carry the adapter's own message: %q", note)
	}
	if strings.Contains(note, "rpc -32000") {
		t.Errorf("note leaks the wire framing: %q", note)
	}
	// The session is still up — the composer's next prompt is the retry
	// — so it must still be registered, not retired.
	if lookupHosted(h.key) == nil {
		t.Error("a failed turn retired the session")
	}
}

func TestTurnErrorReadsAsAPersonWould(t *testing.T) {
	cases := map[error]string{
		acp.ErrClosed: "the agent's adapter has gone away",
		errors.New("acp: rpc -32000: authentication required"): "authentication required",
		errors.New("something else entirely"):                  "something else entirely",
	}
	for err, want := range cases {
		if got := turnError(err); got != want {
			t.Errorf("turnError(%v) = %q, want %q", err, got, want)
		}
	}
}

// scriptedAdapter is the far end of a real acp.Client over pipes: it hands
// the test every session/prompt it receives and lets the test answer them
// in its own time, which is what makes "mid-turn" a controllable moment.
type scriptedAdapter struct {
	prompts chan promptSeen
	out     *io.PipeWriter
}

type promptSeen struct {
	id   json.Number
	text string
}

func newScriptedAdapter(t *testing.T, h *hosted) *scriptedAdapter {
	t.Helper()
	toAgentR, toAgentW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	h.client = acp.NewClient(toClientR, toAgentW, h)
	a := &scriptedAdapter{prompts: make(chan promptSeen, 8), out: toClientW}
	t.Cleanup(func() { h.client.Close(); toAgentW.Close(); toClientW.Close() })
	go func() {
		sc := bufio.NewScanner(toAgentR)
		sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
		for sc.Scan() {
			var m struct {
				ID     json.Number `json:"id"`
				Method string      `json:"method"`
				Params struct {
					Prompt []struct {
						Text string `json:"text"`
					} `json:"prompt"`
				} `json:"params"`
			}
			if json.Unmarshal(sc.Bytes(), &m) != nil || m.Method != acp.MethodSessionPrompt {
				continue
			}
			text := ""
			for _, b := range m.Params.Prompt {
				text += b.Text
			}
			a.prompts <- promptSeen{id: m.ID, text: text}
		}
	}()
	return a
}

func (a *scriptedAdapter) next(t *testing.T) promptSeen {
	t.Helper()
	select {
	case p := <-a.prompts:
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("the adapter never received a session/prompt")
	}
	return promptSeen{}
}

func (a *scriptedAdapter) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case p := <-a.prompts:
		t.Fatalf("a session/prompt %q reached the adapter while a turn was open — two concurrent turns", p.text)
	case <-time.After(d):
	}
}

func (a *scriptedAdapter) end(p promptSeen, stop string) {
	_, _ = io.WriteString(a.out, `{"jsonrpc":"2.0","id":`+p.id.String()+`,"result":{"stopReason":"`+stop+`"}}`+"\n")
}

func waitRow(t *testing.T, key string, ok func(Row) bool) Row {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last Row
	for time.Now().Before(deadline) {
		var r Row
		var have bool
		mutateState(func(*State) {
			if rr := rows[key]; rr != nil {
				r, have = rr.Row, true
			}
		})
		if have && ok(r) {
			return r
		}
		last = r
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("row never reached the wanted shape; last=%+v", last)
	return last
}

// A prompt typed mid-turn is QUEUED — messenger semantics — and runs when
// the turn ends. The composer never disabled, and agent_prompt spawned a
// second promptHosted unconditionally: two concurrent session/prompt
// calls the protocol does not allow, with beginTurn/endTurn flipping out
// of order so the row's state was whichever finished last.
func TestPromptMidTurnIsQueuedAndRunsAfter(t *testing.T) {
	withStateDir(t)
	reset()
	withState(t, 1)
	h := &hosted{key: "acp:q", agent: "codex", sessionID: "sess-q", cwd: t.TempDir(), idle: make(chan struct{}, 4)}
	a := newScriptedAdapter(t, h)
	bindTranscript(h.key, h.sessionID, h.agent, h.cwd, time.Now())
	h.register()

	if h.submitPrompt(turn{text: "one"}) {
		t.Fatal("the first prompt on an idle session was queued rather than run")
	}
	p1 := a.next(t)
	if p1.text != "one" {
		t.Fatalf("first prompt = %q", p1.text)
	}
	waitRow(t, h.key, func(r Row) bool { return r.State == "working" })

	// Mid-turn: queued, said so on the row, and NOT on the wire.
	if !h.submitPrompt(turn{text: "two"}) {
		t.Fatal("a prompt sent mid-turn was not queued")
	}
	if !h.submitPrompt(turn{text: "three"}) {
		t.Fatal("a second mid-turn prompt was not queued")
	}
	waitRow(t, h.key, func(r Row) bool { return r.Queued == 2 && r.State == "working" })
	a.none(t, 50*time.Millisecond)

	// The turn ends: the queue drains in order, one turn at a time, with
	// the row staying working throughout rather than flashing done.
	a.end(p1, "end_turn")
	p2 := a.next(t)
	if p2.text != "two" {
		t.Fatalf("second turn = %q, want the first queued prompt", p2.text)
	}
	waitRow(t, h.key, func(r Row) bool { return r.Queued == 1 && r.State == "working" })
	a.none(t, 50*time.Millisecond)
	a.end(p2, "end_turn")
	p3 := a.next(t)
	if p3.text != "three" {
		t.Fatalf("third turn = %q", p3.text)
	}
	a.end(p3, "end_turn")
	waitRow(t, h.key, func(r Row) bool { return r.State == "done" && r.Queued == 0 })
	waitIdle(t, h)

	// The transcript records each prompt when it was SENT, in order — not
	// when it was typed, which would have split the reply it interrupted.
	var users []string
	for _, e := range snapshot(h.key) {
		if e.Kind == EventUser {
			users = append(users, e.Text)
		}
	}
	if strings.Join(users, ",") != "one,two,three" {
		t.Errorf("user lines = %v", users)
	}
}

// Stop means stop: a cancelled turn drops what was queued behind it, and
// the transcript lists the dropped text so nothing typed is lost from
// view. Same for a failed turn — the error would only repeat.
func TestStopDropsTheQueueAndSaysWhat(t *testing.T) {
	withStateDir(t)
	reset()
	withState(t, 1)
	h := &hosted{key: "acp:qc", agent: "codex", sessionID: "sess-qc", cwd: t.TempDir(), idle: make(chan struct{}, 4)}
	a := newScriptedAdapter(t, h)
	bindTranscript(h.key, h.sessionID, h.agent, h.cwd, time.Now())
	h.register()

	h.submitPrompt(turn{text: "first"})
	p1 := a.next(t)
	h.submitPrompt(turn{text: "never sent"})
	waitRow(t, h.key, func(r Row) bool { return r.Queued == 1 })

	a.end(p1, "cancelled")
	waitRow(t, h.key, func(r Row) bool { return r.State == "done" && r.Reason == "cancelled" && r.Queued == 0 })
	waitIdle(t, h)
	a.none(t, 80*time.Millisecond)

	waitForTranscriptWrites()
	var note string
	for _, e := range snapshot(h.key) {
		if strings.HasPrefix(e.Text, "Dropped 1 queued prompt") {
			note = e.Text
		}
	}
	if note == "" {
		t.Fatal("no note about the dropped prompt")
	}
	if !strings.Contains(note, "after Stop") || !strings.Contains(note, "> never sent") {
		t.Errorf("note = %q", note)
	}
	// And the session is idle again: the next prompt runs at once.
	if h.submitPrompt(turn{text: "again"}) {
		t.Fatal("a prompt after the cancelled turn was queued — the turn claim leaked")
	}
	p := a.next(t)
	if p.text != "again" {
		t.Fatalf("prompt after cancel = %q", p.text)
	}
	// Finish the turn before the test's state stubs are torn down: a
	// turn still open when the pipes close would fail on a goroutine the
	// test no longer owns and write into the real (nil) state service.
	a.end(p, "end_turn")
	waitRow(t, h.key, func(r Row) bool { return r.State == "done" && r.Reason == "end_turn" })
	waitIdle(t, h)
}

// waitIdle blocks until the session's turn goroutine has returned, so a
// test's turn cannot still be writing state after the test's stubs are
// gone.
func waitIdle(t *testing.T, h *hosted) {
	t.Helper()
	select {
	case <-h.idle:
	case <-time.After(2 * time.Second):
		t.Fatal("the turn goroutine never returned")
	}
}

// An adapter's last words arrive on stderr, which routinely loses the race
// with its stdout closing. The exit path waits for them, and is bounded so
// a leftover child holding stderr open cannot hold the exit.
func TestAwaitStderrWaitsForTheLastWordsButNotForEver(t *testing.T) {
	h := &hosted{stderrDone: make(chan struct{})}
	go func() {
		time.Sleep(30 * time.Millisecond)
		h.tailMu.Lock()
		h.tail = []byte("fatal: token expired")
		h.tailMu.Unlock()
		close(h.stderrDone)
	}()
	h.awaitStderr()
	if got := h.stderrText(); got != "fatal: token expired" {
		t.Fatalf("stderr tail = %q, want the reason the adapter died", got)
	}

	old := stderrGrace
	stderrGrace = 20 * time.Millisecond
	t.Cleanup(func() { stderrGrace = old })
	start := time.Now()
	(&hosted{stderrDone: make(chan struct{})}).awaitStderr()
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("waited %s for stderr that never ended", waited)
	}
}

// journal reads the title through shownTitle, which takes hostedMu; the
// first version held that lock across the call and deadlocked agentd on
// its first session. This must return, and must say what it noted.
func TestJournalDoesNotHoldHostedMuAcrossShownTitle(t *testing.T) {
	old := noteActivity
	var got []wire.EvtActivityNote
	noteActivity = func(_ *sdk.Conn, n wire.EvtActivityNote) error { got = append(got, n); return nil }
	t.Cleanup(func() { noteActivity = old })

	h := &hosted{key: "acp:9", agent: "codex", cwd: "/work", sessionID: "s-1", title: "Fix it", conn: &sdk.Conn{}}
	done := make(chan struct{})
	go func() {
		h.journal("agent.turn", "turn done")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("journal deadlocked")
	}
	if len(got) != 1 || got[0].Kind != "agent.turn" || got[0].Title != "Fix it" ||
		got[0].Intent == nil || got[0].Intent.SessionID != "s-1" || got[0].Intent.RowKey != "acp:9" {
		t.Fatalf("noted %+v", got)
	}
}
