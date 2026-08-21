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
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// askTTL is how long a question waits for a human before it gives up and
// lets the agent's own prompt take over. The terminal waits a little
// longer than this, and the hook helper longer still (§12) — each layer
// bounded by the one outside it.
//
// It is *desktop time*, not wall time. A question asked thirty seconds
// before someone closed the lid must not be denied while they are away:
// the clock only runs when there is a desktop that could plausibly answer
// (see expireAsk). Without that, a wall-clock fuse guarantees the
// questions asked at the worst moment are the ones nobody ever sees.
const askTTL = 30 * time.Second

// askFirstRunTTL is askTTL for a user who has no rules yet.
//
// With no ~/.config/wash/agents.json, agentpolicy matches nothing and
// *every* tool call asks a human — which is exactly when a thirty-second
// budget per question is least survivable, and when the person answering
// has the least practice at it. Rules only accumulate as they click
// "always", so friction is highest on the first session and falls from
// there; that is the wrong direction for a first impression. The window
// tightens to askTTL as soon as they have taught it anything at all.
//
// This is a timing change only. It deliberately does not seed a policy:
// what wash may do unasked is the user's decision to make, not a default
// to inherit.
const askFirstRunTTL = 3 * time.Minute

// askHardTTL is the wall-clock ceiling that survives the pause above.
// Something must be finite: RequestPermission blocks the agent for the
// whole duration, so an ask that waits forever wedges the turn instead of
// failing it. This is the "high but finite" end of that trade — long
// enough to cover a lunch break or an overnight disconnect, short enough
// that a forgotten session eventually lets go.
const askHardTTL = 30 * time.Minute

// Why an ask resolved without a human. These are the `why` half of
// replyFn, and they exist because the three producers of DecisionDefer
// mean genuinely different things: nobody was home, this row is shouting,
// or the human had their chance and did not take it. Collapsing them is
// what made "auto-reject everything" look like a policy rather than a
// timer.
const (
	ReasonNoDesktop = "no desktop"
	ReasonTooMany   = "too many pending"
	ReasonTimeout   = "timeout"
	ReasonDesktop   = "desktop"
)

// maxPendingPerRow caps outstanding questions from one roster row. A
// process that spams asks gets deferred rather than turning the sidebar
// into a queue of its own making.
const maxPendingPerRow = 3

// replyFn delivers a verdict to whoever asked. The queue deliberately does
// not know whether that is a terminal across the router or a session this
// process is hosting itself — it only knows how to call back. Every
// producer supplies its own route at enqueue time (docs/AGENT_APP.md §4).
type replyFn func(decision, why string) error

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
	// key for terminals; hosted sessions mint their own), so the sidebar
	// can render it against the right agent.
	RowKey string `json:"row_key"`
	// SourceApp / SourceInstance name the producer, for display and
	// attribution only. The reply route is the closure on `pending`, never
	// these — a question from a session agentd hosts itself has no
	// instance to message.
	SourceApp      string `json:"source_app,omitempty"`
	SourceInstance string `json:"source_instance,omitempty"`
	// AgeMS is how long it has been waiting, as of the push.
	AgeMS int64 `json:"age_ms"`
}

// pending is the in-flight question plus the bookkeeping needed to answer
// it. Guarded by svc.Mutate like the roster rows.
type pending struct {
	Ask
	asked time.Time
	// reply is how this particular requester hears the verdict.
	reply replyFn
	timer *time.Timer
	// softTTL is this question's own askTTL — longer on a machine with no
	// rules yet. Held per-ask so a re-arm uses the same window the
	// question started with rather than whatever the policy says now.
	softTTL time.Duration
	// hardDeadline is the wall-clock ceiling. The soft askTTL timer
	// re-arms across periods with no desktop attached; this one does not
	// move, so a question cannot outlive the turn it is blocking.
	hardDeadline time.Time
}

// askSpec is what a producer knows about a question before the queue gives
// it an id.
type askSpec struct {
	Agent, Tool, Subject, Cwd string
	RowKey                    string
	SourceApp, SourceInstance string
}

var asks = map[string]*pending{}

// The queue's two touches on the state service, indirected so its decision
// logic — nobody-home, the per-row cap, and the promise that every path
// answers exactly once — is unit-testable without a live StateService
// (which needs a Bus, which needs a Conn).
var (
	stateSubscribers = func() int { return svc.SubscriberCount() }
	mutateState      = func(fn func(*State)) { svc.Mutate(fn) }
	// policyRuleCount is how many rules the human has taught wash. One
	// stat per question, the same price the hosted tier already pays to
	// read the policy fresh (acp.go), and for the same reason: a rule
	// written thirty seconds ago should count.
	policyRuleCount = func() int { return len(agentpolicy.Load(agentpolicy.Path()).Rules) }
)

