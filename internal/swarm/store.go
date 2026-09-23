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
	Capability string            `json:"capability,omitempty"`
	Provider   string            `json:"provider"`
	Model      string            `json:"model,omitempty"`
	Thinking   string            `json:"thinking,omitempty"`
	Configs    map[string]string `json:"configs,omitempty"`
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
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Provider       string            `json:"provider"`
	Cwd            string            `json:"cwd"`
	Session        string            `json:"session_id"`
	Creator        string            `json:"creator"`
	Lifetime       string            `json:"lifetime"`
	State          string            `json:"state"`
	Status         string            `json:"status,omitempty"`
	Emoji          string            `json:"emoji,omitempty"`
	Waiting        string            `json:"waiting,omitempty"`
	WaitingFor     string            `json:"waiting_for,omitempty"`
	UpdatedAt      int64             `json:"status_updated_at,omitempty"`
	CanSpawn       bool              `json:"can_spawn"`
	Retire         bool              `json:"retire,omitempty"`
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
	QAOriginalHash string                  `json:"qa_original_hash,omitempty"`
	QADocumentID   string                  `json:"qa_document_id,omitempty"`
	QAPreamble     string                  `json:"qa_preamble,omitempty"`
	QAAuthors      map[string]string       `json:"qa_authors,omitempty"`
	QADocument     *Document               `json:"qa_document,omitempty"`
	QA             []QAThread              `json:"qa"`
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
			if w.Profiles == nil {
				w.Profiles = map[string]AgentProfile{}
			}
			if w.MaxActive == 0 {
				w.MaxActive = 4
			}
			if w.MaxMembers == 0 {
				w.MaxMembers = 16
			}
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
func AddMessage(w *Workspace, from, to, kind, body, reply, assignment, request string) (*Message, error) {
	if !ValidText(body, 32768) {
		return nil, errors.New("message body must contain 1–32768 bytes")
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

// Next persists dispatch before ACP submission. Caller must reserve the session's
// turn first. Crash between those operations is deliberately an uncertain delivery.
func (s *Store) Next(session string) (*Message, error) {
	var out *Message
	st := s.Snapshot()
	w, m := find(&st, session)
	if w == nil || w.State != "active" || m.State != "available" || m.Retire {
		return nil, nil
	}
	has := false
	for _, msg := range w.Messages {
		if msg.Recipient == m.ID && msg.State == "queued" {
			has = true
			break
		}
	}
	if !has {
		return nil, nil
	}
	err := s.Mutate(session, false, func(w *Workspace, m *Member) error {
		if w.State != "active" || m.State != "available" || m.Retire {
			return nil
		}
		for i := range w.Messages {
			msg := &w.Messages[i]
			if msg.Recipient == m.ID && msg.State == "queued" {
				msg.State = "dispatched"
				m.Waiting = ""
				m.WaitingFor = ""
				for j := range w.Assignments {
					if w.Assignments[j].Member == m.ID && w.Assignments[j].State == "blocked" {
						w.Assignments[j].State = "active"
					}
				}
				v := *msg
				out = &v
				for j := range w.Assignments {
					if w.Assignments[j].ID == msg.Assignment && w.Assignments[j].State == "assigned" {
						w.Assignments[j].State = "active"
					}
				}
				break
			}
		}
		return nil
	})
	return out, err
}
func (s *Store) Acknowledge(session, id string) error {
	return s.Mutate(session, false, func(w *Workspace, m *Member) error {
		for i := range w.Messages {
			v := &w.Messages[i]
			if v.ID == id {
				if v.Recipient != m.ID {
					return errors.New("message belongs to another member")
				}
				if v.State != "delivered" && v.State != "dispatched" && v.State != "acknowledged" && v.State != "recorded" {
					return errors.New("message not delivered")
				}
				v.State = "acknowledged"
				return nil
			}
		}
		return errors.New("unknown message")
	})
}
func (s *Store) TurnEnded(session, messageID string, failed bool) error {
	// An ordinary successful turn changes no durable workspace state. In
	// particular, reading workspace_get must not invalidate its own revision
	// when that conversation turn ends.
	if !failed && messageID == "" {
		return nil
	}
	if s.View(session) == nil {
		return nil
	}
	return s.Mutate(session, false, func(w *Workspace, m *Member) error {
		if failed {
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
			if v.ID == messageID && v.State == "dispatched" {
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
func (s *Store) EndMember(session, id string) error {
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
		_, _ = AddMessage(w, m.ID, w.Lead, "lifecycle", m.Name+" ended.", "", "", "")
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
