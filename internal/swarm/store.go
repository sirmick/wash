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

// AgentProfile describes launch settings and an optional enforced capability profile.
type AgentProfile struct {
	Capability string `json:"capability,omitempty"`
	// Approval "auto" launches the member with host auto-approval on; ""
	// and "ask" leave every unmatched tool call to the human.
	Approval string `json:"approval,omitempty"`
	Provider string `json:"provider"`
	// Connection names how the provider is reached ("opencode@openrouter");
	// empty is the provider direct. agentd owns the list and checks it.
	Connection string            `json:"connection,omitempty"`
	Model      string            `json:"model,omitempty"`
	Effort     string            `json:"effort,omitempty"`
	Configs    map[string]string `json:"configs,omitempty"`
	// Subagents "deny" removes the provider's own subagent tool, so the
	// member's work stays in its transcript and the workspace's accounting.
	// "" and "allow" leave it available.
	Subagents string `json:"subagents,omitempty"`
}

type Usage struct {
	Used int64 `json:"used"`
	Size int64 `json:"size"`
}
type Member struct {
	Key string `json:"key,omitempty"`
	// Node is the plan node the member works on; empty is the team (the
	// orchestrator, an Architect).
	Node         string `json:"node,omitempty"`
	Role         string `json:"role,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	InitialTask  string `json:"initial_task,omitempty"`
	// Handoff is the handoff a member launched with handoff_from reads in
	// its first message: what the member it replaces had done and knew.
	Handoff string `json:"handoff,omitempty"`
	Usage   *Usage `json:"usage,omitempty"`
	// Catalog and Model are what the member was asked to run on: the
	// catalog (the workspace's unless the member named one) and the model
	// as given, a slot name or an id. LaunchSettings is what that resolved
	// to, with the member's own settings on top, fixed when its key was
	// reserved: a later catalog change moves no running member.
	Catalog        string            `json:"catalog,omitempty"`
	Model          string            `json:"model,omitempty"`
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
	// Node is the plan node the work is on.
	Node   string `json:"node,omitempty"`
	Text   string `json:"text"`
	State  string `json:"state"`
	Result string `json:"result,omitempty"`
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
	// Questions is a decision_request's question set; Answers the owner's
	// answers on its decision_response.
	Questions *QuestionSet              `json:"questions,omitempty"`
	Answers   map[string]QuestionAnswer `json:"answers,omitempty"`
}
type Workspace struct {
	// QAAuthors names the authors of threads read back from an earlier
	// workspace's files, who are not members of this one.
	QAAuthors map[string]string `json:"qa_authors,omitempty"`
	// QADir is the directory holding one file per QA thread
	// (<thread>.md), written by Wash as threads change.
	QADir string     `json:"qa_dir,omitempty"`
	QA    []QAThread `json:"qa"`
	// Approvals apply to every member of this workspace, whatever its cwd.
	// Members work in worktrees the orchestrator chooses, and those are as
	// often siblings of project_root as children of it, so a path-scoped
	// rule cannot cover a fleet. Membership is the scope instead: these
	// rules carry no Cwd, and agentpolicy's matcher is reused verbatim.
	Approvals []agentpolicy.Rule `json:"approvals,omitempty"`
	// Catalog is where members' models come from (a slot name in a
	// member's `model` resolves against it): the orchestrator's own catalog
	// at setup, changeable with workspace_configure.catalog for later
	// launches.
	Catalog    string `json:"catalog,omitempty"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Root       string `json:"project_root"`
	Lead       string `json:"orchestrator"`
	State      string `json:"state"`
	Revision   int64  `json:"revision"`
	MaxActive  int    `json:"max_active"`
	MaxMembers int    `json:"max_members"`
	// Plan is the workspace's node graph; PlanRevision counts its changes.
	Plan         []Node `json:"plan"`
	PlanRevision int64  `json:"plan_revision"`
	// PlanFile is where Wash writes the plan as it changes (TOML).
	PlanFile string `json:"plan_file,omitempty"`
	// Legend says what the orchestrator's emojis and states mean.
	Legend string `json:"legend,omitempty"`
	// Roles are instruction templates by member role, put before a new
	// member's own instructions (workspace.toml [roles.<role>]).
	Roles map[string]string `json:"roles,omitempty"`
	// ContextWarn is the share of its context window at which a member's
	// use is reported to the orchestrator, once; 0 is the default.
	ContextWarn float64 `json:"context_warn,omitempty"`
	// Nudged are the lifecycle nudges already sent, so each goes once.
	Nudged      []string     `json:"nudged,omitempty"`
	Members     []Member     `json:"members"`
	Assignments []Assignment `json:"assignments"`
	Messages    []Message    `json:"messages"`
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
		return fn(w, m)
	})
}
func ValidText(s string, max int) bool { return strings.TrimSpace(s) != "" && len(s) <= max }

