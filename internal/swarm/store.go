// Package swarm owns durable workspace state. No provider or GUI code lives here.
// Mutations run against a copy and become visible only after atomic persistence.
package swarm

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/agentpolicy"
)

type Item struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Emoji    string `json:"emoji,omitempty"`
	State    string `json:"state"`
	Revision int64  `json:"revision"`
}

// AgentProfile describes launch settings and an optional enforced capability profile.
type AgentProfile struct {
	Capability string `json:"capability,omitempty"`
	// Approval "auto" launches the member with host auto-approval on; ""
	// and "ask" leave every unmatched tool call to the human.
	Approval string            `json:"approval,omitempty"`
	Provider string            `json:"provider"`
	Model    string            `json:"model,omitempty"`
	Thinking string            `json:"thinking,omitempty"`
	Configs  map[string]string `json:"configs,omitempty"`
	// Subagents "deny" removes the provider's own subagent tool, so the
	// member's work stays in its transcript and the workspace's accounting.
	// "" and "allow" leave it available.
	Subagents string `json:"subagents,omitempty"`
}

// Package is the human-facing description of a package code.
type Package struct {
	Title string `json:"title"`
}
type Usage struct {
	Used int64 `json:"used"`
	Size int64 `json:"size"`
}
type Member struct {
	Key            string            `json:"key,omitempty"`
	Package        string            `json:"package,omitempty"`
	Role           string            `json:"role,omitempty"`
	Instructions   string            `json:"instructions,omitempty"`
	InitialTask    string            `json:"initial_task,omitempty"`
	Usage          *Usage            `json:"usage,omitempty"`
	Profile        string            `json:"profile,omitempty"`
	LaunchSettings *AgentProfile     `json:"launch_settings,omitempty"`
	InitialConfigs map[string]string `json:"initial_configs,omitempty"`
	// Adjusted are settings the orchestrator changed on the live member
	// (member_control configure), applied over LaunchSettings on every
	// resume. Kept apart so the keyed launch definition stays as declared.
	Adjusted map[string]string `json:"adjusted_configs,omitempty"`
	// AutoApprove is whether host auto-approval is on for this member now:
	// set from Approval at launch, and by the human's toggle afterwards.
	// Kept here, not on the session, so a restart (which pauses rather
	// than ends a member) does not silently switch it off; it ends with
	// the workspace, never outliving the job it was granted for.
	AutoApprove bool   `json:"auto_approve,omitempty"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Cwd         string `json:"cwd"`
	Session     string `json:"session_id"`
	Creator     string `json:"creator"`
	Lifetime    string `json:"lifetime"`
	State       string `json:"state"`
	Status      string `json:"status,omitempty"`
	Emoji       string `json:"emoji,omitempty"`
	Waiting     string `json:"waiting,omitempty"`
	WaitingFor  string `json:"waiting_for,omitempty"`
	// WaitingOn is a set of assignments this member created and is waiting
	// for as a whole: their results are held and delivered together in one
	// turn once every one has completed or failed. One wake-up per review
	// round instead of one per reviewer.
	WaitingOn []string `json:"waiting_on,omitempty"`
	UpdatedAt int64    `json:"status_updated_at,omitempty"`
	CanSpawn  bool     `json:"can_spawn"`
	Retire    bool     `json:"retire,omitempty"`
}
type Assignment struct {
	ID       string `json:"id"`
	Assigner string `json:"assigner"`
	Member   string `json:"member_id"`
	Text     string `json:"text"`
	State    string `json:"state"`
	Result   string `json:"result,omitempty"`
}
type Message struct {
	Thread     string `json:"thread_id,omitempty"`
	ID         string `json:"id"`
	Swarm      string `json:"swarm_id"`
	Sender     string `json:"sender"`
	Recipient  string `json:"recipient"`
	Type       string `json:"type"`
	Body       string `json:"body"`
	ReplyTo    string `json:"reply_to,omitempty"`
	Assignment string `json:"assignment_id,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	State      string `json:"delivery"`
	Created    int64  `json:"created_at"`
}
type Document struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}
type Workspace struct {
	QAOriginalHash string            `json:"qa_original_hash,omitempty"`
	QADocumentID   string            `json:"qa_document_id,omitempty"`
	QAPreamble     string            `json:"qa_preamble,omitempty"`
	QAAuthors      map[string]string `json:"qa_authors,omitempty"`
	QADocument     *Document         `json:"qa_document,omitempty"`
	QA             []QAThread        `json:"qa"`
	// Approvals apply to every member of this workspace, whatever its cwd.
	// Members work in worktrees the orchestrator chooses, and those are as
	// often siblings of project_root as children of it, so a path-scoped
	// rule cannot cover a fleet. Membership is the scope instead: these
	// rules carry no Cwd, and agentpolicy's matcher is reused verbatim.
	Approvals []agentpolicy.Rule `json:"approvals,omitempty"`
	// Packages names each package code ("CT1") for people: the sidebar groups
	// members and questions under "CT1 · Console input-flood test" instead of
	// a bare code, and member names can shrink to their role.
	Packages       map[string]Package      `json:"packages,omitempty"`
	Profiles       map[string]AgentProfile `json:"profiles"`
	DefaultProfile string                  `json:"default_profile"`
	ID             string                  `json:"id"`
	Name           string                  `json:"name"`
	Root           string                  `json:"project_root"`
	Lead           string                  `json:"orchestrator"`
	State          string                  `json:"state"`
	Revision       int64                   `json:"revision"`
	PlanRevision   int64                   `json:"plan_revision"`
	MaxActive      int                     `json:"max_active"`
	MaxMembers     int                     `json:"max_members"`
	Items          []Item                  `json:"items"`
	Document       *Document               `json:"document,omitempty"`
	Members        []Member                `json:"members"`
	Assignments    []Assignment            `json:"assignments"`
	Messages       []Message               `json:"messages"`
}
type State struct {
	Receipts   []Receipt   `json:"receipts,omitempty"`
	Version    int         `json:"version"`
	Workspaces []Workspace `json:"workspaces"`
}
type Store struct {
	mu    sync.Mutex
	path  string
	state State
	write func(string, []byte) error
}

