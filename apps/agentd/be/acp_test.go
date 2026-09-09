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
		stop: func() { stopped.Store(true) }}
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

	// Everything the watcher promises, polled: it runs on its own goroutine.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && (lookupHosted(h.key) != nil || !stopped.Load()) {
		time.Sleep(5 * time.Millisecond)
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

	promptHosted(h, "do the thing")

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
