package swarm

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type QAEvent struct {
	ID      string `json:"id"`
	Author  string `json:"author"`
	Kind    string `json:"kind"`
	Body    string `json:"body"`
	Message string `json:"message_id,omitempty"`
	Created int64  `json:"created_at"`
}
type QAThread struct {
	ID           string    `json:"id"`
	Package      string    `json:"package"`
	Title        string    `json:"title"`
	Creator      string    `json:"creator"`
	Assignee     string    `json:"assignee"`
	State        string    `json:"state"`
	Blocking     bool      `json:"blocking"`
	Revision     int64     `json:"revision"`
	DecisionRefs []string  `json:"decision_refs"`
	Evidence     string    `json:"evidence,omitempty"`
	Events       []QAEvent `json:"events"`
}
type QAUpdate struct {
	ID           string   `json:"id"`
	Action       string   `json:"action"`
	Package      string   `json:"package,omitempty"`
	Title        string   `json:"title,omitempty"`
	Assignee     string   `json:"assignee,omitempty"`
	Body         string   `json:"body,omitempty"`
	Blocking     *bool    `json:"blocking,omitempty"`
	Expected     *int64   `json:"expected_revision,omitempty"`
	DecisionRefs []string `json:"decision_refs,omitempty"`
	Evidence     string   `json:"evidence,omitempty"`
}

func QA(w *Workspace, id string) *QAThread {
	for i := range w.QA {
		if w.QA[i].ID == id {
			return &w.QA[i]
		}
	}
	return nil
}
func pendingQADecision(w *Workspace, id string) bool {
	for _, msg := range w.Messages {
		if msg.Thread == id && msg.Type == "decision_request" && msg.State == "recorded" {
			return true
		}
	}
	return false
}
func qaEvent(q *QAThread, author, kind, body, message string) error {
	if len(q.Events) >= 1000 {
		return errors.New("QA thread event limit reached")
	}
	q.Events = append(q.Events, QAEvent{ID: ID(), Author: author, Kind: kind, Body: body, Message: message, Created: time.Now().UnixMilli()})
	q.Revision++
	return nil
}

// UpdateQA runs inside the caller's store transaction. Replies append without a
// revision guard; state transitions require a guard and retain attributed history.
func UpdateQA(w *Workspace, m *Member, u QAUpdate) (*QAThread, error) {
	if !ValidProfileName(u.ID) || len(u.Body) > 32768 || len(u.Evidence) > 32768 {
		return nil, errors.New("invalid QA id/body/evidence")
	}
	if len(u.DecisionRefs) > 32 {
		return nil, errors.New("too many decision references")
	}
	for _, r := range u.DecisionRefs {
		if !ValidText(r, 1024) {
			return nil, errors.New("invalid decision reference")
		}
	}
	q := QA(w, u.ID)
	if u.Action == "open" {
		if q != nil {
			return nil, errors.New("QA thread already exists; reply using its ID")
		}
		if len(w.QA) >= 500 || !ValidProfileName(u.Package) || !ValidText(u.Title, 500) || !ValidText(u.Body, 32768) {
			return nil, errors.New("invalid QA thread or thread limit reached")
		}
		assignee := GetMember(w, u.Assignee)
		if assignee == nil || assignee.State == "ended" {
			return nil, errors.New("QA requires an active assignee")
		}
		w.QA = append(w.QA, QAThread{ID: u.ID, Package: u.Package, Title: u.Title, Creator: m.ID, Assignee: assignee.ID, State: "open", DecisionRefs: u.DecisionRefs, Events: []QAEvent{}})
		q = &w.QA[len(w.QA)-1]
		if u.Blocking != nil {
			q.Blocking = *u.Blocking
		}
	} else {
		if q == nil {
			return nil, errors.New("unknown QA thread")
		}
		if u.Action != "reply" {
			if u.Expected == nil || *u.Expected != q.Revision {
				return nil, fmt.Errorf("QA revision conflict: read thread %s (revision %d)", q.ID, q.Revision)
			}
			if m.ID != w.Lead && m.ID != q.Creator && m.ID != q.Assignee && !(m.Role == "reviewer" && m.Package == q.Package) {
				return nil, errors.New("QA transition requires participant or reviewer authority")
			}
		}
		switch u.Action {
		case "reply":
			if q.State == "resolved" {
				return nil, errors.New("reopen the QA thread before replying")
			}
			if !ValidText(u.Body, 32768) {
				return nil, errors.New("QA reply requires body")
			}
			if u.Package != "" || u.Title != "" || u.Assignee != "" || u.Blocking != nil || u.DecisionRefs != nil || u.Evidence != "" {
				return nil, errors.New("reply only appends body; use guarded actions for state changes")
			}
		case "assign":
			next := GetMember(w, u.Assignee)
			if next == nil || next.State == "ended" {
				return nil, errors.New("unknown QA assignee")
			}
			if q.State == "resolved" {
				return nil, errors.New("reopen before assigning")
			}
			q.Assignee = next.ID
			q.State = "open"
		case "block":
			if q.State == "resolved" {
				return nil, errors.New("reopen before blocking")
			}
			q.State = "blocked"
			q.Blocking = true
		case "resolve":
			if pendingQADecision(w, q.ID) {
				return nil, errors.New("QA awaits a human decision")
			}
			if m.ID != w.Lead && !(m.Role == "reviewer" && m.Package == q.Package) {
				return nil, errors.New("QA resolution requires orchestrator or package reviewer")
			}
			if !ValidText(u.Evidence, 32768) {
				return nil, errors.New("QA resolution requires evidence")
			}
			q.State = "resolved"
			q.Blocking = false
			q.Evidence = u.Evidence
		case "reopen":
			if !ValidText(u.Body, 32768) {
				return nil, errors.New("reopen requires a reason")
			}
			q.State = "open"
			q.Evidence = ""
		default:
			return nil, errors.New("invalid QA action")
		}
		if u.Action != "reply" {
			if u.Blocking != nil && u.Action != "resolve" {
				q.Blocking = *u.Blocking
			}
			if u.DecisionRefs != nil {
				q.DecisionRefs = u.DecisionRefs
			}
		}
	}
	if pendingQADecision(w, q.ID) {
		q.State = "awaiting-owner"
	}
	body := u.Body
	if u.Evidence != "" {
		body += "\n\nEvidence: " + u.Evidence
	}
	if u.Action == "assign" {
		body += "\nNext responder: " + q.Assignee
	}
	if err := qaEvent(q, m.ID, u.Action, strings.TrimSpace(body), ""); err != nil {
		return nil, err
	}
	return q, nil
}