func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }
func Open(path string) (*Store, error) {
	s := &Store{path: path, state: State{Version: 1}, write: atomicWrite}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &s.state); err != nil {
		return nil, err
	}
	if s.state.Version != 1 {
		return nil, errors.New("unsupported workspace store version")
	}
	// Crash recovery is explicit: preserve identities, never replay uncertain work.
	err = s.change(func(st *State) error {
		for i := range st.Workspaces {
			w := &st.Workspaces[i]
			if w.State == "ended" {
				continue
			}
			w.State = "paused"
			for j := range w.Members {
				m := &w.Members[j]
				if m.State != "ended" {
					m.State = "paused"
					m.Waiting = "Interrupted: resume explicitly"
				}
			}
			for j := range w.Messages {
				if w.Messages[j].State == "dispatched" {
					w.Messages[j].State = "uncertain"
				}
			}
		}
		return nil
	})
	return s, err
}
func atomicWrite(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".workspace-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (s *Store) change(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.state)
	if err := fn(&next); err != nil {
		return err
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err = s.write(s.path, b); err != nil {
		return err
	}
	s.state = next
	return nil
}
func (s *Store) Snapshot() State { s.mu.Lock(); defer s.mu.Unlock(); return clone(s.state) }
func find(st *State, session string) (*Workspace, *Member) {
	for i := range st.Workspaces {
		w := &st.Workspaces[i]
		if w.State == "ended" {
			continue
		}
		for j := range w.Members {
			if w.Members[j].Session == session && w.Members[j].State != "ended" {
				return w, &w.Members[j]
			}
		}
	}
	return nil, nil
}
func (s *Store) View(session string) *Workspace {
	st := s.Snapshot()
	w, _ := find(&st, session)
	return w
}
func (s *Store) Mutate(session string, lead bool, fn func(*Workspace, *Member) error) error {
	return s.change(func(st *State) error {
		w, m := find(st, session)
		if w == nil {
			return errors.New("no active workspace; call workspace_configure")
		}
		if lead && m.ID != w.Lead {
			return errors.New("orchestrator operation")
		}
		if err := fn(w, m); err != nil {
			return err
		}
		w.Revision++
		return nil
	})
}
func ValidText(s string, max int) bool { return strings.TrimSpace(s) != "" && len(s) <= max }

type Limits struct{ MaxActive, MaxMembers int }

func (s *Store) Setup(session, provider, cwd, name, root string, items []Item, limits ...Limits) (*Workspace, error) {
	cap := Limits{MaxActive: 4, MaxMembers: 16}
	if len(limits) > 0 {
		if limits[0].MaxActive != 0 {
			cap.MaxActive = limits[0].MaxActive
		}
		if limits[0].MaxMembers != 0 {
			cap.MaxMembers = limits[0].MaxMembers
		}
	}
	if cap.MaxActive < 1 || cap.MaxActive > 16 || cap.MaxMembers < 1 || cap.MaxMembers > 64 || cap.MaxActive > cap.MaxMembers {
		return nil, errors.New("invalid limits: max_active 1–16, max_members 1–64, active <= members")
	}
	if !ValidText(name, 160) {
		return nil, errors.New("name must contain 1–160 bytes")
	}
	if root == "" {
		root = cwd
	}
	if !filepath.IsAbs(root) {
		return nil, errors.New("project_root must be absolute")
	}
	if err := ValidateItems(items); err != nil {
		return nil, err
	}
	err := s.change(func(st *State) error {
		if w, _ := find(st, session); w != nil {
			if w.Name == name && w.Root == root {
				return nil
			}
			return errors.New("teardown current workspace first")
		}
		lead := Member{ID: ID(), Name: "Orchestrator", Provider: provider, Cwd: cwd, Session: session, Lifetime: "resident", State: "available", CanSpawn: true}
		w := Workspace{Profiles: map[string]AgentProfile{}, ID: ID(), Name: name, Root: root, Lead: lead.ID, State: "active", Revision: 1, PlanRevision: 1, MaxActive: cap.MaxActive, MaxMembers: cap.MaxMembers, Items: items, Members: []Member{lead}, Assignments: []Assignment{}, Messages: []Message{}}
		if w.Items == nil {
			w.Items = []Item{}
		}
		for i := range w.Items {
			w.Items[i].Revision = 1
		}
		st.Workspaces = append(st.Workspaces, w)
		return nil
	})
	return s.View(session), err
}
func ValidateItems(items []Item) error {
	if len(items) > 500 {
		return errors.New("maximum 500 plan items")
	}
	seen := map[string]bool{}
	for _, it := range items {
		if !ValidText(it.ID, 80) || !ValidText(it.Text, 2000) || len(it.Emoji) > 64 {
			return errors.New("invalid plan item")
		}
		if !slices.Contains([]string{"pending", "active", "blocked", "done"}, it.State) {
			return errors.New("invalid plan state")
		}
		if seen[it.ID] {
			return errors.New("duplicate plan item ID")
		}
		seen[it.ID] = true
	}
	return nil
}
func GetMember(w *Workspace, id string) *Member {
	if w == nil {
		return nil
	}
	for i := range w.Members {
		if w.Members[i].ID == id || w.Members[i].Key != "" && w.Members[i].Key == id {
			return &w.Members[i]
		}
	}
	return nil
}

// ReportLimit bounds a result, and a member's message to the orchestrator.
// Either stays in the receiver's context and is re-read on every later turn,
// and the orchestrator's context is the workspace's largest cost: like a
// subagent's summary, a report says what happened and points at the detail.
const ReportLimit = 2000

func AddMessage(w *Workspace, from, to, kind, body, reply, assignment, request string) (*Message, error) {
	if !ValidText(body, 32768) {
		return nil, errors.New("message body must contain 1–32768 bytes")
	}
	if len(body) > ReportLimit && (kind == "result" || to == w.Lead && from != w.Lead && GetMember(w, from) != nil) {
		return nil, fmt.Errorf("a %s to the orchestrator or assigner is at most %d bytes (got %d): it is re-read on every later turn; summarize, and put the detail in the QA thread or a file it can open", kind, ReportLimit, len(body))
	}
	if len(w.Messages) >= 10000 {
		return nil, errors.New("workspace message limit reached")
	}
	if to != "human" {
		m := GetMember(w, to)
		if m == nil || m.State == "ended" {
			return nil, errors.New("recipient is not an active workspace member")
		}
	}
	if reply != "" {
		found := false
		for _, m := range w.Messages {
			if m.ID == reply {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("unknown reply_to")
		}
	}
	if assignment != "" {
		found := false
		for _, a := range w.Assignments {
			if a.ID == assignment {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("unknown assignment")
		}
	}
	if request != "" {
		for i := range w.Messages {
			m := &w.Messages[i]
			if m.Sender == from && m.RequestID == request {
				if m.Recipient != to || m.Type != kind || m.Body != body || m.ReplyTo != reply || m.Assignment != assignment {
					return nil, errors.New("request_id reused with different message")
				}
				return m, nil
			}
		}
	}
	state := "queued"
	if kind == "progress" || kind == "flash" || to == "human" {
		state = "recorded"
	}
	w.Messages = append(w.Messages, Message{ID: ID(), Swarm: w.ID, Sender: from, Recipient: to, Type: kind, Body: body, ReplyTo: reply, Assignment: assignment, RequestID: request, State: state, Created: time.Now().UnixMilli()})
	return &w.Messages[len(w.Messages)-1], nil
}

// DeliverLastReport wakes the orchestrator with m's latest undelivered progress
// report when m goes idle. Progress is an FYI while the sender works on; the
// last one before it waits is a report someone has to act on. Sent as progress
// with no assignment to complete, finished work sat unread while both sides
// waited (observed in Redoubt: two implementers "done and reported").
// Earlier check-ins stay in the inbox history.
func DeliverLastReport(w *Workspace, m *Member) {
	if m.ID == w.Lead {
		return
	}
	for i := len(w.Messages) - 1; i >= 0; i-- {
		v := &w.Messages[i]
		if v.Sender != m.ID || v.Recipient != w.Lead {
			continue
		}
		if v.Type == "progress" && v.State == "recorded" {
			v.State = "queued"
		}
		return
	}
}

func (s *Store) Send(session, to, kind, body, reply, assignment, request string) (Message, error) {
	var out Message
	if !slices.Contains([]string{"instruction", "question", "answer", "progress"}, kind) {
		return out, errors.New("invalid message type")
	}
	err := s.Mutate(session, false, func(w *Workspace, m *Member) error {
		v, e := AddMessage(w, m.ID, to, kind, body, reply, assignment, request)
		if e == nil {
			out = *v
		}
		return e
	})
	return out, err
}
func (s *Store) Assign(session, member, text, request string) (Assignment, error) {
	var out Assignment
	err := s.Mutate(session, false, func(w *Workspace, m *Member) error {
		if !m.CanSpawn {
			return errors.New("assignment requires spawning authority")
		}
		target := GetMember(w, member)
		if target == nil || target.State == "ended" {
			return errors.New("unknown member")
		}
		if request != "" {
			for _, msg := range w.Messages {
				if msg.Sender == m.ID && msg.RequestID == request {
					for _, a := range w.Assignments {
						if a.ID == msg.Assignment && a.Text == text && a.Member == member {
							out = a
							return nil
						}
					}
					return errors.New("request_id conflict")
				}
			}
		}
		for _, a := range w.Assignments {
			if a.Member == member && (a.State == "assigned" || a.State == "active" || a.State == "blocked") {
				return errors.New("member already has an active assignment")
			}
		}
		out = Assignment{ID: ID(), Assigner: m.ID, Member: member, Text: text, State: "assigned"}
		w.Assignments = append(w.Assignments, out)
		_, err := AddMessage(w, m.ID, member, "instruction", text, "", out.ID, request)
		return err
	})
	return out, err
}
func (s *Store) Complete(session, id, body string, failed bool) error {
	return s.Mutate(session, false, func(w *Workspace, m *Member) error {
		for i := range w.Assignments {
			a := &w.Assignments[i]
			if a.ID != id {
				continue
			}
			if a.Member != m.ID {
				return errors.New("assignment belongs to another member")
			}
			state := "completed"
			if failed {
				state = "failed"
			}
			if a.State == state && a.Result == body {
				return nil
			}
			if a.State != "active" && a.State != "assigned" && a.State != "blocked" {
				return errors.New("assignment already resolved")
			}
			a.State = state
			a.Result = body
			recipient := a.Assigner
			if assigner := GetMember(w, recipient); assigner == nil || assigner.State == "ended" {
				recipient = w.Lead
			}
			_, err := AddMessage(w, m.ID, recipient, "result", body, "", id, "")
			if err != nil {
				return err
			}
			if m.Lifetime == "ephemeral" {
				m.Retire = true
			}
			return nil
		}
		return errors.New("unknown assignment")
	})
}

// Next persists dispatch before ACP submission and returns the batch one turn
// delivers: normally one message, or every held result of a waiting set once
// the whole set has resolved. Caller must reserve the session's turn first. A
// crash between those operations is deliberately an uncertain delivery.
func (s *Store) Next(session string) ([]Message, error) {
	st := s.Snapshot()
	w, m := find(&st, session)
	if w == nil || w.State != "active" || m.State != "available" || m.Retire {
		return nil, nil
	}
	// Decide on the snapshot first: Mutate bumps the workspace revision, and
	// most calls here find nothing to deliver.
	if batch, stale := pickDelivery(w, m); len(batch) == 0 && len(stale) == 0 {
		return nil, nil
	}
	var out []Message
	err := s.Mutate(session, false, func(w *Workspace, m *Member) error {
		if w.State != "active" || m.State != "available" || m.Retire {
			return nil
		}
		batch, stale := pickDelivery(w, m)
		// An instruction for an assignment already resolved is never sent.
		for _, i := range stale {
			w.Messages[i].State = "cancelled"
		}
		if len(batch) == 0 {
			return nil
		}
		m.Waiting = ""
		m.WaitingFor = ""
		if first := w.Messages[batch[0]]; first.Type == "result" && slices.Contains(m.WaitingOn, first.Assignment) {
			m.WaitingOn = nil
		}
		for j := range w.Assignments {
			if w.Assignments[j].Member == m.ID && w.Assignments[j].State == "blocked" {
				w.Assignments[j].State = "active"
			}
		}
		for _, i := range batch {
			msg := &w.Messages[i]
			msg.State = "dispatched"
			for j := range w.Assignments {
				if w.Assignments[j].ID == msg.Assignment && w.Assignments[j].Member == m.ID && w.Assignments[j].State == "assigned" {
					w.Assignments[j].State = "active"
				}
			}
			out = append(out, *msg)
		}
		return nil
	})
	return out, err
}

// pickDelivery chooses what m's next turn delivers, as indexes into
// w.Messages, and which queued messages are stale.
//
// Stale: an assignment's instruction still queued after the assignee already
// completed or failed that assignment. A member that reads its inbox in the
// turn that delivers its role finds the queued task there and does it; wash
// then used to dispatch the same task again ("late duplicate delivery"), one
// wasted turn per member.
//
// Held: results for a WaitingOn set that has not fully resolved. When it has,
// all of that set's queued results go out together.
func pickDelivery(w *Workspace, m *Member) (batch, stale []int) {
	resolved := func(id string) bool {
		for _, a := range w.Assignments {
			if a.ID == id {
				return a.State == "completed" || a.State == "failed"
			}
		}
		return true
	}
	setDone := len(m.WaitingOn) > 0
	for _, id := range m.WaitingOn {
		setDone = setDone && resolved(id)
	}
	first := -1
	for i, msg := range w.Messages {
		if msg.Recipient != m.ID || msg.State != "queued" {
			continue
		}
		if msg.Type == "instruction" && msg.Assignment != "" && resolved(msg.Assignment) {
			for _, a := range w.Assignments {
				if a.ID == msg.Assignment && a.Member == m.ID {
					stale = append(stale, i)
				}
			}
			continue
		}
		inSet := msg.Type == "result" && slices.Contains(m.WaitingOn, msg.Assignment)
		if inSet {
			if setDone {
				batch = append(batch, i)
			}
			continue
		}
		if first < 0 {
			first = i
		}
	}
	if len(batch) > 0 {
		return batch, stale
	}
	if first >= 0 {
		return []int{first}, stale
	}
	return nil, stale
}

func (s *Store) TurnEnded(session string, messageIDs []string, failed bool) error {
	return s.turnEnded(session, messageIDs, failed, false)
}

// TurnStopped is a turn the human stopped. For a member that is the same as
// a failure (paused, resume explicitly). For the lead it is not: the lead is
// the human's own conversation, and stopping it to redirect it must not
// halt the team. AGENT_SWARM.md pauses dispatch on orchestrator FAILURE;
// observed live, treating a stop as one made eight member launches fail
// "workspace paused" after the human interrupted to type a sentence.
func (s *Store) TurnStopped(session string, messageIDs []string) error {
	return s.turnEnded(session, messageIDs, true, true)
}

func (s *Store) turnEnded(session string, messageIDs []string, failed, stopped bool) error {
	// An ordinary successful turn changes no durable workspace state. In
	// particular, reading workspace_get must not invalidate its own revision
	// when that conversation turn ends.
	if !failed && len(messageIDs) == 0 {
		return nil
	}
	if s.View(session) == nil {
		return nil
	}
	return s.Mutate(session, false, func(w *Workspace, m *Member) error {
		if failed && !(stopped && m.ID == w.Lead) {
			if m.State != "paused" && m.ID != w.Lead {
				// Notification failure must never prevent pausing the member.
				_, _ = AddMessage(w, m.ID, w.Lead, "lifecycle", m.Name+" stopped or failed; inspect and resume explicitly.", "", "", "")
			}
			if m.ID == w.Lead {
				w.State = "paused"
			}
			m.State = "paused"
			m.Waiting = "Turn stopped or failed; resume explicitly"
		}
		for i := range w.Messages {
			v := &w.Messages[i]
			if slices.Contains(messageIDs, v.ID) && v.State == "dispatched" {
				if failed {
					v.State = "uncertain"
				} else {
					v.State = "delivered"
				}
			}
		}
		return nil
	})
}

// EndMember ends a member. notify tells the lead with a lifecycle message,
// which wakes it: right when the member ended outside the lead's control (the
// human closed its session, its adapter exited), noise when the lead ended it
// itself or an ephemeral member retired after delivering its result. Each
// needless notice cost the orchestrator a turn, and five arrived at once when
// it trimmed a team to save money.
func (s *Store) EndMember(session, id string, notify bool) error {
	return s.Mutate(session, true, func(w *Workspace, _ *Member) error {
		if id == w.Lead {
			return errors.New("use teardown_workspace to end the workspace")
		}
		m := GetMember(w, id)
		if m == nil {
			return errors.New("unknown member")
		}
		if m.State == "ended" {
			return nil
		}
		m.State = "ended"
		if notify {
			_, _ = AddMessage(w, m.ID, w.Lead, "lifecycle", m.Name+" ended.", "", "", "")
		}
		for i := range w.Messages {
			if w.Messages[i].Sender == id && w.Messages[i].Type == "decision_request" && w.Messages[i].State == "recorded" {
				w.Messages[i].State = "cancelled"
			}
			if w.Messages[i].Recipient == id && w.Messages[i].State == "dispatched" {
				w.Messages[i].State = "uncertain"
			}
			if w.Messages[i].Recipient == id && w.Messages[i].State == "queued" {
				w.Messages[i].State = "cancelled"
			}
		}
		for i := range w.Assignments {
			a := &w.Assignments[i]
			if a.Member == id && (a.State == "assigned" || a.State == "active" || a.State == "blocked") {
				a.State = "cancelled"
			}
		}
		return nil
	})
}
func (s *Store) String() string { return fmt.Sprintf("workspace store %s", s.path) }
