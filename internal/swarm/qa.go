package swarm

import (
	"errors"
	"fmt"
	"io"
	"slices"
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
	ID string `json:"id"`
	// Node is the plan node the thread is about.
	Node         string   `json:"node"`
	Title        string   `json:"title"`
	Creator      string   `json:"creator"`
	Assignee     string   `json:"assignee"`
	State        string   `json:"state"`
	Blocking     bool     `json:"blocking"`
	Revision     int64    `json:"revision"`
	DecisionRefs []string `json:"decision_refs"`
	Evidence     string   `json:"evidence,omitempty"`
	// Participants are members the thread was opened on behalf of: an
	// implementer whose question the orchestrator framed for the Architect
	// hears the answer itself, as anyone who has written on the thread does.
	Participants []string `json:"participants,omitempty"`
	// Resumed marks a thread resolved in an earlier workspace and read
	// back from its QA file: its evidence is about that workspace's code.
	Resumed bool `json:"resumed,omitempty"`
	// Archived marks a resolved thread read back as a header only: its
	// events stay in its file until something needs them.
	Archived bool      `json:"archived,omitempty"`
	Events   []QAEvent `json:"events"`
}
type QAUpdate struct {
	ID           string   `json:"id"`
	Action       string   `json:"action"`
	Node         string   `json:"node,omitempty"`
	Title        string   `json:"title,omitempty"`
	Assignee     string   `json:"assignee,omitempty"`
	Body         string   `json:"body,omitempty"`
	Blocking     *bool    `json:"blocking,omitempty"`
	Expected     *int64   `json:"expected_revision,omitempty"`
	DecisionRefs []string `json:"decision_refs,omitempty"`
	Evidence     string   `json:"evidence,omitempty"`
	// OnBehalfOf, on open, names the member whose question this is.
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
}

