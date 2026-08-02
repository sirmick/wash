// Interactive approval (docs/AGENT_TERM.md §12, M6).
//
// M3 let a terminal answer a permission request from a static rule table.
// The table is the hard part — nobody writes one up front, and the
// questions arrive while you are somewhere else in the desktop. So when
// the policy has no answer, the terminal asks agentd, agentd puts the
// question in the roster the sidebar is already rendering, and whatever
// the human clicks travels back down the same path.
//
// Three properties this file exists to guarantee:
//
//   - **Nobody home ⇒ no stall.** With no subscribers (headless box, no
//     browser attached) the answer is an immediate `defer`, which is
//     exactly M3's behaviour: the agent's own prompt appears.
//   - **Every path ends in an answer.** Answered, timed out, terminal
//     gone, service restarted — the requester always hears something, or
//     its own deadline fires. Nothing is left hanging.
//   - **Deny is the only thing cheaper than allow.** Everything unknown
//     resolves to `defer` (ask the human in the terminal), never to allow.
package agentd

import (
	"log"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

// askTTL is how long a question waits for a human before it gives up and
// lets the agent's own prompt take over. The terminal waits a little
// longer than this, and the hook helper longer still (§12) — each layer
// bounded by the one outside it.
const askTTL = 30 * time.Second

// maxPendingPerTab caps outstanding questions from one terminal tab. A
// pty-resident process that spams asks gets deferred rather than turning
// the sidebar into a queue of its own making.
const maxPendingPerTab = 3

// Ask is one question waiting for a human. It rides the roster's own
// state push, so the sidebar needs no second subscription.
type Ask struct {
	// ID is agentd's handle for this question; the answer names it.
	ID string `json:"id"`
	// Agent / Tool / Subject are what the human reads: "claude wants to
	// run `git push origin main`".
	Agent   string `json:"agent"`
	Tool    string `json:"tool"`
	Subject string `json:"subject,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Dir     string `json:"dir,omitempty"`
	// SuggestedRule is what "Always allow" would write. Shown ON the
	// button — what you clicked is what gets saved.
	SuggestedRule string `json:"suggested_rule,omitempty"`
	// RowKey ties the question to its roster row (same "<instance>:<chan>"
	// key), so the sidebar can render it against the right agent.
	RowKey       string `json:"row_key"`
	TermInstance string `json:"term_instance"`
	// AgeMS is how long it has been waiting, as of the push.
	AgeMS int64 `json:"age_ms"`
}

// pending is the in-flight question plus the bookkeeping needed to answer
// it. Guarded by svc.Mutate like the roster rows.
type pending struct {
	Ask
	asked time.Time
	// reqID is the terminal's own id for the request; the answer carries
	// it back so the terminal can match its waiting socket handler.
	reqID string
	timer *time.Timer
}

var asks = map[string]*pending{}

// askSeq numbers questions within this process. Ids are opaque to
// everyone else.
var askSeq uint64

// registerAskHandlers installs the M6 verbs on the service bus.
func registerAskHandlers(bus *sdk.Bus, c *sdk.Conn) {
	// agent_ask: a terminal has a request its policy can't answer.
	sdk.HandleFromVoid(bus, "agent_ask", func(conn *sdk.Conn, _ string, req askReq, from wire.Sender) error {
		if from.InstanceID == "" || req.ReqID == "" {
			return nil
		}
		rowKey := rowKey(from.InstanceID, req.ChannelID)

		// Nobody watching ⇒ answer now. This is the difference between
		// "wash asks you" and "wash stalls your agent".
		if svc.SubscriberCount() == 0 {
			return answerTerminal(conn, from.InstanceID, req.ReqID, DecisionDefer, "no desktop")
		}

		now := time.Now()
		var over bool
		svc.Mutate(func(s *State) {
			if countForRow(rowKey) >= maxPendingPerTab {
				over = true
				return
			}
			askSeq++
			id := "ask-" + itoa(askSeq)
			p := &pending{
				Ask: Ask{
					ID:            id,
					Agent:         req.Agent,
					Tool:          req.Tool,
					Subject:       req.Subject,
					Cwd:           req.Cwd,
					Dir:           dirLabel(req.Cwd),
					SuggestedRule: agentpolicy.SuggestRule(req.Tool, req.Subject, req.Cwd),
					RowKey:        rowKey,
					TermInstance:  from.InstanceID,
				},
				asked: now,
				reqID: req.ReqID,
			}
			// Expiry is the safety net for a human who never answers.
			p.timer = time.AfterFunc(askTTL, func() { expireAsk(conn, id) })
			asks[id] = p
			s.Asks = publishAsks(now)
		})
		if over {
			log.Printf("agentd: ask rejected row=%s tool=%s reason=too-many-pending", rowKey, req.Tool)
			return answerTerminal(conn, from.InstanceID, req.ReqID, DecisionDefer, "too many pending")
		}
		log.Printf("agentd: ask row=%s agent=%s tool=%s subject=%q", rowKey, req.Agent, req.Tool, req.Subject)
		return nil
	})

	// agent_answer: the human clicked. Comes from the session BE gateway,
	// which is the desktop speaking for the person in front of it.
	sdk.HandleFromVoid(bus, "agent_answer", func(conn *sdk.Conn, _ string, req answerReq, _ wire.Sender) error {
		var p *pending
		svc.Mutate(func(s *State) {
			p = asks[req.ID]
			if p == nil {
				return
			}
			delete(asks, req.ID)
			s.Asks = publishAsks(time.Now())
		})
		if p == nil {
			// Already expired or answered — the requester has moved on.
			return nil
		}
		if p.timer != nil {
			p.timer.Stop()
		}
		decision := normalizeAnswer(req.Decision)
		rule := ""
		if req.Remember && decision != DecisionDefer {
			// Write the rule the button named, not one we re-derive now.
			rule = req.Rule
			if rule == "" {
				rule = p.SuggestedRule
			}
			if err := agentpolicy.Append(agentpolicy.Path(), rule, decision); err != nil {
				log.Printf("agentd: remember rule=%q: %v", rule, err)
			} else {
				log.Printf("agentd: remembered rule=%q decision=%s", rule, decision)
			}
		}
		log.Printf("agentd: answer id=%s tool=%s decision=%s remember=%v", req.ID, p.Tool, decision, req.Remember)
		return answerTerminal(conn, p.TermInstance, p.reqID, decision, "desktop")
	})
}

// expireAsk resolves a question nobody answered in time.
func expireAsk(conn *sdk.Conn, id string) {
	var p *pending
	svc.Mutate(func(s *State) {
		p = asks[id]
		if p == nil {
			return
		}
		delete(asks, id)
		s.Asks = publishAsks(time.Now())
	})
	if p == nil {
		return
	}
	log.Printf("agentd: ask expired id=%s tool=%s after=%s", id, p.Tool, askTTL)
	_ = answerTerminal(conn, p.TermInstance, p.reqID, DecisionDefer, "timeout")
}

// answerTerminal sends the verdict back to the terminal that asked.
func answerTerminal(conn *sdk.Conn, instance, reqID, decision, why string) error {
	return conn.SendAppMsgTo(wire.Recipient{InstanceID: instance}, map[string]any{
		"kind":     "ask_result",
		"req_id":   reqID,
		"decision": decision,
		"rule":     why,
	})
}

// publishAsks renders the pending list for the sidebar, oldest first —
// the person waiting longest is the one to answer first. Called inside
// Mutate; builds a fresh slice (copy-on-write, per the race gate).
func publishAsks(now time.Time) []Ask {
	out := make([]Ask, 0, len(asks))
	for _, p := range asks {
		a := p.Ask
		a.AgeMS = elapsedMS(p.asked, now)
		out = append(out, a)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].AgeMS > out[j-1].AgeMS; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func countForRow(rowKey string) int {
	n := 0
	for _, p := range asks {
		if p.RowKey == rowKey {
			n++
		}
	}
	return n
}

// Decisions agentd can return. Defer means "wash has no answer" — the
// agent's own prompt appears, which is the fallback for every failure.
const (
	DecisionAllow = agentpolicy.DecisionAllow
	DecisionDeny  = agentpolicy.DecisionDeny
	// Defer is agentd's own: "wash has no answer", which the helper turns
	// into silence and the agent into its own prompt.
	DecisionDefer = "defer"
)

// normalizeAnswer maps anything unrecognized to defer. A malformed answer
// must never become an allow.
func normalizeAnswer(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case DecisionAllow:
		return DecisionAllow
	case DecisionDeny:
		return DecisionDeny
	}
	return DecisionDefer
}

type askReq struct {
	ReqID     string `json:"req_id"`
	ChannelID uint64 `json:"channel_id"`
	Agent     string `json:"agent"`
	Tool      string `json:"tool"`
	Subject   string `json:"subject"`
	Cwd       string `json:"cwd"`
}

type answerReq struct {
	ID       string `json:"id"`
	Decision string `json:"decision"`
	Remember bool   `json:"remember"`
	Rule     string `json:"rule"`
}
