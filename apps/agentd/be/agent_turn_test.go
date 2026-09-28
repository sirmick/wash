package agentd

import (
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/agentproto"
)

// state sends Claude Code's run state as claude-agent-acp forwards it.
func (a *scriptedAdapter) state(s string) {
	_, _ = io.WriteString(a.out, `{"jsonrpc":"2.0","method":"_claude/sdkMessage","params":{"sessionId":"x","message":{"type":"system","subtype":"session_state_changed","state":"`+s+`"}}}`+"\n")
}

func newTurnSession(t *testing.T, key string) (*hosted, *scriptedAdapter) {
	t.Helper()
	withStateDir(t)
	reset()
	withState(t, 1)
	h := &hosted{key: key, agent: "claude", sessionID: "sess-" + key, cwd: t.TempDir(), idle: make(chan struct{}, 4)}
	a := newScriptedAdapter(t, h)
	bindTranscript(h.key, h.sessionID, h.record(), h.cwd, time.Now())
	h.register()
	return h, a
}

func (h *hosted) isBusy() bool {
	h.turnMu.Lock()
	defer h.turnMu.Unlock()
	return h.busy()
}

// A background task finishing wakes Claude Code into a turn of its own. A
// prompt sent into that turn is swallowed by the adapter (the Redoubt FMT1
// hang), so it is held until the agent says it is idle.
func TestPromptWaitsForTheAgentsOwnTurn(t *testing.T) {
	h, a := newTurnSession(t, "acp:own")

	a.state("running")
	waitRow(t, h.key, func(r agentproto.Row) bool { return r.State == "working" })
	if !h.isBusy() {
		t.Fatal("the agent's own turn does not count as busy")
	}
	if !h.submitPrompt(turn{text: "held"}) {
		t.Fatal("a prompt during the agent's own turn was sent rather than held")
	}
	a.none(t, 50*time.Millisecond)

	a.state("idle")
	p := a.next(t)
	if p.text != "held" {
		t.Fatalf("released prompt = %q", p.text)
	}
	a.end(p, "end_turn")
	waitRow(t, h.key, func(r agentproto.Row) bool { return r.State == "done" && r.Reason == "end_turn" })
	waitIdle(t, h)
}

// Claude Code reports running for Wash's own prompts too, and idle a moment
// after the prompt's answer. Neither opens a turn of the agent's, and the
// idle does not rewrite how Wash's turn ended.
func TestRunStateDuringOurTurnChangesNothing(t *testing.T) {
	h, a := newTurnSession(t, "acp:ours")

	h.submitPrompt(turn{text: "one"})
	p := a.next(t)
	a.state("running")
	a.end(p, "max_tokens")
	waitRow(t, h.key, func(r agentproto.Row) bool { return r.State == "done" && r.Reason == "max_tokens" })
	waitIdle(t, h)
	a.state("idle")
	time.Sleep(30 * time.Millisecond)
	r := waitRow(t, h.key, func(agentproto.Row) bool { return true })
	if r.State != "done" || r.Reason != "max_tokens" {
		t.Errorf("the idle after our turn rewrote its outcome: %+v", r)
	}
	if h.isBusy() {
		t.Error("busy after our turn and the idle")
	}
}

// A prompt typed between Wash's turn ending and the agent's idle waits for
// the idle rather than racing it.
func TestPromptBeforeTheIdleGoesAtTheIdle(t *testing.T) {
	h, a := newTurnSession(t, "acp:tail")

	h.submitPrompt(turn{text: "one"})
	p := a.next(t)
	a.state("running")
	a.end(p, "end_turn")
	waitIdle(t, h)
	if !h.submitPrompt(turn{text: "two"}) {
		t.Fatal("sent while the agent still reported running")
	}
	a.none(t, 30*time.Millisecond)
	a.state("idle")
	p2 := a.next(t)
	if p2.text != "two" {
		t.Fatalf("prompt = %q", p2.text)
	}
	a.end(p2, "end_turn")
	waitIdle(t, h)
}

