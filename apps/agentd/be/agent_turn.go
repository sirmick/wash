package agentd

import (
	"context"
	"encoding/json"
	"log"
	"maps"
	"time"

	"github.com/sirmick/wash/internal/acp"
)

// Turns the agent starts itself, and turns that will not end.
//
// Claude Code runs turns Wash did not send: a background task it started
// (Bash with run_in_background) wakes it when it finishes, and it answers in
// a turn of its own, with no session/prompt open. Wash only knew about turns
// it sent, so the session looked idle. A prompt sent into that turn is
// swallowed by claude-agent-acp 0.81.2: it never reaches the model and never
// settles, and a cancel alone does not release it (reproduced live
// 2026-09-28; the Redoubt FMT1 hang). Everything queued behind it waited for
// good. Claude Code reports its own run state as session_state_changed; the
// session asks for those messages (claudeStateMeta), and Wash holds prompts
// while the agent is running.
//
// A turn can still fail to end (an adapter bug not yet met), and a cancel
// that waited for its answer forever unstuck nothing. cancelTurn gives the
// agent cancelDeadline to end the turn, then ends it on Wash's side.

// claudeAdapter is claude-agent-acp's name at initialize.
const claudeAdapter = "@agentclientprotocol/claude-agent-acp"

// cancelDeadline is how long a cancelled turn has to end before Wash stops
// waiting for it. A variable for tests.
var cancelDeadline = 10 * time.Second

// claudeStateMeta adds the request for Claude Code's run state to a
// session's _meta, keeping whatever else it carries.
func claudeStateMeta(meta map[string]any) map[string]any {
	out := maps.Clone(meta)
	if out == nil {
		out = map[string]any{}
	}
	cc, _ := out["claudeCode"].(map[string]any)
	cc = maps.Clone(cc)
	if cc == nil {
		cc = map[string]any{}
	}
	cc["emitRawSDKMessages"] = []map[string]any{{"type": "system", "subtype": "session_state_changed"}}
	out["claudeCode"] = cc
	return out
}

// SDKMessage follows Claude Code's run state (acp.SDKMessages).
func (h *hosted) SDKMessage(_ context.Context, n acp.SDKMessageNotification) {
	var m struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		State   string `json:"state"`
	}
	if json.Unmarshal(n.Message, &m) != nil || m.Type != "system" || m.Subtype != "session_state_changed" {
		return
	}
	// requires_action is a turn waiting on a permission or a question: still
	// the agent's turn.
	h.agentState(m.State != "idle")
}

// agentState records whether the agent says it is running. Outside a turn
// of Wash's, running opens the agent's own turn and idle closes it; either
// way idle is when a held prompt goes.
func (h *hosted) agentState(running bool) {
	h.turnMu.Lock()
	h.agentRunning = running
	var next turn
	switch {
	case h.turnLive:
	case running && !h.ownTurn:
		h.ownTurn = true
		h.turnEnd = make(chan struct{})
		h.activityPhase = "working"
		h.activityTools = map[string]string{}
		h.setState("working", "")
		h.journal("agent.turn", "turn started by the agent")
	case !running:
		if h.ownTurn {
			h.endOwnTurn("")
			h.journal("agent.turn", "turn done (started by the agent)")
		}
		if len(h.pending) > 0 {
			next, h.pending = h.pending[0], h.pending[1:]
			h.queued.Store(int32(len(h.pending)))
			h.turnLive = true
			h.setState("working", "")
		}
	}
	h.turnMu.Unlock()
	if !next.empty() {
		log.Printf("agentd: acp prompt released key=%s: the agent is idle", h.key)
		h.run(next)
	}
	if !running && workspaces != nil {
		workspaces.signal()
	}
}

// endOwnTurn closes the agent's own turn. Caller holds turnMu.
func (h *hosted) endOwnTurn(reason string) {
	h.ownTurn = false
	close(h.turnEnd)
	h.turnEnd = nil
	flushTranscript(h.key)
	h.setState("done", reason)
}

// busy reports a turn in progress, Wash's or the agent's own. Caller holds
// turnMu.
func (h *hosted) busy() bool { return h.turnLive || h.ownTurn }

// cancelTurn asks the agent to stop its turn and waits for the turn to end.
// If it has not ended within cancelDeadline, Wash ends it: it gives up its
// session/prompt call (the outcome is "abandoned", see promptHosted), or
// forgets the agent's own turn, so what is queued can go. running reports
// whether there was a turn to cancel.
func (h *hosted) cancelTurn() (running, abandoned bool, err error) {
	h.turnMu.Lock()
	end := h.turnEnd
	h.turnMu.Unlock()
	if end == nil {
		return false, false, nil
	}
	if err := h.client.Cancel(h.sessionID); err != nil {
		return true, false, err
	}
	select {
	case <-end:
		return true, false, nil
	case <-time.After(cancelDeadline):
	}
	h.turnMu.Lock()
	if h.turnEnd != end {
		h.turnMu.Unlock()
		return true, false, nil
	}
	own, abort := h.ownTurn, h.turnAbort
	if own {
		h.agentRunning = false
		h.endOwnTurn("cancelled")
	}
	h.turnMu.Unlock()
	log.Printf("agentd: acp turn abandoned key=%s own=%t: no end within %s of cancel", h.key, own, cancelDeadline)
	h.journal("agent.turn", "turn abandoned: the agent did not end it within "+cancelDeadline.String()+" of a cancel")
	if !own && abort != nil {
		h.abandoned.Store(true)
		abort()
		<-end
	}
	return true, true, nil
}
