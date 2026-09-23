package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
	for _, u := range updates {
		switch u.Action {
		case "create":
			member, err := resolveMember(s, h.sessionID, u.Member)
			if err != nil {
				return nil, err
			}
			a, err := s.Assign(h.sessionID, member, u.Text, u.Request)
			if err != nil {
				return nil, err
			}
			results = append(results, a)
		case "complete", "fail":
			if err := s.Complete(h.sessionID, u.ID, u.Body, u.Action == "fail"); err != nil {
				return nil, err
			}
			results = append(results, map[string]string{"id": u.ID, "action": u.Action})
		default:
			return nil, errors.New("assignment action must be create, complete or fail")
		}
	}
	return results, nil
}
func (ws *workspaceService) call(ctx context.Context, h *hosted, c workspacemcp.Call) (any, error) {
	if err := workspacemcp.ValidateCall(c); err != nil {
		return nil, err
	}
	if len(c.Arguments) == 0 {
		c.Arguments = json.RawMessage(`{}`)
	}
	switch c.Name {
	case "workspace_configure":
		return ws.configureBulk(ctx, h, c.Arguments)
	case "workspace_end":
		return legacyCall(ctx, ws, h, "teardown_workspace", map[string]any{})
	case "member_control":
		var p struct {
			Action  string   `json:"action"`
			Members []string `json:"member_ids"`
			Package string   `json:"package,omitempty"`
		}
		if err := decodeWorkspace(c.Arguments, &p); err != nil {
			return nil, err
		}
		if !slices.Contains([]string{"pause", "resume", "end"}, p.Action) {
			return nil, errors.New("invalid member action")
		}
		w := ws.store.View(h.sessionID)
		if w == nil {
			return nil, errors.New("no workspace")
		}
		if p.Package != "" {
			if len(p.Members) > 0 {
				return nil, errors.New("select member_ids or package")
			}
			for _, m := range w.Members {
				if m.Package == p.Package && m.State != "ended" {
					p.Members = append(p.Members, m.ID)
				}
			}
		}
		if len(p.Members) == 0 || len(p.Members) > 64 {
			return nil, errors.New("select 1–64 members")
		}
		// Validate the whole target set and authority before any process is stopped.
		self := ""
		for _, m := range w.Members {
			if m.Session == h.sessionID {
				self = m.ID
			}
		}
		ids := []string{}
		for _, ref := range p.Members {
			m := swarm.GetMember(w, ref)
			if m == nil || m.ID == w.Lead || self != w.Lead && m.Creator != self || p.Action == "end" && self != w.Lead {
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
			if p.Action == "resume" && m.Session == "" && m.Key != "" {
				err = ws.store.Mutate(h.sessionID, true, func(w *swarm.Workspace, _ *swarm.Member) error {
					m := swarm.GetMember(w, id)
					if !slices.Contains([]string{"failed", "paused", "pending"}, m.State) {
						return errors.New("member cannot be relaunched")
					}
					m.State = "pending"
					w.State = "active"
					return nil
				})
				if err == nil {
					result, err = ws.spawn(ctx, h, workspaceArgs{Reserved: id})
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
			Status  *string `json:"status,omitempty"`
			Emoji   *string `json:"emoji,omitempty"`
			Waiting *struct {
				Reason string `json:"reason"`
				Reply  string `json:"reply_to,omitempty"`
			} `json:"waiting,omitempty"`
			Acknowledge []string           `json:"acknowledge,omitempty"`
			Results     []assignmentChange `json:"assignment_results,omitempty"`
			QA          []swarm.QAUpdate   `json:"qa_updates,omitempty"`
			Request     string             `json:"request_id,omitempty"`
		}
		if err := decodeWorkspace(c.Arguments, &p); err != nil {
			return nil, err
		}
		if len(p.QA) > 100 || len(p.Acknowledge) > 100 {
			return nil, errors.New("maximum 100 QA updates/acknowledgments")
		}
		return ws.store.Transaction(h.sessionID, c.Name, p.Request, c.Arguments, false, func(s *swarm.Store) (any, error) {
			for _, id := range p.Acknowledge {
				if err := s.Acknowledge(h.sessionID, id); err != nil {
					return nil, err
				}
			}
			// A reporting call cannot create an assignment as a side effect.
			for _, u := range p.Results {
				if u.Action != "complete" && u.Action != "fail" {
					return nil, errors.New("assignment_results only accepts complete/fail")
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
				if _, err := legacyCall(ctx, &workspaceService{store: s}, h, "member_wait", map[string]any{"reason": p.Waiting.Reason, "reply_to": p.Waiting.Reply}); err != nil {
					return nil, err
				}
			}
			out := map[string]any{"ok": true, "assignment_results": results, "qa": qas}
			if p.Waiting != nil {
				out["instruction"] = "Finish your turn now; Wash wakes you for new messages. Do not poll."
			}
			return out, nil
		})
	case "inbox_ack":
		var p struct {
			IDs     []string `json:"ids"`
			Request string   `json:"request_id,omitempty"`
		}
		if err := decodeWorkspace(c.Arguments, &p); err != nil {
			return nil, err
		}
		if len(p.IDs) < 1 || len(p.IDs) > 100 {
			return nil, errors.New("acknowledge 1–100 IDs")
		}
		return ws.store.Transaction(h.sessionID, c.Name, p.Request, c.Arguments, false, func(s *swarm.Store) (any, error) {
			for _, id := range p.IDs {
				if err := s.Acknowledge(h.sessionID, id); err != nil {
					return nil, err
				}
			}
			return map[string]any{"acknowledged": p.IDs}, nil
		})
	case "assignment_update":
		var p struct {
			Updates []assignmentChange `json:"updates"`
			Request string             `json:"request_id,omitempty"`
		}
		if err := decodeWorkspace(c.Arguments, &p); err != nil {
			return nil, err
		}
		if len(p.Updates) == 0 {
			return nil, errors.New("updates required")
		}
		return ws.store.Transaction(h.sessionID, c.Name, p.Request, c.Arguments, false, func(s *swarm.Store) (any, error) { return applyAssignments(s, h, p.Updates) })
	case "message_send":
		// Preserve the v1 single-message shape for resumed conversations.
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
		var p struct {
			Text    string `json:"text"`
			Thread  string `json:"thread_id,omitempty"`
			Request string `json:"request_id,omitempty"`
		}
		if err := decodeWorkspace(c.Arguments, &p); err != nil {
			return nil, err
		}
		created := false
		result, err := ws.store.Transaction(h.sessionID, c.Name, p.Request, c.Arguments, false, func(s *swarm.Store) (any, error) {
			created = true
			id := ""
			err := s.Mutate(h.sessionID, false, func(w *swarm.Workspace, m *swarm.Member) error {
				msg, err := swarm.AddMessage(w, m.ID, "human", "decision_request", p.Text, "", "", p.Request)
				if err != nil {
					return err
				}
				if p.Thread != "" {
					if err := swarm.LinkQA(w, p.Thread, msg); err != nil {
						return err
					}
				}
				id = msg.ID
				return nil
			})
			return map[string]any{"id": id}, err
		})
		if err == nil && created && ws.conn != nil {
			ws.conn.NotifyAbout(h.key, "Workspace decision", p.Text, "info")
		}
		return result, err
	default:
		return ws.callLegacy(ctx, h, c)
	}
}
