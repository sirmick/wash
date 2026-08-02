// Asking the desktop (docs/AGENT_TERM.md §12, M6).
//
// When the policy has no answer for a tool request, the terminal can ask
// the human where they already are instead of falling straight through to
// the agent's own prompt: it hands the question to com.wash.agentd, which
// puts it in the roster the sidebar renders, and waits for the click to
// come back.
//
// The waiting is the delicate part, because a socket handler is holding an
// agent's turn open while it happens. Hence: a hard deadline shorter than
// the helper's, a reply channel that is always closed exactly once, and
// "defer" for every path that isn't a human answering — service missing,
// service restarted, timeout, terminal shutting down.
package term

import (
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

// askWait bounds how long the terminal holds a request while the desktop
// thinks. Deliberately longer than agentd's own 30s TTL (so its timeout
// fires first and the answer is authoritative) and shorter than the
// helper's 45s read deadline (so the helper never gives up on a live
// answer). §12: three nested deadlines, innermost first.
const askWait = 35 * time.Second

// DecisionDefer is "wash has no answer" — the helper prints nothing and
// the agent's own prompt appears. Every failure resolves here.
const DecisionDefer = "defer"

// pendingAsks maps a request id to the channel its socket handler is
// parked on. One entry per in-flight question.
var pendingAsks sync.Map // string → chan string

// askSeq numbers this process's questions.
var askSeq atomic.Uint64

// askDesktop puts the question to the human and returns their decision, or
// DecisionDefer if nobody answered in time. Blocks for up to askWait —
// callers must be on a per-connection goroutine, never the SDK's dispatch
// path.
func askDesktop(c *sdk.Conn, id uint32, req decideRequest, subject string) string {
	if c == nil {
		// No app connection (a terminal running outside a router, or a
		// unit test): there is nobody to ask, which is a defer.
		return DecisionDefer
	}
	reqID := strconv.FormatUint(askSeq.Add(1), 10)
	reply := make(chan string, 1)
	pendingAsks.Store(reqID, reply)
	defer pendingAsks.Delete(reqID)

	err := c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
		"kind":       "agent_ask",
		"req_id":     reqID,
		"channel_id": uint64(id),
		"agent":      askAgentName(id),
		"tool":       req.ToolName,
		"subject":    subject,
		"cwd":        req.Cwd,
	})
	if err != nil {
		// No roster service (or no router): behave exactly as M3 did.
		return DecisionDefer
	}

	select {
	case d := <-reply:
		return d
	case <-time.After(askWait):
		// agentd's own TTL is shorter, so reaching here means the service
		// went away mid-question rather than the human being slow.
		log.Printf("term: agent-ask ch=%d tool=%s no answer within %s", id, req.ToolName, askWait)
		return DecisionDefer
	case <-c.Done():
		return DecisionDefer
	}
}

// askAgentName is the agent slug for a tab, for the question's text. Falls
// back to a generic name — a request without a detected agent is still a
// request worth asking about.
func askAgentName(id uint32) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	if rec := st.agents[id]; rec != nil {
		if rec.oscAgent != "" {
			return rec.oscAgent
		}
		if rec.pollAgent != "" {
			return rec.pollAgent
		}
	}
	return "agent"
}

// onAskResult is agentd's reply. Registered on the term's bus; runs on the
// SDK dispatch path, so it only hands the value to the waiting goroutine.
func onAskResult(req askResultMsg) {
	v, ok := pendingAsks.Load(req.ReqID)
	if !ok {
		// The handler already gave up (or answered) — nothing to do.
		return
	}
	ch, ok := v.(chan string)
	if !ok {
		return
	}
	select {
	case ch <- req.Decision:
	default:
		// Buffered size 1; a second answer for one question is a bug
		// upstream, not something to block on.
	}
}

type askResultMsg struct {
	ReqID    string `json:"req_id"`
	Decision string `json:"decision"`
	Rule     string `json:"rule"`
}
