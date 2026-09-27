package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/internal/workspacemcp"
)

type assignmentChange struct {
	Action  string `json:"action"`
	ID      string `json:"id,omitempty"`
	Member  string `json:"member_id,omitempty"`
	Text    string `json:"text,omitempty"`
	Body    string `json:"body,omitempty"`
	Request string `json:"request_id,omitempty"`
	// Node is the plan node the work is on (the member's own by default);
	// Override says why it starts before what the node needs is done.
	Node     string `json:"node,omitempty"`
	Override string `json:"override,omitempty"`
	// CC copies a complete/fail result, as progress (which wakes nobody), to
	// these members: a reviewer's findings reach the implementer they concern
	// without the orchestrator retyping them into the fix assignment.
	CC []string `json:"cc,omitempty"`
}
type messageChange struct {
	Recipient  string          `json:"recipient"`
	Type       string          `json:"type"`
	Body       string          `json:"body"`
	Reply      string          `json:"reply_to,omitempty"`
	Assignment string          `json:"assignment_id,omitempty"`
	Request    string          `json:"request_id,omitempty"`
	Thread     string          `json:"thread_id,omitempty"`
	QA         *swarm.QAUpdate `json:"qa,omitempty"`
}

func resolveMember(s *swarm.Store, session, id string) (string, error) {
	w := s.View(session)
	if w == nil {
		return "", errors.New("no workspace")
	}
	m := swarm.GetMember(w, id)
	if m == nil {
		return "", errors.New("unknown member")
	}
	return m.ID, nil
}
func applyAssignments(s *swarm.Store, h *hosted, updates []assignmentChange) ([]any, error) {
	if len(updates) > 100 {
		return nil, errors.New("maximum 100 assignment changes")
	}
	results := []any{}
	// The batch is atomic (the transaction rolls it back), so a bare store
	// error left the caller guessing which update failed, and three creates
	// for one member read as a conflict with some assignment it could not see.
	created := map[string]int{}
	fail := func(i int, err error) ([]any, error) { return nil, fmt.Errorf("update %d: %w", i, err) }
	for i, u := range updates {
		switch u.Action {
		case "create":
			// Wash names assignments; a caller's id was dropped without a
			// word, and the caller then waited on an id that did not exist.
			if u.ID != "" {
				return fail(i, errors.New("create takes no id: Wash assigns it and returns it; use request_id to make a retry safe"))
			}
			member, err := resolveMember(s, h.sessionID, u.Member)
			if err != nil {
				return fail(i, fmt.Errorf("member %q: %w", u.Member, err))
			}
			a, err := s.Assign(h.sessionID, member, u.Node, u.Override, u.Text, u.Request)
			if err != nil {
				if j, ok := created[member]; ok && errors.Is(err, swarm.ErrActiveAssignment) {
					err = fmt.Errorf("%w: update %d of this batch created it", err, j)
				}
				return fail(i, fmt.Errorf("member %q: %w", u.Member, err))
			}
			created[member] = i
			results = append(results, a)
		case "complete", "fail":
			if len(u.CC) > 8 {
				return fail(i, errors.New("cc: at most 8 members"))
			}
			if err := s.Complete(h.sessionID, u.ID, u.Body, u.Action == "fail"); err != nil {
				return fail(i, err)
			}
			for _, ref := range u.CC {
				member, err := resolveMember(s, h.sessionID, ref)
				if err != nil {
					return fail(i, fmt.Errorf("cc %s: %w", ref, err))
				}
				if _, err := s.Send(h.sessionID, member, "progress", u.Body, "", u.ID, ""); err != nil {
					return fail(i, fmt.Errorf("cc %s: %w", ref, err))
				}
			}
			results = append(results, map[string]string{"id": u.ID, "action": u.Action})
		default:
			return fail(i, errors.New("assignment action must be create, complete or fail"))
		}
	}
	return results, nil
}
func (ws *workspaceService) call(ctx context.Context, h *hosted, c workspacemcp.Call) (any, error) {
	seen := map[string]bool{}
	if ws.store == nil {
		return ws.callOperation(ctx, h, c)
	}
	if w := ws.store.View(h.sessionID); w != nil {
		for _, m := range w.Messages {
			seen[m.ID] = true
		}
	}
	result, err := ws.callOperation(ctx, h, c)
	var options struct {
		Preview bool `json:"preview"`
	}
	_ = json.Unmarshal(c.Arguments, &options)
	if err != nil || options.Preview {
		return result, err
	}
	if c.Name != "workspace_get" && c.Name != "inbox_read" && c.Name != "plan_get" {
		ws.syncQADocuments()
		ws.syncPlanFiles()
	}
	w := ws.store.View(h.sessionID)
	add := func(key string, value any) {
		if object, ok := result.(map[string]any); ok {
			object[key] = value
			return
		}
		encoded, _ := json.Marshal(result)
		var object map[string]any
		if json.Unmarshal(encoded, &object) == nil && object != nil {
			object[key] = value
			result = object
		}
	}
	if w != nil && w.QADir != "" && result != nil {
		// Report file failures separately from a successfully committed QA change.
		add("qa_document_status", ws.qaDocumentStatus(w))
	}
	if nudges := ws.takeNudges(h, seen); len(nudges) > 0 && result != nil {
		add("nudges", nudges)
	}
	return result, nil
}