// LinkQA appends the actual message text once. The message and QA event are
// committed together, so neither delivery nor concurrent replies lose evidence.
func LinkQA(w *Workspace, id string, msg *Message) error {
	q := QA(w, id)
	if q == nil {
		return errors.New("unknown QA thread")
	}
	if msg.Thread != "" && msg.Thread != id {
		return errors.New("message belongs to a different QA thread")
	}
	for _, e := range q.Events {
		if e.Message == msg.ID {
			return nil
		}
	}
	if q.State == "resolved" {
		return errors.New("reopen the QA thread before sending replies")
	}
	if err := qaEvent(q, msg.Sender, msg.Type, msg.Body, msg.ID); err != nil {
		return err
	}
	msg.Thread = id
	if msg.Type == "decision_request" {
		q.State = "awaiting-owner"
	}
	if msg.Type == "decision_response" {
		q.State = "open"
		if pendingQADecision(w, id) {
			q.State = "awaiting-owner"
		}
	}
	return nil
}
func QAMarkdown(w *Workspace) string {
	var b strings.Builder
	b.WriteString("# Workspace QA\n\nQuestions, decisions and review evidence.\n")
	name := func(id string) string {
		if id == "human" {
			return "Owner"
		}
		if m := GetMember(w, id); m != nil {
			return m.Name
		}
		return id
	}
	clean := func(s string) string {
		return strings.NewReplacer("\n", " ", "\r", " ", "#", "", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	for _, q := range w.QA {
		if b.Len() > 200*1024 {
			b.WriteString("\nView truncated; read complete thread events with workspace_get view=qa and thread_id.\n")
			break
		}
		fmt.Fprintf(&b, "\n## %s · %s — %s\n\nStatus: **%s** · Assigned to: %s · Revision: %d\n", clean(q.Package), q.ID, clean(q.Title), q.State, clean(name(q.Assignee)), q.Revision)
		if q.Blocking {
			b.WriteString("\n**Blocks package work.**\n")
		}
		if len(q.DecisionRefs) > 0 {
			fmt.Fprintf(&b, "\nDecision references: %s\n", strings.Join(q.DecisionRefs, ", "))
		}
		events := q.Events
		if len(events) > 30 {
			b.WriteString("\nEarlier events omitted; read the thread through MCP.\n")
			events = events[len(events)-30:]
		}
		for _, e := range events {
			if b.Len() > 200*1024 {
				break
			}
			kind := map[string]string{"open": "Question", "reply": "Reply", "question": "Question", "answer": "Answer", "assign": "Assigned", "block": "Blocked", "resolve": "Resolved", "reopen": "Reopened", "decision_request": "Owner decision requested", "decision_response": "Owner decision", "progress": "Progress", "instruction": "Instruction"}[e.Kind]
			if kind == "" {
				kind = e.Kind
			}
			fmt.Fprintf(&b, "\n### %s · %s\n\n", clean(name(e.Author)), kind)
			// Quote each line so collaborator text cannot forge attributed headings.
			for _, line := range strings.Split(e.Body, "\n") {
				fmt.Fprintf(&b, "> %s\n", line)
			}
		}
	}
	if len(w.QA) == 0 {
		b.WriteString("\nNo QA threads yet.\n")
	}
	return b.String()
}
