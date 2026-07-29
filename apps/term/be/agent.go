// Agent status for terminal tabs (docs/AGENT_TERM.md §5, M1).
//
// Two detection tiers feed one per-tab record:
//
//   - T0, the existing 1Hz foreground poll: the foreground program's comm
//     matched against pty's agent table. Free, no install, but it only
//     knows "an agent is running here".
//   - T1, OSC 7770 events emitted by installed agent hooks and picked out
//     of the pty stream by internal/pty's scanner: working / needs-input /
//     done, with the agent's own session id.
//
// The merged view goes to the FE as an `agent_status` app_msg, following
// the tab_status pattern exactly: send-on-change with a per-channel dedupe
// key, re-seeded when the FE asks for list_sessions. Nothing is persisted —
// a reattach re-seeds from the next poll tick or the next OSC event.
package term

import (
	"log"
	"time"

	"github.com/sirmick/wash/internal/pty"
	"github.com/sirmick/wash/internal/sdk"
)

// Wire states for agent_status.state. "running" is what T0 alone can say;
// the other three come from OSC events and are what the FE colours the tab
// dot with (blue / amber / green).
const (
	agentStateRunning    = "running"
	agentStateWorking    = "working"
	agentStateNeedsInput = "needs-input"
	agentStateDone       = "done"
)

// agentOSCStale bounds how long an OSC-reported state outlives its agent.
// An agent killed with SIGKILL never fires its SessionEnd hook, so without
// this a tab would wear a frozen "working" dot forever. The T0 poll is the
// liveness signal: once it stops seeing an agent in the foreground and no
// fresh OSC event has arrived for this long, the record is dropped.
//
// Deliberately NOT applied while the foreground program is ssh: a remote
// agent is invisible to T0 by construction (all we see locally is the ssh
// client), yet its hooks still reach us through the same pty.
const agentOSCStale = 30 * time.Second

// agentRec is one tab's agent status: the T1 (OSC) half and the T0 (poll)
// half, kept separate so either can go away without erasing the other.
// Guarded by st.mu.
type agentRec struct {
	// oscState is "" until an OSC event arrives; the other osc* fields
	// are only meaningful while it is set.
	oscState   string
	oscAgent   string
	oscSession string
	oscReason  string
	oscSince   time.Time // when the CURRENT state began (drives since_ms)
	oscSeen    time.Time // last OSC event of any kind (drives staleness)

	pollAgent string    // T0 slug, "" when the foreground isn't an agent
	pollSince time.Time // when T0 first saw this agent
	pollSSH   bool      // foreground is an ssh client (see agentOSCStale)
}

// agentView is the merged, FE-facing shape. ok=false means "no agent
// here", which the FE renders by clearing the tab's dot.
type agentView struct {
	Agent   string
	State   string
	Session string
	Reason  string
	Since   time.Time
}

// view merges the two tiers. T1 wins when it has something to say (it
// knows the state, T0 only knows presence); T0 fills in the agent name
// when the hook didn't send one, and is the fallback for agents wash has
// no hooks for.
func (r *agentRec) view(now time.Time) (agentView, bool) {
	if r.oscState != "" && !r.oscExpired(now) {
		agent := r.oscAgent
		if agent == "" {
			agent = r.pollAgent
		}
		if agent == "" {
			// A hook that reported no slug at all still means an agent.
			agent = "agent"
		}
		return agentView{
			Agent:   agent,
			State:   r.oscState,
			Session: r.oscSession,
			Reason:  r.oscReason,
			Since:   r.oscSince,
		}, true
	}
	if r.pollAgent != "" {
		return agentView{Agent: r.pollAgent, State: agentStateRunning, Since: r.pollSince}, true
	}
	return agentView{}, false
}

// oscExpired reports whether the OSC half has outlived its agent — see
// agentOSCStale.
func (r *agentRec) oscExpired(now time.Time) bool {
	if r.pollAgent != "" || r.pollSSH {
		return false
	}
	return now.Sub(r.oscSeen) > agentOSCStale
}