// nodeReviewer is a reviewer on the thread's node or a node it sits inside.
func nodeReviewer(w *Workspace, m *Member, q *QAThread) bool {
	return m.Role == "reviewer" && m.Node != "" && Within(w, q.Node, m.Node)
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

// qaDetail is what a QA body limit says: a thread is re-read by everyone on
// it, and DOC1's QA file reached 2 MB because members pasted whole plans and
// review reports into threads. The detail belongs in a file under version
// control; the thread holds the pointer.
func qaDetail(what string, n int) error {
	return fmt.Errorf("QA %s is at most %d bytes (got %d): put the detail in a file and give its path here", what, ReportLimit, n)
}

// UpdateQA runs inside the caller's store transaction. Replies append without a
// revision guard; state transitions require a guard and retain attributed history.
func UpdateQA(w *Workspace, m *Member, u QAUpdate) (*QAThread, error) {
	if !ValidProfileName(u.ID) {
		return nil, errors.New("QA thread id must be 1–80 letters, digits, - or _")
	}
	if len(u.Body) > ReportLimit {
		return nil, qaDetail("body", len(u.Body))
	}
	if len(u.Evidence) > ReportLimit {
		return nil, qaDetail("evidence", len(u.Evidence))
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
		switch {
		case len(w.QA) >= 500:
			return nil, errors.New("QA thread limit (500) reached")
		case PlanNode(w, u.Node) == nil:
			return nil, fmt.Errorf("opening a QA thread needs node: a plan node it is about (%q is not one)", u.Node)
		case !ValidText(u.Title, 500):
			return nil, errors.New("opening a QA thread needs a title of at most 500 bytes")
		case !ValidText(u.Body, ReportLimit):
			return nil, errors.New("opening a QA thread needs a body: the question")
		}
		assignee := GetMember(w, u.Assignee)
		if assignee == nil || assignee.State == "ended" {
			return nil, errors.New("QA requires an active assignee")
		}
		var participants []string
		if u.OnBehalfOf != "" {
			p := GetMember(w, u.OnBehalfOf)
			if p == nil || p.State == "ended" {
				return nil, fmt.Errorf("on_behalf_of %q is not an active member", u.OnBehalfOf)
			}
			if p.ID != m.ID && p.ID != assignee.ID {
				participants = []string{p.ID}
			}
		}
		w.QA = append(w.QA, QAThread{ID: u.ID, Node: u.Node, Title: u.Title, Creator: m.ID, Assignee: assignee.ID, State: "open", DecisionRefs: u.DecisionRefs, Participants: participants, Events: []QAEvent{}})
		q = &w.QA[len(w.QA)-1]
		if u.Blocking != nil {
			q.Blocking = *u.Blocking
		}
	} else {
		if q == nil {
			return nil, errors.New("unknown QA thread")
		}
		if u.OnBehalfOf != "" {
			return nil, errors.New("on_behalf_of is set when a thread opens")
		}
		if u.Action != "reply" {
			if u.Expected == nil || *u.Expected != q.Revision {
				return nil, fmt.Errorf("QA revision conflict: read thread %s (revision %d)", q.ID, q.Revision)
			}
			if m.ID != w.Lead && m.ID != q.Creator && m.ID != q.Assignee && !nodeReviewer(w, m, q) {
				return nil, fmt.Errorf("QA thread %s is node %s's: only the orchestrator, the thread's creator or assignee, or a reviewer on node %s may %s it", q.ID, q.Node, q.Node, u.Action)
			}
		}
		switch u.Action {
		case "reply":
			if q.State == "resolved" {
				return nil, errors.New("reopen the QA thread before replying")
			}
			if strings.TrimSpace(u.Body) == "" {
				return nil, errors.New("QA reply requires body")
			}
			if u.Node != "" || u.Title != "" || u.Assignee != "" || u.Blocking != nil || u.DecisionRefs != nil || u.Evidence != "" {
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
			if m.ID != w.Lead && !nodeReviewer(w, m, q) {
				return nil, fmt.Errorf("QA thread %s is node %s's: only the orchestrator or a reviewer on node %s may resolve it; reply with your verdict instead", q.ID, q.Node, q.Node)
			}
			if strings.TrimSpace(u.Evidence) == "" {
				return nil, errors.New("QA resolution requires evidence")
			}
			q.State = "resolved"
			q.Blocking = false
			q.Evidence = u.Evidence
		case "reopen":
			if strings.TrimSpace(u.Body) == "" {
				return nil, errors.New("reopen requires a reason")
			}
			if q.Archived {
				return nil, fmt.Errorf("QA thread %s was read back as a header only; Wash loads its events before a reopen", q.ID)
			}
			// A reopened question is the orchestrator's until it assigns
			// it again: left with its assignee, a thread resumed from an
			// earlier workspace stayed with a member that no longer runs.
			q.State = "open"
			q.Evidence = ""
			q.Assignee = w.Lead
			q.Resumed = false
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
		body += "\nNext responder: " + GetMember(w, q.Assignee).Name
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

// WithdrawDecision cancels a pending decision request. Its QA thread returns
// to open unless another decision is still pending on it: left alone, the
// thread of an ended member's question stayed awaiting-owner, asking the
// human about a question nobody would read the answer to.
func WithdrawDecision(w *Workspace, msg *Message) {
	msg.State = "cancelled"
	if q := QA(w, msg.Thread); q != nil && q.State == "awaiting-owner" && !pendingQADecision(w, q.ID) {
		q.State = "open"
	}
}

// Audience is who hears what is added to a thread: its creator and assignee,
// the members it was opened on behalf of, and everyone who has written on
// it. Creator and assignee alone meant a question the orchestrator framed
// for the Architect on an implementer's behalf was answered to the
// orchestrator only, which relayed every ruling by hand (Redoubt, eight
// times in a day).
func Audience(q *QAThread) []string {
	out := append([]string{q.Creator, q.Assignee}, q.Participants...)
	for _, e := range q.Events {
		out = append(out, e.Author)
	}
	return out
}

// NotifyQA tells a thread's audience what author just added to it, as an
// answer (which wakes an idle member). A ruling that reached a blocked member
// only as a thread event, or a copy that does not wake, left it asleep. skip
// names members who already receive the text another way.
func NotifyQA(w *Workspace, q *QAThread, author, body string, skip ...string) error {
	told := append([]string{author, "human"}, skip...)
	for _, id := range Audience(q) {
		m := GetMember(w, id)
		if m == nil || m.State == "ended" || slices.Contains(told, m.ID) {
			continue
		}
		told = append(told, m.ID)
		msg, err := AddMessage(w, author, m.ID, "answer", body, "", "", "")
		if err != nil {
			return err
		}
		msg.Thread = q.ID
	}
	return nil
}

// QAMarkdown is the bounded QA view the window shows: every thread's header,
// and the latest events of the threads whose events are loaded.
func QAMarkdown(w *Workspace) string {
	var b strings.Builder
	name := qaNames(w)
	b.WriteString("# Workspace QA\n\nQuestions, decisions and review evidence.\n")
	for _, q := range w.QA {
		if b.Len() > 200*1024 {
			b.WriteString("\nView truncated; read complete thread events with workspace_get view=qa and thread_id.\n")
			break
		}
		_ = WriteQAThread(&b, q, name, true)
	}
	if len(w.QA) == 0 {
		b.WriteString("\nNo QA threads yet.\n")
	}
	return b.String()
}

// qaNames names a thread author: a member, the owner, or an author from an
// earlier workspace's files.
func qaNames(w *Workspace) func(string) string {
	return func(id string) string {
		if id == "human" {
			return "Owner"
		}
		if m := GetMember(w, id); m != nil && m.Name != "" {
			return m.Name
		}
		if name := w.QAAuthors[id]; name != "" {
			return name
		}
		return id
	}
}

type qaWriter struct {
	out  io.Writer
	size int
	err  error
}

func (b *qaWriter) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	n, err := b.out.Write(p)
	b.size += n
	b.err = err
	return n, err
}
func (b *qaWriter) WriteString(s string) { _, _ = io.WriteString(b, s) }

// WriteQAThread writes one thread as Markdown: its heading, status and every
// event, quoted so collaborator text cannot forge an attributed heading.
// bounded keeps the last 30 events and leaves out event IDs and times.
func WriteQAThread(out io.Writer, q QAThread, name func(string) string, bounded bool) error {
	b := &qaWriter{out: out}
	clean := func(s string) string {
		return strings.NewReplacer("\n", " ", "\r", " ", "#", "", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	fmt.Fprintf(b, "\n## %s · %s — %s\n\nStatus: **%s** · Assigned to: %s · Revision: %d\n", clean(q.Node), q.ID, clean(q.Title), q.State, clean(name(q.Assignee)), q.Revision)
	if q.Resumed {
		b.WriteString("\n*Resolved in an earlier workspace; reopen it if the code has changed since.*\n")
	}
	if q.Blocking {
		b.WriteString("\n**Blocks work on its node.**\n")
	}
	if len(q.Participants) > 0 {
		names := make([]string, len(q.Participants))
		for i, id := range q.Participants {
			names[i] = clean(name(id))
		}
		fmt.Fprintf(b, "\nOn behalf of: %s\n", strings.Join(names, ", "))
	}
	if len(q.DecisionRefs) > 0 {
		fmt.Fprintf(b, "\nDecision references: %s\n", strings.Join(q.DecisionRefs, ", "))
	}
	if q.Evidence != "" && q.State == "resolved" {
		fmt.Fprintf(b, "\nEvidence: %s\n", clean(q.Evidence))
	}
	if q.Archived {
		b.WriteString("\nEvents are in the thread's file; read them with workspace_get view=qa and thread_id.\n")
		return b.err
	}
	events := q.Events
	if bounded && len(events) > 30 {
		b.WriteString("\nEarlier events omitted; read the thread through MCP.\n")
		events = events[len(events)-30:]
	}
	for _, e := range events {
		kind := map[string]string{"open": "Question", "reply": "Reply", "question": "Question", "answer": "Answer", "assign": "Assigned", "block": "Blocked", "resolve": "Resolved", "reopen": "Reopened", "decision_request": "Owner decision requested", "decision_response": "Owner decision", "progress": "Progress", "instruction": "Instruction"}[e.Kind]
		if kind == "" {
			kind = e.Kind
		}
		fmt.Fprintf(b, "\n### %s · %s\n\n", clean(name(e.Author)), kind)
		if !bounded {
			fmt.Fprintf(b, "Event: `%s` · %s\n\n", e.ID, time.UnixMilli(e.Created).UTC().Format(time.RFC3339))
		}
		for _, line := range strings.Split(e.Body, "\n") {
			fmt.Fprintf(b, "> %s\n", line)
		}
	}
	return b.err
}

// ClaimFiles takes the QA directory and plan file over from ended
// workspaces, which otherwise keep retrying their last writes into them. The
// caller runs this inside configuration's staged transaction.
func (s *Store) ClaimFiles(session string) error {
	return s.change(func(st *State) error {
		w, _ := find(st, session)
		if w == nil {
			return nil
		}
		for i := range st.Workspaces {
			old := &st.Workspaces[i]
			if old.ID == w.ID {
				continue
			}
			if w.QADir != "" && old.QADir == w.QADir {
				if old.State != "ended" {
					return fmt.Errorf("QA directory belongs to active workspace %s (%q); if its orchestrator is not running, end it with workspace_end {\"workspace_id\":%q}", old.ID, old.Name, old.ID)
				}
				old.QADir = ""
			}
			if w.PlanFile != "" && old.PlanFile == w.PlanFile {
				if old.State != "ended" {
					return fmt.Errorf("plan file belongs to active workspace %s (%q)", old.ID, old.Name)
				}
				old.PlanFile = ""
			}
		}
		return nil
	})
}