// softTTLNow picks the window for a question about to be asked.
func softTTLNow() time.Duration {
	if policyRuleCount() == 0 {
		return askFirstRunTTL
	}
	return askTTL
}

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
		enqueueAsk(askSpec{
			Agent:          req.Agent,
			Tool:           req.Tool,
			Subject:        req.Subject,
			Cwd:            req.Cwd,
			RowKey:         rowKey(from.InstanceID, req.ChannelID),
			SourceApp:      "com.wash.term",
			SourceInstance: from.InstanceID,
		}, replyToInstance(conn, from.InstanceID, req.ReqID))
		return nil
	})

	// agent_answer: the human clicked. Comes from the session BE gateway,
	// which is the desktop speaking for the person in front of it.
	sdk.HandleFromVoid(bus, "agent_answer", func(conn *sdk.Conn, _ string, req answerReq, _ wire.Sender) error {
		var p *pending
		mutateState(func(s *State) {
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
		return p.reply(decision, ReasonDesktop)
	})
}

// enqueueAsk puts a question in front of the human. It returns false when
// it could not — nobody watching, or this row is already at its cap — in
// which case `reply` has *already* been called with a defer. Every path
// out of this function has answered the requester exactly once.
func enqueueAsk(spec askSpec, reply replyFn) bool {
	// Nobody watching ⇒ answer now. This is the difference between
	// "wash asks you" and "wash stalls your agent".
	if stateSubscribers() == 0 {
		_ = reply(DecisionDefer, ReasonNoDesktop)
		return false
	}

	now := time.Now()
	soft := softTTLNow()
	var over bool
	mutateState(func(s *State) {
		if countForRow(spec.RowKey) >= maxPendingPerRow {
			over = true
			return
		}
		askSeq++
		id := "ask-" + itoa(askSeq)
		p := &pending{
			Ask: Ask{
				ID:             id,
				Agent:          spec.Agent,
				Tool:           spec.Tool,
				Subject:        spec.Subject,
				Cwd:            spec.Cwd,
				Dir:            dirLabel(spec.Cwd),
				SuggestedRule:  agentpolicy.SuggestRule(spec.Tool, spec.Subject, spec.Cwd),
				RowKey:         spec.RowKey,
				SourceApp:      spec.SourceApp,
				SourceInstance: spec.SourceInstance,
			},
			asked:        now,
			reply:        reply,
			softTTL:      soft,
			hardDeadline: now.Add(askHardTTL),
		}
		// Expiry is the safety net for a human who never answers. It is a
		// safety net, not a deadline: expireAsk re-arms it for as long as
		// there is nobody who could have answered.
		p.timer = time.AfterFunc(soft, func() { expireAsk(id) })
		asks[id] = p
		s.Asks = publishAsks(now)
	})
	if over {
		log.Printf("agentd: ask rejected row=%s tool=%s reason=too-many-pending", spec.RowKey, spec.Tool)
		_ = reply(DecisionDefer, ReasonTooMany)
		return false
	}
	log.Printf("agentd: ask row=%s agent=%s tool=%s subject=%q ttl=%s", spec.RowKey, spec.Agent, spec.Tool, spec.Subject, soft)
	return true
}

// expireAsk resolves a question nobody answered in time — or, if nobody
// *could* have answered it, gives it another askTTL to be seen.
//
// The pause is the whole point. A wall-clock fuse denies the questions
// asked while the user is away, which is precisely when they are least
// able to answer and most likely to care: on 2026-08-20 ten consecutive
// asks expired unanswered because the lid was shut for the entire window.
// Subscriber count is the right signal rather than a per-row one, because
// the sidebar rail renders every row's asks — one attached desktop can
// answer any of them.
//
// hardDeadline is the floor under the pause: past it the ask resolves
// whether or not anyone was watching, so a blocked agent turn is bounded.
func expireAsk(id string) {
	var p *pending
	var paused bool
	now := time.Now()
	// Read the subscriber count BEFORE entering Mutate: fn must not call
	// back into the state service, which takes the same lock and would
	// deadlock (pkg/sdk/stateservice.go). A count one instant stale is
	// harmless here — the next timer window re-reads it.
	watching := stateSubscribers()
	mutateState(func(s *State) {
		p = asks[id]
		if p == nil {
			return
		}
		if p.timer != nil && watching == 0 && now.Before(p.hardDeadline) {
			// Nobody could have seen this. Give it another window and
			// republish, so the age the sidebar shows stays honest.
			paused = true
			p.timer.Reset(p.softTTL)
			s.Asks = publishAsks(now)
			return
		}
		delete(asks, id)
		s.Asks = publishAsks(now)
	})
	if p == nil {
		return
	}
	if paused {
		log.Printf("agentd: ask waiting id=%s tool=%s age=%s reason=no-desktop hard_deadline_in=%s",
			id, p.Tool, now.Sub(p.asked).Round(time.Second), p.hardDeadline.Sub(now).Round(time.Second))
		return
	}
	log.Printf("agentd: ask expired id=%s row=%s tool=%s after=%s age=%s subscribers=%d",
		id, p.RowKey, p.Tool, p.softTTL, now.Sub(p.asked).Round(time.Second), watching)
	_ = p.reply(DecisionDefer, ReasonTimeout)
}

// replyToInstance is the reply route for a question that arrived over the
// router: an ask_result app_msg back to the instance that asked. The
// terminal tier's route today; any future app that asks on someone's
// behalf uses the same one.
func replyToInstance(conn *sdk.Conn, instance, reqID string) replyFn {
	return func(decision, why string) error {
		return conn.SendAppMsgTo(wire.Recipient{InstanceID: instance}, map[string]any{
			"kind":     "ask_result",
			"req_id":   reqID,
			"decision": decision,
			"rule":     why,
		})
	}
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