// applyOSC folds one OSC 7770 event into the record. Returns false for an
// event that changes nothing (so the caller can skip a publish).
//
// The events are advisory — any process in the pty can write them — so
// this only ever moves UI state. It never writes to the pty and never
// makes a decision on the agent's behalf (that's M3's socket, with its own
// trust story).
func (r *agentRec) applyOSC(ev pty.AgentEvent, now time.Time) bool {
	if ev.Agent != "" {
		r.oscAgent = ev.Agent
	}
	if ev.Session != "" {
		r.oscSession = ev.Session
	}
	r.oscSeen = now

	var state, reason string
	switch ev.Event {
	case pty.AgentEvStart:
		state = agentStateRunning
	case pty.AgentEvWorking:
		state = agentStateWorking
	case pty.AgentEvNeedsInput:
		state, reason = agentStateNeedsInput, ev.Reason
	case pty.AgentEvDone:
		state = agentStateDone
	case pty.AgentEvEnd:
		// The agent said goodbye: drop the T1 half entirely and fall
		// back to T0 (which will itself clear once the process exits).
		*r = agentRec{pollAgent: r.pollAgent, pollSince: r.pollSince, pollSSH: r.pollSSH}
		return true
	default:
		return false
	}
	if r.oscState == state && r.oscReason == reason {
		// Same state again (a second permission prompt in one turn):
		// keep the original since_ms so the elapsed clock doesn't reset.
		return false
	}
	r.oscState, r.oscReason, r.oscSince = state, reason, now
	return true
}

// applyPoll folds one foreground-poll sample into the record.
func (r *agentRec) applyPoll(fu pty.ForegroundUser, now time.Time) {
	r.pollSSH = fu.State == "ssh"
	if fu.Agent == r.pollAgent {
		return
	}
	r.pollAgent = fu.Agent
	r.pollSince = now
}

// agentKey is the send-on-change dedupe key. since_ms is deliberately
// excluded — it moves every tick, and the FE counts elapsed time up from
// the value it was given.
func agentKey(v agentView, ok bool) string {
	if !ok {
		return ""
	}
	return v.Agent + "\x00" + v.State + "\x00" + v.Session + "\x00" + v.Reason
}

// publishAgent recomputes a tab's merged agent view and pushes an
// agent_status app_msg when it differs from the last one sent. Called from
// the 1Hz poll and, for zero added latency, straight off an OSC event.
//
// Must not be called with st.mu held.
func publishAgent(c *sdk.Conn, id uint32) {
	now := time.Now()
	st.mu.Lock()
	rec := st.agents[id]
	if rec == nil {
		st.mu.Unlock()
		return
	}
	v, ok := rec.view(now)
	if !ok && rec.oscState == "" && rec.pollAgent == "" {
		// Nothing left to track; keep the map small.
		delete(st.agents, id)
	}
	key := agentKey(v, ok)
	if st.agentSent[id] == key {
		st.mu.Unlock()
		return
	}
	st.agentSent[id] = key
	st.mu.Unlock()

	if ok {
		log.Printf("term: agent-status ch=%d agent=%s state=%s session=%s reason=%s",
			id, v.Agent, v.State, v.Session, v.Reason)
	} else {
		log.Printf("term: agent-status ch=%d agent=none", id)
	}
	_ = c.SendAppMsg(map[string]any{
		"kind":       "agent_status",
		"channel_id": uint64(id),
		"agent":      v.Agent,
		"state":      v.State,
		// since_ms: how long the tab has been in this state, at send
		// time. The FE anchors its own clock to it and counts up.
		"since_ms":   uint64(elapsedMS(v.Since, now)),
		"session_id": v.Session,
		"reason":     v.Reason,
	})
}

func elapsedMS(since, now time.Time) int64 {
	if since.IsZero() {
		return 0
	}
	ms := now.Sub(since).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

// onAgentEvent is the pty scanner's callback: it runs on the pty→channel
// copy goroutine, so it does the bookkeeping and one SendAppMsg, nothing
// more.
func onAgentEvent(c *sdk.Conn, id uint32, ev pty.AgentEvent) {
	now := time.Now()
	st.mu.Lock()
	rec := st.agents[id]
	if rec == nil {
		if _, live := st.sessions[id]; !live {
			// Tab already closed — nothing to report against.
			st.mu.Unlock()
			return
		}
		rec = &agentRec{}
		st.agents[id] = rec
	}
	changed := rec.applyOSC(ev, now)
	st.mu.Unlock()
	if changed {
		publishAgent(c, id)
	}
}