type Limits struct{ MaxActive, MaxMembers int }

// OrchestratorKey is the orchestrator's key, reserved for it: members
// address it by this rather than by its random ID.
const OrchestratorKey = "orchestrator"

func (s *Store) Setup(session, provider, cwd, name, root string, limits ...Limits) (*Workspace, error) {
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
	err := s.change(func(st *State) error {
		if w, _ := find(st, session); w != nil {
			if w.Name == name && w.Root == root {
				return nil
			}
			return errors.New("teardown current workspace first")
		}
		lead := Member{ID: ID(), Key: OrchestratorKey, Name: "Orchestrator", Provider: provider, Cwd: cwd, Session: session, Lifetime: "resident", State: "available", CanSpawn: true}
		w := Workspace{ID: ID(), Name: name, Root: root, Lead: lead.ID, State: "active", Revision: 1, PlanRevision: 1, MaxActive: cap.MaxActive, MaxMembers: cap.MaxMembers, Plan: []Node{}, Members: []Member{lead}, Assignments: []Assignment{}, Messages: []Message{}, QA: []QAThread{}}
		st.Workspaces = append(st.Workspaces, w)
		return nil
	})
	return s.View(session), err
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

// ErrActiveAssignment is a sentinel so a batch can say which earlier update
// the conflict was with.
var ErrActiveAssignment = errors.New("member already has an active assignment")

// Assign gives member an assignment on a plan node: node, or the member's
// own node when node is empty. The node's needs must be done unless override
// says why not.
func (s *Store) Assign(session, member, node, override, text, request string) (Assignment, error) {
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
						if a.ID == msg.Assignment && a.Text == text && a.Member == target.ID {
							out = a
							return nil
						}
					}
					return errors.New("request_id conflict")
				}
			}
		}
		a, err := NewAssignment(w, m, target, node, override, text)
		if err != nil {
			return err
		}
		out = *a
		_, err = AddMessage(w, m.ID, target.ID, "instruction", text, "", out.ID, request)
		return err
	})
	return out, err
}