// A cancel the agent never answers no longer wedges the session: after
// cancelDeadline Wash gives up the call, and the next prompt runs.
func TestUnansweredCancelAbandonsTheTurn(t *testing.T) {
	h, a := newTurnSession(t, "acp:hang")
	old := cancelDeadline
	cancelDeadline = 50 * time.Millisecond
	t.Cleanup(func() { cancelDeadline = old })

	h.submitPrompt(turn{text: "stuck"})
	p1 := a.next(t)
	running, abandoned, err := h.cancelTurn()
	if err != nil || !running || !abandoned {
		t.Fatalf("cancelTurn = %t %t %v, want running and abandoned", running, abandoned, err)
	}
	waitRow(t, h.key, func(r agentproto.Row) bool { return r.State == "done" && r.Reason == "cancelled" })
	waitIdle(t, h)
	waitForTranscriptWrites()
	found := false
	for _, e := range snapshot(h.key) {
		found = found || strings.HasPrefix(e.Text, "Wash stopped waiting for this turn")
	}
	if !found {
		t.Error("the transcript does not say the turn was abandoned")
	}

	if h.submitPrompt(turn{text: "next"}) {
		t.Fatal("the prompt after an abandoned turn was queued")
	}
	p2 := a.next(t)
	// The adapter answers the abandoned call late; nobody is waiting on it.
	a.end(p1, "cancelled")
	a.end(p2, "end_turn")
	waitRow(t, h.key, func(r agentproto.Row) bool { return r.State == "done" && r.Reason == "end_turn" })
	waitIdle(t, h)
}

// A cancel the agent answers in time is not abandoned.
func TestAnsweredCancelIsNotAbandoned(t *testing.T) {
	h, a := newTurnSession(t, "acp:ok")

	h.submitPrompt(turn{text: "one"})
	p := a.next(t)
	go func() {
		time.Sleep(20 * time.Millisecond)
		a.end(p, "cancelled")
	}()
	if _, abandoned, err := h.cancelTurn(); err != nil || abandoned {
		t.Fatalf("cancelTurn abandoned=%t err=%v", abandoned, err)
	}
	waitIdle(t, h)
}

// No turn, nothing to cancel.
func TestCancelWithNoTurn(t *testing.T) {
	h, _ := newTurnSession(t, "acp:none")
	if running, _, _ := h.cancelTurn(); running {
		t.Fatal("reported a turn on an idle session")
	}
}

// The agent's own turn that does not stop on a cancel is forgotten, so
// held prompts can go.
func TestUnansweredCancelForgetsTheAgentsOwnTurn(t *testing.T) {
	h, a := newTurnSession(t, "acp:ownhang")
	old := cancelDeadline
	cancelDeadline = 50 * time.Millisecond
	t.Cleanup(func() { cancelDeadline = old })

	a.state("running")
	waitRow(t, h.key, func(r agentproto.Row) bool { return r.State == "working" })
	if _, abandoned, _ := h.cancelTurn(); !abandoned {
		t.Fatal("the agent's own turn was not abandoned")
	}
	if h.isBusy() {
		t.Fatal("still busy after abandoning the agent's own turn")
	}
	if h.submitPrompt(turn{text: "go"}) {
		t.Fatal("held after the own turn was abandoned")
	}
	p := a.next(t)
	a.end(p, "end_turn")
	waitIdle(t, h)
}

func TestClaudeStateMetaKeepsWhatWasThere(t *testing.T) {
	in := map[string]any{"claudeCode": map[string]any{"options": map[string]any{"disallowedTools": []string{"Task"}}}}
	out := claudeStateMeta(in)
	cc := out["claudeCode"].(map[string]any)
	if !reflect.DeepEqual(cc["options"], in["claudeCode"].(map[string]any)["options"]) {
		t.Errorf("options lost: %v", cc)
	}
	if _, ok := in["claudeCode"].(map[string]any)["emitRawSDKMessages"]; ok {
		t.Error("the input was modified")
	}
	if cc["emitRawSDKMessages"] == nil {
		t.Error("no emitRawSDKMessages")
	}
	if claudeStateMeta(nil)["claudeCode"] == nil {
		t.Error("nil meta got nothing")
	}
}