// takeNudges hands the orchestrator, in the result of its own call, the
// nudges that call caused (a milestone finished, a node left with nobody on
// it), and marks them delivered. Queued, they reached it only after the
// turn in which it had already moved on (live shakedown, step 9 and 10).
func (ws *workspaceService) takeNudges(h *hosted, seen map[string]bool) []string {
	w := ws.store.View(h.sessionID)
	if w == nil {
		return nil
	}
	if self := workspaceMember(w, h.sessionID); self == nil || self.ID != w.Lead {
		return nil
	}
	var ids, bodies []string
	for _, m := range w.Messages {
		if !seen[m.ID] && m.Sender == "wash" && m.Recipient == w.Lead && m.State == "queued" {
			ids = append(ids, m.ID)
			bodies = append(bodies, m.Body)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	_ = ws.store.Mutate(h.sessionID, false, func(w *swarm.Workspace, _ *swarm.Member) error {
		for i := range w.Messages {
			if slices.Contains(ids, w.Messages[i].ID) {
				w.Messages[i].State = "delivered"
			}
		}
		return nil
	})
	return bodies
}
func (ws *workspaceService) callOperation(ctx context.Context, h *hosted, c workspacemcp.Call) (any, error) {
	if h.capability == "reviewer" && !reviewerWorkspaceTool(c.Name) {
		return nil, errors.New("reviewer capability prohibits this workspace operation")
	}
	if err := workspacemcp.ValidateCall(c); err != nil {
		return nil, err
	}
	if len(c.Arguments) == 0 {
		c.Arguments = json.RawMessage(`{}`)
	}
	switch c.Name {
	case "workspace_configure":
		return ws.configureBulk(ctx, h, c.Arguments)
	case "plan_get":
		w := ws.store.View(h.sessionID)
		if w == nil {
			return nil, errors.New("no workspace; call workspace_configure")
		}
		return ws.planGet(w, c.Arguments)
	case "plan_set":
		return ws.planSet(ctx, h, c.Arguments)
	case "plan_accept":
		return ws.planAccept(h, c.Arguments)
	case "member_control":
		var p struct {
			Action  string            `json:"action"`
			Members []string          `json:"member_ids"`
			Node    string            `json:"node,omitempty"`
			Configs map[string]string `json:"configs,omitempty"`
		}
		if err := decodeWorkspace(c.Arguments, &p); err != nil {
			return nil, err
		}
		if !slices.Contains([]string{"pause", "resume", "end", "interrupt", "configure"}, p.Action) {
			return nil, errors.New("invalid member action")
		}
		if (p.Action == "configure") != (len(p.Configs) > 0) {
			return nil, errors.New("configs are required for configure and only for configure")
		}
		w := ws.store.View(h.sessionID)
		if w == nil {
			return nil, errors.New("no workspace")
		}
		if lead := swarm.GetMember(w, w.Lead); lead == nil || lead.Session != h.sessionID {
			return nil, errors.New("orchestrator operation")
		}
		if p.Node != "" {
			if len(p.Members) > 0 {
				return nil, errors.New("select member_ids or node")
			}
			for _, m := range w.Members {
				if m.Node != "" && swarm.Within(w, m.Node, p.Node) && m.State != "ended" {
					p.Members = append(p.Members, m.ID)
				}
			}
		}
		if len(p.Members) == 0 || len(p.Members) > 64 {
			return nil, errors.New("select 1–64 members")
		}
		// Validate the whole target set before any process is stopped.
		ids := []string{}
		for _, ref := range p.Members {
			m := swarm.GetMember(w, ref)
			// The lead may resume itself, and only that: a backend restart
			// pauses the lead with everyone else, and the workspace stays
			// paused (nothing dispatches, nothing launches) until the lead is
			// resumed. Refusing the lead as a target left no way back but
			// ending the workspace — the GUI's Resume button calls this too.
			if m == nil || m.ID == w.Lead && p.Action != "resume" {
				return nil, errors.New("invalid or unauthorized member target")
			}
			if !slices.Contains(ids, m.ID) {
				ids = append(ids, m.ID)
			}
		}
		outcomes := []any{}
		for _, id := range ids {
			var result any
			var err error
			m := swarm.GetMember(ws.store.View(h.sessionID), id)
			if m == nil {
				outcomes = append(outcomes, map[string]string{"member_id": id, "error": "workspace ended"})
				continue
			}
			if p.Action == "configure" {
				result, err = ws.configureMember(ctx, h, id, p.Configs)
			} else if p.Action == "interrupt" {
				result, err = ws.interrupt(id, m)
			} else if p.Action == "resume" && m.Session == "" && m.Key != "" {
				err = ws.store.Mutate(h.sessionID, true, func(w *swarm.Workspace, _ *swarm.Member) error {
					m := swarm.GetMember(w, id)
					if !slices.Contains([]string{"failed", "paused", "pending"}, m.State) {
						return errors.New("member cannot be relaunched")
					}
					m.State = "pending"
					return nil
				})
				if err == nil {
					result, err = ws.spawn(ctx, h, id)
				}
			} else {
				result, err = ws.lifecycle(ctx, h, "member_"+p.Action, id)
			}
			outcome := map[string]any{"member_id": id, "result": result}
			if err != nil {
				outcome["error"] = err.Error()
			}
			outcomes = append(outcomes, outcome)
		}
		return map[string]any{"outcomes": outcomes}, nil
	case "member_update":
		var p struct {
			Status *string `json:"status,omitempty"`
			Emoji  *string `json:"emoji,omitempty"`
			// Handoff is what this member has done and knows, for the
			// member that replaces it (handoff_from).
			Handoff *string `json:"handoff,omitempty"`
			Waiting *struct {
				Reason string   `json:"reason"`
				Reply  string   `json:"reply_to,omitempty"`
				Until  []string `json:"until_assignments,omitempty"`
			} `json:"waiting,omitempty"`
			Results []assignmentChange `json:"assignment_results,omitempty"`
			QA      []swarm.QAUpdate   `json:"qa_updates,omitempty"`
			Request string             `json:"request_id,omitempty"`
		}
		if err := decodeWorkspace(c.Arguments, &p); err != nil {
			return nil, err
		}
		if len(p.QA) > 100 {
			return nil, errors.New("maximum 100 QA updates")
		}
		handoff := ""
		if p.Handoff != nil {
			w := ws.store.View(h.sessionID)
			self := workspaceMember(w, h.sessionID)
			if w == nil || self == nil {
				return nil, errors.New("no workspace")
			}
			if !swarm.ValidText(*p.Handoff, 32768) {
				return nil, errors.New("a handoff is 1–32768 bytes")
			}
			path, err := writeHandoff(w.Root, memberRef(*self), *p.Handoff)
			if err != nil {
				return nil, fmt.Errorf("handoff not written: %w", err)
			}
			handoff = path
		}
		// A reopen appends to the thread's history, so a thread read back
		// as a header only gets its events from its file first.
		reopen := []string{}
		for _, u := range p.QA {
			if u.Action == "reopen" {
				reopen = append(reopen, u.ID)
			}
		}
		if err := ws.unarchiveQA(h.sessionID, reopen); err != nil {
			return nil, err
		}
		return ws.store.Transaction(h.sessionID, c.Name, p.Request, c.Arguments, false, func(s *swarm.Store) (any, error) {
			// A reporting call cannot create an assignment as a side effect.
			for _, u := range p.Results {
				if u.Action != "complete" && u.Action != "fail" {
					return nil, errors.New("assignment_results only accepts complete/fail; create assignments with assignment_update")
				}
			}
			results, err := applyAssignments(s, h, p.Results)
			if err != nil {
				return nil, err
			}
			qas := []swarm.QAThread{}
			err = s.Mutate(h.sessionID, false, func(w *swarm.Workspace, m *swarm.Member) error {
				if p.Status != nil {
					if len(*p.Status) > 500 {
						return errors.New("status too long")
					}
					m.Status = *p.Status
					m.UpdatedAt = time.Now().UnixMilli()
				}
				if p.Emoji != nil {
					if len(*p.Emoji) > 64 {
						return errors.New("emoji too long")
					}
					m.Emoji = *p.Emoji
				}
				for _, u := range p.QA {
					q, err := swarm.UpdateQA(w, m, u)
					if err != nil {
						return err
					}
					if err := notifyQAUpdate(w, m, q, u); err != nil {
						return err
					}
					summary := *q
					summary.Events = nil
					qas = append(qas, summary)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			if p.Waiting != nil {
				if err := s.Mutate(h.sessionID, false, func(w *swarm.Workspace, m *swarm.Member) error {
					if !swarm.ValidText(p.Waiting.Reason, 500) {
						return errors.New("invalid waiting reason")
					}
					if p.Waiting.Reply != "" {
						found := false
						for _, msg := range w.Messages {
							if msg.ID == p.Waiting.Reply && (msg.Sender == m.ID || msg.Recipient == m.ID) {
								found = true
								break
							}
						}
						if !found {
							return errors.New("unknown waiting correlation")
						}
					}
					// A set of your own assignments to wait for as a whole: their
					// results arrive together, in one turn, when the last resolves.
					if len(p.Waiting.Until) > 64 {
						return errors.New("until_assignments: at most 64")
					}
					for _, id := range p.Waiting.Until {
						if !slices.ContainsFunc(w.Assignments, func(a swarm.Assignment) bool { return a.ID == id && a.Assigner == m.ID }) {
							return fmt.Errorf("until_assignments: %s is not an assignment you created", id)
						}
					}
					m.WaitingOn = slices.Clone(p.Waiting.Until)
					m.WaitingFor, m.Waiting = p.Waiting.Reply, p.Waiting.Reason
					// The waiting reason is what the member is doing now; a
					// status from before it is stale, and the team view
					// showed the two disagreeing. A status in the same call
					// stands.
					if p.Status == nil {
						m.Status = ""
					}
					swarm.DeliverLastReport(w, m)
					for i := range w.Assignments {
						if w.Assignments[i].Member == m.ID && w.Assignments[i].State == "active" {
							w.Assignments[i].State = "blocked"
						}
					}
					return nil
				}); err != nil {
					return nil, err
				}
			}
			out := map[string]any{"ok": true, "assignment_results": results, "qa": qas}
			if handoff != "" {
				out["handoff"] = handoff
			}
			if p.Waiting != nil {
				out["instruction"] = "Finish your turn now; Wash wakes you for new messages. Do not poll."
			}
			return out, nil
		})
	case "assignment_update":
		var p struct {
			Updates []assignmentChange `json:"updates"`
			Request string             `json:"request_id,omitempty"`
			// Wait sets the caller waiting on the assignments this call
			// creates, as one set: their results arrive together.
			Wait *struct {
				Reason string `json:"reason"`
			} `json:"wait,omitempty"`
		}
		if err := decodeWorkspace(c.Arguments, &p); err != nil {
			return nil, err
		}
		if len(p.Updates) == 0 {
			return nil, errors.New("updates required")
		}
		return ws.store.Transaction(h.sessionID, c.Name, p.Request, c.Arguments, false, func(s *swarm.Store) (any, error) {
			results, err := applyAssignments(s, h, p.Updates)
			if err != nil || p.Wait == nil {
				return results, err
			}
			var created []string
			for _, r := range results {
				if a, ok := r.(swarm.Assignment); ok {
					created = append(created, a.ID)
				}
			}
			if len(created) == 0 {
				return nil, errors.New("wait needs at least one create in the same call")
			}
			if !swarm.ValidText(p.Wait.Reason, 500) {
				return nil, errors.New("wait needs a reason")
			}
			err = s.Mutate(h.sessionID, false, func(w *swarm.Workspace, m *swarm.Member) error {
				m.WaitingOn, m.Waiting, m.WaitingFor = created, p.Wait.Reason, ""
				m.Status = ""
				swarm.DeliverLastReport(w, m)
				for i := range w.Assignments {
					if w.Assignments[i].Member == m.ID && w.Assignments[i].State == "active" {
						w.Assignments[i].State = "blocked"
					}
				}
				return nil
			})
			return map[string]any{"assignments": results, "waiting_on": created, "instruction": "Finish your turn now; the results arrive together, in one turn. Do not poll."}, err
		})
	case "message_send":
		// A single message and a batch use the same atomic mutation path.
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(c.Arguments, &fields)
		var p struct {
			Messages []messageChange `json:"messages"`
			Request  string          `json:"request_id,omitempty"`
		}
		if fields["messages"] != nil {
			if err := decodeWorkspace(c.Arguments, &p); err != nil {
				return nil, err
			}
		} else {
			var msg messageChange
			if err := decodeWorkspace(c.Arguments, &msg); err != nil {
				return nil, err
			}
			p.Messages = []messageChange{msg}
			p.Request = msg.Request
		}
		if len(p.Messages) < 1 || len(p.Messages) > 100 {
			return nil, errors.New("send 1–100 messages")
		}
		return ws.store.Transaction(h.sessionID, c.Name, p.Request, c.Arguments, false, func(s *swarm.Store) (any, error) {
			out := []swarm.Message{}
			for _, msg := range p.Messages {
				if !slices.Contains([]string{"instruction", "question", "answer", "progress"}, msg.Type) {
					return nil, errors.New("invalid message type")
				}
				if (msg.Thread != "" || msg.QA != nil) && len(msg.Body) > swarm.ReportLimit {
					return nil, fmt.Errorf("a message on a QA thread is at most %d bytes (got %d): put the detail in a file and give its path", swarm.ReportLimit, len(msg.Body))
				}
				err := s.Mutate(h.sessionID, false, func(w *swarm.Workspace, m *swarm.Member) error {
					target := swarm.GetMember(w, msg.Recipient)
					if target == nil {
						return errors.New("unknown recipient")
					}
					if msg.QA != nil {
						if msg.QA.Action != "open" {
							return errors.New("message qa only opens threads; reply using thread_id")
						}
						if msg.Thread != "" && msg.Thread != msg.QA.ID {
							return errors.New("thread ID mismatch")
						}
						u := *msg.QA
						if u.Body != "" && u.Body != msg.Body {
							return errors.New("QA opening body must match message body")
						}
						u.Body = msg.Body
						if u.Assignee == "" {
							u.Assignee = target.ID
						}
						if _, err := swarm.UpdateQA(w, m, u); err != nil {
							return err
						}
						msg.Thread = u.ID
					}
					v, err := swarm.AddMessage(w, m.ID, target.ID, msg.Type, msg.Body, msg.Reply, msg.Assignment, msg.Request)
					if err != nil {
						return err
					}
					if msg.QA != nil {
						q := swarm.QA(w, msg.Thread)
						q.Events[len(q.Events)-1].Message = v.ID
						v.Thread = msg.Thread
					} else if msg.Thread != "" {
						if err := swarm.LinkQA(w, msg.Thread, v); err != nil {
							return err
						}
						// Whoever waits on the thread hears an answer, not
						// only the message's recipient.
						if msg.Type == "answer" {
							if err := swarm.NotifyQA(w, swarm.QA(w, msg.Thread), m.ID, v.Body, target.ID); err != nil {
								return err
							}
						}
					}
					out = append(out, *v)
					return nil
				})
				if err != nil {
					return nil, err
				}
			}
			if fields["messages"] == nil {
				return out[0], nil
			}
			return map[string]any{"messages": out}, nil
		})
	case "decision_request":
		// The owner answers in a panel above the asker's composer; the asker
		// waits: nothing else reaches it until the answers do.
		var p struct {
			Title     string           `json:"title,omitempty"`
			Questions []swarm.Question `json:"questions"`
			Thread    string           `json:"thread_id,omitempty"`
			Request   string           `json:"request_id,omitempty"`
		}
		if err := decodeWorkspace(c.Arguments, &p); err != nil {
			return nil, err
		}
		set := swarm.QuestionSet{Title: p.Title, Questions: p.Questions}
		for i := range set.Questions {
			set.Questions[i].NoText = false
		}
		if err := swarm.ValidateQuestions(&set); err != nil {
			return nil, err
		}
		result, err := ws.store.Transaction(h.sessionID, c.Name, p.Request, c.Arguments, false, func(s *swarm.Store) (any, error) {
			id := ""
			err := s.Mutate(h.sessionID, false, func(w *swarm.Workspace, m *swarm.Member) error {
				msg, err := swarm.AddMessage(w, m.ID, "human", "decision_request", swarm.QuestionsMarkdown(set), "", "", p.Request)
				if err != nil {
					return err
				}
				msg.Questions = &set
				if p.Thread != "" {
					if err := swarm.LinkQA(w, p.Thread, msg); err != nil {
						return err
					}
				}
				about := set.Title
				if about == "" {
					about = set.Questions[0].Question
				}
				m.Waiting = firstLine("Waiting for the owner: "+about, 200)
				id = msg.ID
				return nil
			})
			return map[string]any{"id": id, "instruction": "End your turn now. The owner's answers arrive as your next message; nothing else reaches you until then."}, err
		})
		if err == nil {
			publishQuestions()
			// A retried call answers with its stored receipt.
			var receipt struct {
				ID string `json:"id"`
			}
			encoded, _ := json.Marshal(result)
			_ = json.Unmarshal(encoded, &receipt)
			id := receipt.ID
			for _, q := range ws.decisionQuestions(time.Now()) {
				if q.ID == id {
					notifyQuestion(q)
				}
			}
		}
		return result, err
	default:
		return ws.callCore(h, c)
	}
}

// notifyQAUpdate tells the people on a thread about a change they wait for:
// a reply or a resolution reaches its creator and assignee, and a new
// assignee learns it is the next responder.
func notifyQAUpdate(w *swarm.Workspace, m *swarm.Member, q *swarm.QAThread, u swarm.QAUpdate) error {
	notice := func(text string) string {
		if len(text) > swarm.ReportLimit {
			text = strings.ToValidUTF8(text[:swarm.ReportLimit-len("…")], "") + "…"
		}
		return text
	}
	switch u.Action {
	case "reply":
		return swarm.NotifyQA(w, q, m.ID, notice("QA "+q.ID+": "+u.Body))
	case "resolve":
		return swarm.NotifyQA(w, q, m.ID, notice("QA "+q.ID+" resolved: "+u.Evidence))
	case "assign":
		if q.Assignee == m.ID {
			return nil
		}
		text := "You are the next responder on QA thread " + q.ID + " (" + q.Title + "). Read it with workspace_get view=qa thread_id."
		if u.Body != "" {
			text += " " + u.Body
		}
		msg, err := swarm.AddMessage(w, m.ID, q.Assignee, "question", notice(text), "", "", "")
		if err == nil {
			msg.Thread = q.ID
		}
		return err
	}
	return nil
}