// NewAssignment records an assignment for target on node (its own when
// empty), starting work there: every assignment is on a plan node, and one
// member holds one open assignment at a time.
func NewAssignment(w *Workspace, by, target *Member, node, override, text string) (*Assignment, error) {
	if node == "" {
		node = target.Node
	}
	if node == "" {
		return nil, fmt.Errorf("an assignment is on a plan node: give node (member %s is on none)", target.Name)
	}
	for _, a := range w.Assignments {
		if a.Member == target.ID && a.Open() {
			return nil, ErrActiveAssignment
		}
	}
	if err := startWork(w, by, node, override); err != nil {
		return nil, err
	}
	w.Assignments = append(w.Assignments, Assignment{ID: ID(), Assigner: by.ID, Member: target.ID, Node: node, Text: text, State: "assigned"})
	return &w.Assignments[len(w.Assignments)-1], nil
}
func (s *Store) Complete(session, id, body string, failed bool) error {
	return s.Mutate(session, false, func(w *Workspace, m *Member) error {
		for i := range w.Assignments {
			a := &w.Assignments[i]
			if a.ID != id {
				continue
			}
			if a.Member != m.ID {
				// Only the assignee reports a result. The one who assigned it
				// was told only "another member", even when the work was
				// already done and there was nothing left to record.
				who := a.Member
				if assignee := GetMember(w, a.Member); assignee != nil {
					who = assignee.Name
				}
				if a.Assigner == m.ID {
					return fmt.Errorf("assignment %s is %s's to complete or fail, and it is %s; accepting a result needs no call", id, who, a.State)
				}
				return fmt.Errorf("assignment %s is %s's; only its assignee completes or fails it", id, who)
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
			settleWork(w, a.Node, failed)
			PlanNudges(w)
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
// delivers: everything queued that is not held for a waiting set. Caller must reserve the session's turn first. A
// crash between those operations is deliberately an uncertain delivery.
func (s *Store) Next(session string) ([]Message, error) {
	st := s.Snapshot()
	w, m := find(&st, session)
	if w == nil || w.State != "active" || m.State != "available" || m.Retire {
		return nil, nil
	}
	// Decide on the snapshot first: Mutate rewrites the state file, and most
	// calls here find nothing to deliver.
	if batch, stale, _ := pickDelivery(w, m); len(batch) == 0 && len(stale) == 0 {
		return nil, nil
	}
	var out []Message
	err := s.Mutate(session, false, func(w *Workspace, m *Member) error {
		if w.State != "active" || m.State != "available" || m.Retire {
			return nil
		}
		batch, stale, setDone := pickDelivery(w, m)
		// An instruction for an assignment already resolved is never sent.
		for _, i := range stale {
			w.Messages[i].State = "cancelled"
		}
		if len(batch) == 0 {
			return nil
		}
		m.Waiting = ""
		m.WaitingFor = ""
		if setDone {
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
// Held: while a WaitingOn set has not fully resolved, its results and the
// answers and progress of the members doing it. A reviewer answering a QA
// question and then reporting its result used to wake the orchestrator twice,
// and the early answer overtook other members' held results. Questions still
// wake at once. When the set resolves, everything held goes out together.
//
// Everything else queued goes out in the same turn, in order: one message
// per turn woke the orchestrator for each in turn. A member's turn stops
// before a second ask (an instruction or question): members run on cheaper
// models, which drop or blur the second of two tasks in one prompt. The
// orchestrator takes every ask at once.
func pickDelivery(w *Workspace, m *Member) (batch, stale []int, setDone bool) {
	resolved := func(id string) bool {
		for _, a := range w.Assignments {
			if a.ID == id {
				return a.State == "completed" || a.State == "failed" || a.State == "cancelled"
			}
		}
		return true
	}
	setDone = len(m.WaitingOn) > 0
	var doers []string
	for _, id := range m.WaitingOn {
		setDone = setDone && resolved(id)
		for _, a := range w.Assignments {
			if a.ID == id {
				doers = append(doers, a.Member)
			}
		}
	}
	// A member that asked the owner waits for the answer: its question
	// blocks it, and nothing else reaches it until the answer does.
	owner := false
	for _, msg := range w.Messages {
		owner = owner || msg.Sender == m.ID && msg.Type == "decision_request" && msg.State == "recorded"
	}
	asked, cut := false, false
	for i, msg := range w.Messages {
		if msg.Recipient != m.ID || msg.State != "queued" {
			continue
		}
		if owner && msg.Type != "decision_response" {
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
		held := msg.Type == "result" && slices.Contains(m.WaitingOn, msg.Assignment) ||
			(msg.Type == "answer" || msg.Type == "progress") && slices.Contains(doers, msg.Sender)
		if held && !setDone || cut {
			continue
		}
		if m.ID != w.Lead && (msg.Type == "instruction" || msg.Type == "question") {
			if asked {
				cut = true
				continue
			}
			asked = true
		}
		batch = append(batch, i)
	}
	// The owner's answer leads its turn: it is what the member waited for,
	// and what it held back comes after.
	slices.SortStableFunc(batch, func(a, b int) int {
		ra, rb := w.Messages[a].Type == "decision_response", w.Messages[b].Type == "decision_response"
		switch {
		case ra && !rb:
			return -1
		case rb && !ra:
			return 1
		}
		return 0
	})
	// A note wakes nobody busy: it goes out with the next turn something
	// else starts. It does wake a member idle in a plain wait (no waiting
	// set): an orchestrator that kept to "do not poll" and waited for a
	// member's owner question to show was otherwise never told, and sat
	// until its deadline (live shakedown, GLM-5.3, 2026-09-27); Sonnet had
	// only got through by polling.
	if (m.Waiting == "" || len(m.WaitingOn) > 0) && !slices.ContainsFunc(batch, func(i int) bool { return w.Messages[i].Type != "note" }) {
		batch = nil
	}
	return batch, stale, setDone
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
	// An ordinary successful turn changes no durable workspace state.
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
		if !failed {
			idleNudge(w, m)
		}
		return nil
	})
}

// idleNudge reminds a member, once per assignment, that its turn ended with
// its assignment open, no report and no waiting set: a cheap model that
// forgets to report strands the work, and everyone waits on it.
func idleNudge(w *Workspace, m *Member) {
	if m.ID == w.Lead || m.Retire {
		return
	}
	// Not when it will wake anyway (mail is queued for it), is waiting on
	// the owner, or ended its turn on a question it is waiting to hear back on.
	lastSent := ""
	for _, msg := range w.Messages {
		if msg.Recipient == m.ID && msg.State == "queued" {
			return
		}
		if msg.Sender == m.ID && msg.Type == "decision_request" && msg.State == "recorded" {
			return
		}
		if msg.Sender == m.ID {
			lastSent = msg.Type
		}
	}
	if lastSent == "question" {
		return
	}
	for _, a := range w.Assignments {
		if a.Member != m.ID || a.State != "active" {
			continue
		}
		key := "idle:" + a.ID
		if slices.Contains(w.Nudged, key) {
			return
		}
		if _, err := AddMessage(w, "wash", m.ID, "instruction", "Your turn ended with assignment "+a.ID+" still open and no report. Report it now with member_update assignment_results (complete or fail, a summary of at most 2000 bytes), or, if you are waiting on someone, set member_update waiting and end your turn.", "", a.ID, ""); err == nil {
			w.Nudged = append(w.Nudged, key)
		}
		return
	}
}

// NudgeOnce sends the orchestrator one lifecycle message per key.
func NudgeOnce(w *Workspace, key, body string) { nudge(w, key, body) }

// EndMember ends a member. notify tells the lead with a lifecycle message,
// which wakes it: right when the member ended outside the lead's control (the
// human closed its session, its adapter exited), noise when the lead ended it
// itself or an ephemeral member retired after delivering its result. Each
// needless notice cost the orchestrator a turn, and five arrived at once when
// it trimmed a team to save money.
func (s *Store) EndMember(session, id string, notify bool) error {
	return s.Mutate(session, true, func(w *Workspace, _ *Member) error {
		if id == w.Lead {
			return errors.New("use workspace_end to end the workspace")
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
				WithdrawDecision(w, &w.Messages[i])
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
			if a.Member == id && a.Open() {
				a.State = "cancelled"
				if n := PlanNode(w, a.Node); n != nil && n.State == "active" && len(OpenOn(w, a.Node)) == 0 {
					nudge(w, "idle:"+a.ID, "Node "+n.ID+" ("+n.Title+") is active with nobody on it: "+m.Name+" ended with assignment "+a.ID+" open. Assign it again, or set the node's state.")
				}
			}
		}
		return nil
	})
}
func (s *Store) String() string { return fmt.Sprintf("workspace store %s", s.path) }
