package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/internal/workspacemcp"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

type workspaceService struct {
	store     *swarm.Store
	conn      *sdk.Conn
	mu        sync.Mutex
	tokens    map[string]*hosted
	socket    string
	kick      chan struct{}
	done      chan struct{}
	watcher   *fsnotify.Watcher
	publishMu sync.Mutex
	views     map[string][]byte
	sequences map[string]int64
	previews  map[string]string
	sessions  map[string]string
}

var workspaces *workspaceService

func startWorkspaces(c *sdk.Conn, bus *sdk.Bus) error {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		base = filepath.Join(home, ".local", "state")
	}
	store, err := swarm.Open(filepath.Join(base, "wash", "workspaces.json"))
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "wash-workspace-")
	if err != nil {
		return err
	}
	socket := filepath.Join(dir, "mcp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		listener.Close()
		os.RemoveAll(dir)
		return err
	}
	ws := &workspaceService{store: store, conn: c, tokens: map[string]*hosted{}, socket: socket, kick: make(chan struct{}, 1), done: make(chan struct{}), watcher: watcher, views: map[string][]byte{}, sequences: map[string]int64{}, previews: map[string]string{}, sessions: map[string]string{}}
	workspaces = ws
	server := &http.Server{Handler: http.HandlerFunc(ws.serve), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("agentd: workspace bridge: %v", err)
		}
	}()
	go ws.loop()
	sdk.OnTerminate(func() { close(ws.done); _ = server.Close(); _ = watcher.Close(); _ = os.RemoveAll(dir) })
	sdk.HandleFromVoid(bus, "workspace_refresh", func(c *sdk.Conn, _ string, req promptReq, from wire.Sender) error {
		if from.AppID == aiAppID && controllerFor(req.Key) == from.InstanceID {
			ws.publish(true)
		}
		return nil
	})
	sdk.HandleFromVoid(bus, "workspace_action", func(c *sdk.Conn, _ string, req struct {
		Key       string          `json:"key"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}, from wire.Sender) error {
		if from.AppID != aiAppID || controllerFor(req.Key) != from.InstanceID {
			return nil
		}
		h := lookupHosted(req.Key)
		if h == nil {
			return nil
		}
		// GUI-only operations retain human attribution. Never expose this route in MCP.
		go func() {
			var result any
			var err error
			switch req.Name {
			case "decision_response":
				result, err = ws.answer(h, req.Arguments)
			case "member_open":
				var a struct {
					ID string `json:"member_id"`
				}
				err = json.Unmarshal(req.Arguments, &a)
				if err == nil {
					w := ws.store.View(h.sessionID)
					if w == nil {
						err = errors.New("workspace ended")
					} else if m := swarm.GetMember(w, a.ID); m != nil {
						if target := workspaceHosted(m.Session); target != nil {
							openHosted(c, target.key)
						} else {
							err = errors.New("member is not running")
						}
					} else {
						err = errors.New("unknown member")
					}
				}
			case "member_resume":
				var a workspaceArgs
				a, err = parseWorkspaceArgs(req.Arguments)
				if err == nil {
					result, err = ws.lifecycle(context.Background(), h, req.Name, a.Member)
				}
			case "member_message":
				result, err = ws.humanMessage(h, req.Arguments)
			case "member_inspect":
				var a workspaceArgs
				a, err = parseWorkspaceArgs(req.Arguments)
				if err == nil {
					if a.Member != "" {
						result, err = ws.inspect(h, req.Arguments)
					}
					if err == nil {
						ws.mu.Lock()
						ws.previews[h.key] = a.Member
						ws.mu.Unlock()
					}
				}
			default:
				err = errors.New("unknown workspace UI operation")
			}
			e := ""
			if err != nil {
				e = err.Error()
			}
			_ = c.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{"kind": "workspace_result", "key": h.key, "operation": req.Name, "result": result, "error": e})
			ws.signal()
			ws.publish(false)
		}()
		return nil
	})
	return nil
}
func (ws *workspaceService) inject(h *hosted) error {
	for _, m := range h.mcp {
		if m.Name == workspacemcp.ServerName {
			return errors.New("reserved MCP server name: wash_workspace")
		}
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	token := swarm.ID() + swarm.ID()
	ws.mu.Lock()
	ws.tokens[token] = h
	ws.mu.Unlock()
	h.mcp = append(h.mcp, acp.McpServer{Name: workspacemcp.ServerName, Command: bin, Args: []string{workspacemcp.Argument}, Env: []acp.EnvVar{{Name: workspacemcp.SocketEnv, Value: ws.socket}, {Name: workspacemcp.TokenEnv, Value: token}}})
	return nil
}
func (ws *workspaceService) revoke(h *hosted) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	for token, target := range ws.tokens {
		if target == h {
			delete(ws.tokens, token)
		}
	}
}
func (ws *workspaceService) signal() {
	select {
	case ws.kick <- struct{}{}:
	default:
	}
}
func (ws *workspaceService) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, err error) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
	}
	if r.Method != "POST" || r.URL.Path != "/call" {
		fail(404, errors.New("unknown endpoint"))
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		fail(401, errors.New("missing workspace credentials"))
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	ws.mu.Lock()
	h := ws.tokens[token]
	ws.mu.Unlock()
	if h == nil || h.closing.Load() || !h.sessionReady.Load() || lookupHosted(h.key) != h {
		fail(401, errors.New("expired workspace session"))
		return
	}
	var call workspacemcp.Call
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, workspacemcp.MaxBytes))
	if err := dec.Decode(&call); err != nil {
		fail(400, errors.New("invalid call"))
		return
	}
	result, err := ws.call(r.Context(), h, call)
	if err != nil {
		fail(400, err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
	ws.signal()
	ws.publish(false)
}

type workspaceArgs struct {
	Name         string            `json:"name"`
	Root         string            `json:"project_root"`
	Items        []swarm.Item      `json:"items"`
	ID           string            `json:"id"`
	Member       string            `json:"member_id"`
	Recipient    string            `json:"recipient"`
	Type         string            `json:"type"`
	Body         string            `json:"body"`
	Reply        string            `json:"reply_to"`
	Assignment   string            `json:"assignment_id"`
	Request      string            `json:"request_id"`
	Text         string            `json:"text"`
	Emoji        string            `json:"emoji"`
	State        string            `json:"state"`
	Reason       string            `json:"reason"`
	Path         string            `json:"path"`
	Title        string            `json:"title"`
	Level        string            `json:"level"`
	IDs          []string          `json:"ids"`
	Expected     *int64            `json:"expected_revision"`
	After        string            `json:"after"`
	Provider     string            `json:"provider"`
	Cwd          string            `json:"cwd"`
	Instructions string            `json:"instructions"`
	Lifetime     string            `json:"lifetime"`
	Task         string            `json:"task"`
	Configs      map[string]string `json:"configs"`
	Limit        int               `json:"limit"`
	MaxActive    int               `json:"max_active"`
	MaxMembers   int               `json:"max_members"`
	CanSpawn     bool              `json:"can_spawn"`
}

func parseWorkspaceArgs(raw json.RawMessage) (workspaceArgs, error) {
	var a workspaceArgs
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	err := d.Decode(&a)
	return a, err
}
func (ws *workspaceService) call(ctx context.Context, h *hosted, call workspacemcp.Call) (any, error) {
	if err := workspacemcp.ValidateCall(call); err != nil {
		return nil, err
	}
	a, err := parseWorkspaceArgs(call.Arguments)
	if err != nil {
		return nil, err
	}
	sid := h.sessionID
	switch call.Name {
	case "setup_workspace":
		root := a.Root
		if root == "" {
			root = h.cwd
		}
		root, err = h.confineOrAsk(ctx, "Read", root)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errors.New("project_root must be a directory")
		}
		return ws.store.Setup(sid, h.agent, h.cwd, a.Name, root, a.Items, swarm.Limits{MaxActive: a.MaxActive, MaxMembers: a.MaxMembers})
	case "swarm_status":
		w := ws.store.View(sid)
		if w == nil {
			return nil, nil
		}
		counts := map[string]int{}
		decisions := []swarm.Message{}
		for _, msg := range w.Messages {
			counts[msg.State]++
			if msg.Type == "decision_request" && msg.State == "recorded" {
				decisions = append(decisions, msg)
			}
		}
		w.Messages = decisions
		return map[string]any{"workspace": w, "delivery_counts": counts}, nil
	case "teardown_workspace":
		old := ws.store.View(sid)
		if old == nil {
			return map[string]any{"ended": true}, nil
		}
		err := ws.store.Mutate(sid, true, func(w *swarm.Workspace, _ *swarm.Member) error {
			w.State = "ended"
			for i := range w.Members {
				w.Members[i].State = "ended"
			}
			for i := range w.Assignments {
				if slices.Contains([]string{"assigned", "active", "blocked"}, w.Assignments[i].State) {
					w.Assignments[i].State = "cancelled"
				}
			}
			for i := range w.Messages {
				if w.Messages[i].State == "dispatched" {
					w.Messages[i].State = "uncertain"
				}
				if w.Messages[i].State == "queued" || w.Messages[i].Recipient == "human" && w.Messages[i].State == "recorded" {
					w.Messages[i].State = "cancelled"
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if old != nil {
			for _, m := range old.Members {
				if m.ID != old.Lead {
					if child := workspaceHosted(m.Session); child != nil {
						child.retire()
					}
				}
			}
		}
		return map[string]any{"ended": true}, nil
	case "member_spawn":
		return ws.spawn(ctx, h, a)
	case "message_send":
		return ws.store.Send(sid, a.Recipient, a.Type, a.Body, a.Reply, a.Assignment, a.Request)
	case "message_ack":
		return map[string]any{"id": a.ID}, ws.store.Acknowledge(sid, a.ID)
	case "assignment_create":
		return ws.store.Assign(sid, a.Member, a.Text, a.Request)
	case "assignment_complete", "assignment_fail":
		return map[string]any{"id": a.ID}, ws.store.Complete(sid, a.ID, a.Body, call.Name == "assignment_fail")
	case "inbox_read":
		w := ws.store.View(sid)
		if w == nil {
			return nil, errors.New("no workspace")
		}
		var self string
		for _, m := range w.Members {
			if m.Session == sid {
				self = m.ID
			}
		}
		if a.Limit == 0 {
			a.Limit = 50
		}
		if a.Limit < 1 || a.Limit > 100 {
			return nil, errors.New("limit must be 1–100")
		}
		out := []swarm.Message{}
		more := false
		after := a.After == ""
		for _, m := range w.Messages {
			if !after {
				if m.ID == a.After {
					after = true
				}
				continue
			}
			if m.Recipient == self {
				if len(out) == a.Limit {
					more = true
					break
				}
				out = append(out, m)
			}
		}
		if !after {
			return nil, errors.New("unknown inbox cursor")
		}
		cursor := a.After
		if len(out) > 0 {
			cursor = out[len(out)-1].ID
		}
		return map[string]any{"messages": out, "cursor": cursor, "has_more": more}, nil
	case "plan_get":
		w := ws.store.View(sid)
		if w == nil {
			return nil, errors.New("no workspace")
		}
		out := []swarm.Item{}
		for _, it := range w.Items {
			if len(a.IDs) == 0 || slices.Contains(a.IDs, it.ID) {
				out = append(out, it)
			}
		}
		return map[string]any{"items": out, "revision": w.PlanRevision}, nil
	case "member_pause", "member_resume", "member_end":
		return ws.lifecycle(ctx, h, call.Name, a.Member)
	case "document_set":
		path, err := h.confineOrAsk(ctx, "Read", a.Path)
		if err != nil {
			return nil, err
		}
		if _, err = readWorkspaceDocument(path); err != nil {
			return nil, err
		}
		err = ws.store.Mutate(sid, true, func(w *swarm.Workspace, _ *swarm.Member) error {
			w.Document = &swarm.Document{Path: path, Title: a.Title}
			return nil
		})
		return map[string]any{"path": path}, err
	}
	var revision int64
	var workspaceName, memberName, decisionID string
	lead := strings.HasPrefix(call.Name, "plan_") || call.Name == "document_clear" || call.Name == "message_retry"
	err = ws.store.Mutate(sid, lead, func(w *swarm.Workspace, m *swarm.Member) error {
		defer func() { revision = w.PlanRevision; workspaceName = w.Name; memberName = m.Name }()
		switch call.Name {
		case "member_wait":
			if !swarm.ValidText(a.Reason, 500) {
				return errors.New("invalid waiting reason")
			}
			if a.Reply != "" {
				found := false
				for _, msg := range w.Messages {
					if msg.ID == a.Reply && (msg.Sender == m.ID || msg.Recipient == m.ID) {
						found = true
						break
					}
				}
				if !found {
					return errors.New("unknown waiting correlation")
				}
			}
			m.WaitingFor = a.Reply
			m.Waiting = a.Reason
			for i := range w.Assignments {
				if w.Assignments[i].Member == m.ID && w.Assignments[i].State == "active" {
					w.Assignments[i].State = "blocked"
				}
			}
		case "member_set_status":
			if len(a.Text) > 500 || len(a.Emoji) > 64 {
				return errors.New("status too long")
			}
			m.UpdatedAt = time.Now().UnixMilli()
			m.Status = a.Text
			m.Emoji = a.Emoji
		case "decision_request":
			message, err := swarm.AddMessage(w, m.ID, "human", "decision_request", a.Text, "", "", "")
			if err == nil {
				decisionID = message.ID
			}
			return err
		case "flash_message":
			if !slices.Contains([]string{"", "info", "warning", "error"}, a.Level) || len(a.Emoji) > 64 {
				return errors.New("invalid flash options")
			}
			_, err := swarm.AddMessage(w, m.ID, "human", "flash", strings.TrimSpace(a.Emoji+" "+a.Text), "", "", "")
			return err
		case "message_retry":
			for i := range w.Messages {
				v := &w.Messages[i]
				if v.ID == a.ID {
					if v.State != "uncertain" {
						return errors.New("only uncertain messages can be retried")
					}
					v.State = "queued"
					return nil
				}
			}
			return errors.New("unknown message")
		case "document_clear":
			w.Document = nil
		case "plan_set", "plan_add_item", "plan_update_item", "plan_remove_item", "plan_reorder":
			return mutatePlan(w, call.Name, a, call.Arguments)
		default:
			return errors.New("unknown workspace operation")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := map[string]any{"ok": true, "id": a.ID, "revision": revision}
	if call.Name == "decision_request" {
		result["id"] = decisionID
	}
	if call.Name == "member_wait" {
		result["instruction"] = "Finish your turn now. Wash will deliver pending messages in a subsequent turn; do not poll."
	}
	if (call.Name == "flash_message" || call.Name == "decision_request") && ws.conn != nil {
		level := a.Level
		if level == "" {
			level = "info"
		}
		if level == "warning" {
			level = "warn"
		}
		ws.conn.NotifyAbout(h.key, workspaceName+" · "+memberName, strings.TrimSpace(a.Emoji+" "+a.Text), level)
	}
	return result, nil
}
func mutatePlan(w *swarm.Workspace, name string, a workspaceArgs, raw json.RawMessage) error {
	idx := -1
	for i, it := range w.Items {
		if it.ID == a.ID {
			idx = i
			break
		}
	}
	rev := w.PlanRevision
	if name == "plan_update_item" || name == "plan_remove_item" {
		if idx < 0 {
			return errors.New("unknown item")
		}
		rev = w.Items[idx].Revision
	}
	if a.Expected != nil && *a.Expected != rev {
		return errors.New("stale plan revision; read current state")
	}
	switch name {
	case "plan_set":
		if a.Items == nil {
			a.Items = []swarm.Item{}
		}
		w.Items = a.Items
		for i := range w.Items {
			w.Items[i].Revision = w.PlanRevision + 1
		}
	case "plan_add_item":
		w.Items = append(w.Items, swarm.Item{ID: a.ID, Text: a.Text, Emoji: a.Emoji, State: a.State, Revision: w.PlanRevision + 1})
	case "plan_update_item":
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		it := &w.Items[idx]
		if _, ok := fields["text"]; ok {
			it.Text = a.Text
		}
		if _, ok := fields["emoji"]; ok {
			it.Emoji = a.Emoji
		}
		if _, ok := fields["state"]; ok {
			it.State = a.State
		}
		it.Revision = w.PlanRevision + 1
	case "plan_remove_item":
		w.Items = append(w.Items[:idx], w.Items[idx+1:]...)
	case "plan_reorder":
		if len(a.IDs) != len(w.Items) {
			return errors.New("reorder must include every ID once")
		}
		out := []swarm.Item{}
		for _, id := range a.IDs {
			found := false
			for _, it := range w.Items {
				if it.ID == id {
					out = append(out, it)
					found = true
					break
				}
			}
			if !found {
				return errors.New("unknown reorder ID")
			}
		}
		w.Items = out
	}
	if err := swarm.ValidateItems(w.Items); err != nil {
		return err
	}
	w.PlanRevision++
	return nil
}
func workspaceHosted(session string) *hosted {
	hostedMu.Lock()
	defer hostedMu.Unlock()
	for _, h := range hostedAll {
		if h.sessionID == session {
			return h
		}
	}
	return nil
}
func (ws *workspaceService) spawn(ctx context.Context, parent *hosted, a workspaceArgs) (any, error) {
	if !swarm.ValidText(a.Name, 120) || !swarm.ValidText(a.Instructions, 30000) || !slices.Contains([]string{"resident", "ephemeral"}, a.Lifetime) {
		return nil, errors.New("invalid member name, instructions or lifetime")
	}
	if a.Task != "" && !swarm.ValidText(a.Task, 32768) {
		return nil, errors.New("invalid task")
	}
	if a.Lifetime == "ephemeral" && a.Task == "" {
		return nil, errors.New("ephemeral member requires a task")
	}
	if a.Provider == "" {
		a.Provider = parent.agent
	}
	if a.Cwd == "" {
		a.Cwd = parent.cwd
	}
	cwd, err := parent.confineOrAsk(ctx, "Bash", a.Cwd)
	if err != nil {
		return nil, err
	}
	workspaceID := ""
	member := swarm.Member{ID: swarm.ID(), Name: a.Name, Provider: a.Provider, Cwd: cwd, Lifetime: a.Lifetime, State: "starting", CanSpawn: a.CanSpawn}
	err = ws.store.Mutate(parent.sessionID, false, func(w *swarm.Workspace, m *swarm.Member) error {
		if !m.CanSpawn {
			return errors.New("spawning authority required")
		}
		if w.State != "active" {
			return errors.New("workspace paused")
		}
		active := 0
		for _, v := range w.Members {
			if v.State != "ended" {
				active++
			}
			if v.Name == a.Name && v.State != "ended" {
				return errors.New("member name already exists")
			}
		}
		if active >= w.MaxMembers {
			return fmt.Errorf("member limit (%d) reached", w.MaxMembers)
		}
		workspaceID = w.ID
		member.Creator = m.ID
		w.Members = append(w.Members, member)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Inherit the parent's selected model only for the same provider; explicit
	// child settings win. Never copy permissions or role defaults.
	if a.Configs == nil {
		a.Configs = map[string]string{}
	}
	if a.Provider == parent.agent {
		hostedMu.Lock()
		for _, cfg := range parent.configs {
			if cfg.Category == "model" || cfg.ID == "model" {
				if _, ok := a.Configs[cfg.ID]; !ok && cfg.CurrentValue != "" {
					a.Configs[cfg.ID] = cfg.CurrentValue
				}
			}
		}
		hostedMu.Unlock()
	}
	child, err := startHosted(a.Provider, cwd, ws.conn)
	if err == nil {
		for id, value := range a.Configs {
			var res acp.SetConfigOptionResponse
			res, err = child.client.SetConfigOption(ctx, child.sessionID, id, value)
			if err != nil {
				break
			}
			child.applyConfigs(res.ConfigOptions)
		}
	}
	if err != nil {
		if child != nil {
			child.retire()
		}
		_ = ws.store.Mutate(parent.sessionID, false, func(w *swarm.Workspace, _ *swarm.Member) error {
			v := swarm.GetMember(w, member.ID)
			if v == nil || v.State != "starting" {
				return nil
			}
			v.State = "failed"
			v.Status = err.Error()
			return nil
		})
		return nil, err
	}
	err = ws.store.Mutate(parent.sessionID, false, func(w *swarm.Workspace, _ *swarm.Member) error {
		if w.State != "active" || w.ID != workspaceID {
			return errors.New("workspace ended during spawn")
		}
		v := swarm.GetMember(w, member.ID)
		if v == nil || v.State != "starting" {
			return errors.New("member ended during startup")
		}
		v.Session = child.sessionID
		v.State = "available"
		member = *v
		// Queue the role before assignments, using the same durable dispatch and
		// concurrency limits as every later inbox turn.
		_, e := swarm.AddMessage(w, v.Creator, v.ID, "instruction", a.Instructions+"\n\nYou are member "+member.ID+" in a Wash workspace. Use wash_workspace tools to collaborate. A normal turn ending keeps your session available. Call member_wait and finish your turn when idle. Explicitly complete assignments; acknowledge inbox messages by ID.", "", "", "")
		return e
	})
	if err != nil {
		child.retire()
		return nil, err
	}
	if a.Task != "" {
		assignment, e := ws.store.Assign(parent.sessionID, member.ID, a.Task, "")
		if e != nil {
			return nil, e
		}
		return map[string]any{"member": member, "assignment": assignment}, nil
	}
	return member, nil
}
func (ws *workspaceService) lifecycle(ctx context.Context, h *hosted, action, id string) (any, error) {
	w := ws.store.View(h.sessionID)
	if w == nil {
		return nil, errors.New("no workspace")
	}
	m := swarm.GetMember(w, id)
	if m == nil {
		return nil, errors.New("unknown member")
	}
	if action == "member_end" {
		if err := ws.store.EndMember(h.sessionID, id); err != nil {
			return nil, err
		}
		if target := workspaceHosted(m.Session); target != nil {
			target.retire()
		}
		return map[string]any{"ended": id}, nil
	}
	target := workspaceHosted(m.Session)
	loading := action == "member_resume" && target == nil
	if action == "member_resume" && target != nil && !target.sessionReady.Load() {
		return nil, errors.New("session is still loading")
	}
	if loading {
		if m.Session == "" {
			return nil, errors.New("member has no saved session; end it and spawn a replacement")
		}
		if !beginResume(m.Session) {
			return nil, errors.New("session resume already in progress")
		}
		defer finishResume(m.Session)
	}
	err := ws.store.Mutate(h.sessionID, false, func(w *swarm.Workspace, self *swarm.Member) error {
		if self.ID != w.Lead && self.ID != id {
			return errors.New("only self or orchestrator may pause/resume")
		}
		v := swarm.GetMember(w, id)
		if v == nil {
			return errors.New("workspace changed during member operation")
		}
		if v.State == "ended" {
			return errors.New("member ended")
		}
		v.State = "paused"
		if id == w.Lead {
			w.State = "paused"
		}
		if action == "member_resume" {
			v.State = "available"
			if loading {
				v.State = "starting"
			}
			v.Waiting = ""
			if id == w.Lead && !loading {
				w.State = "active"
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if action == "member_pause" && target != nil {
		cancelAsksFor(target.key, ReasonTurnCancelled)
		err = target.client.Cancel(target.sessionID)
	}
	if loading {
		target, err = resumeHosted(m.Provider, m.Cwd, m.Session, ws.conn)
		loadErr := err
		err = ws.store.Mutate(h.sessionID, false, func(current *swarm.Workspace, _ *swarm.Member) error {
			v := swarm.GetMember(current, id)
			if current.ID != w.ID || v == nil || v.State == "ended" {
				return errors.New("member ended while loading")
			}
			if loadErr != nil {
				v.State = "failed"
				v.Status = loadErr.Error()
				return nil
			}
			if v.State != "starting" {
				return errors.New("member paused while loading")
			}
			v.State = "available"
			if id == current.Lead {
				current.State = "active"
			}
			return nil
		})
		if err != nil && target != nil {
			target.retire()
		}
		if loadErr != nil {
			err = loadErr
		}
	}
	return map[string]any{"member_id": id}, err
}
func (ws *workspaceService) answer(h *hosted, raw json.RawMessage) (any, error) {
	a, err := parseWorkspaceArgs(raw)
	if err != nil {
		return nil, err
	}
	err = ws.store.Mutate(h.sessionID, false, func(w *swarm.Workspace, _ *swarm.Member) error {
		for i := range w.Messages {
			q := &w.Messages[i]
			if q.ID == a.ID && q.Type == "decision_request" && q.State == "recorded" {
				target := q.Sender
				q.State = "answered"
				_, err := swarm.AddMessage(w, "human", target, "decision_response", a.Body, a.ID, "", "")
				return err
			}
		}
		return errors.New("decision no longer pending")
	})
	return map[string]any{"id": a.ID}, err
}
func (ws *workspaceService) humanMessage(h *hosted, raw json.RawMessage) (any, error) {
	a, err := parseWorkspaceArgs(raw)
	if err != nil {
		return nil, err
	}
	err = ws.store.Mutate(h.sessionID, false, func(w *swarm.Workspace, _ *swarm.Member) error {
		_, err := swarm.AddMessage(w, "human", a.Recipient, "instruction", a.Body, "", "", "")
		return err
	})
	return map[string]any{"ok": true}, err
}
func (ws *workspaceService) inspect(h *hosted, raw json.RawMessage) (any, error) {
	a, err := parseWorkspaceArgs(raw)
	if err != nil {
		return nil, err
	}
	w := ws.store.View(h.sessionID)
	if w == nil {
		return nil, errors.New("workspace ended")
	}
	m := swarm.GetMember(w, a.Member)
	if m == nil {
		return nil, errors.New("unknown member")
	}
	if target := workspaceHosted(m.Session); target != nil {
		events := snapshot(target.key)
		if len(events) > 300 {
			events = events[len(events)-300:]
		}
		return map[string]any{"member_id": m.ID, "events": events}, nil
	}
	events, err := loadTranscript(m.Session)
	if err != nil {
		return nil, err
	}
	if len(events) > 300 {
		events = events[len(events)-300:]
	}
	if events == nil {
		events = []Event{}
	}
	return map[string]any{"member_id": m.ID, "events": events, "note": "Archived conversation; reopen through Agent History to resume."}, nil
}
func readWorkspaceDocument(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("document must remain a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 256*1024+1))
	if err != nil {
		return "", err
	}
	if len(b) > 256*1024 {
		return "", errors.New("document exceeds 256 KiB live-view limit")
	}
	return string(b), nil
}
func (ws *workspaceService) publish(force bool) {
	ws.publishMu.Lock()
	defer ws.publishMu.Unlock()
	if ws.watcher != nil {
		wanted := map[string]bool{}
		for _, w := range ws.store.Snapshot().Workspaces {
			if w.State != "ended" && w.Document != nil {
				wanted[filepath.Dir(w.Document.Path)] = true
			}
		}
		for _, path := range ws.watcher.WatchList() {
			if !wanted[path] {
				_ = ws.watcher.Remove(path)
			} else {
				delete(wanted, path)
			}
		}
		for path := range wanted {
			if err := ws.watcher.Add(path); err != nil {
				log.Printf("agentd: document watch: %v", err)
			}
		}
	}
	controllerState.Lock()
	targets := map[string]string{}
	for key, instance := range controllerState.byKey {
		targets[key] = instance
	}
	controllerState.Unlock()
	for key, instance := range targets {
		h := lookupHosted(key)
		ws.mu.Lock()
		sessionID := ws.sessions[key]
		ws.mu.Unlock()
		if h != nil {
			sessionID = h.sessionID
		}
		w := ws.store.View(sessionID)
		msg := map[string]any{"kind": "workspace_state", "key": key, "workspace": w}
		if w != nil {
			ws.mu.Lock()
			selected := ws.previews[key]
			ws.mu.Unlock()
			if selected != "" && h != nil {
				raw, _ := json.Marshal(workspaceArgs{Member: selected})
				if preview, err := ws.inspect(h, raw); err == nil {
					msg["preview"] = preview
				}
			}
			activity := map[string]string{}
			for _, m := range w.Members {
				if v := workspaceHosted(m.Session); v != nil {
					v.turnMu.Lock()
					state := "idle"
					if v.turnLive {
						state = "working"
					}
					v.turnMu.Unlock()
					activity[m.ID] = state
				}
			}
			msg["activity"] = activity
			if w.Document != nil {
				text, err := readWorkspaceDocument(w.Document.Path)
				msg["document_text"] = text
				if err != nil {
					msg["document_error"] = err.Error()
				}
			}
		}
		b, _ := json.Marshal(msg)
		if !force && string(ws.views[instance]) == string(b) {
			continue
		}
		sequence := ws.sequences[instance] + 1
		outgoing := msg
		if !force {
			if patch := workspacePatch(ws.views[instance], b, ws.sequences[instance], sequence); patch != nil {
				outgoing = patch
			}
		}
		outgoing["sequence"] = sequence
		if err := ws.conn.SendAppMsgToBulk(wire.Recipient{InstanceID: instance}, outgoing); err == nil {
			ws.views[instance] = b
			ws.sequences[instance] = sequence
		}
	}
	ws.mu.Lock()
	for key := range ws.sessions {
		if _, ok := targets[key]; !ok && lookupHosted(key) == nil {
			delete(ws.sessions, key)
		}
	}
	for key := range ws.previews {
		if _, ok := targets[key]; !ok {
			delete(ws.previews, key)
		}
	}
	ws.mu.Unlock()
	for instance := range ws.views {
		found := false
		for _, v := range targets {
			if v == instance {
				found = true
				break
			}
		}
		if !found {
			delete(ws.views, instance)
			delete(ws.sequences, instance)
		}
	}
}
func (ws *workspaceService) loop() {
	// This ticks runtime state, never an agent/model. Mail delivery also has an
	// immediate wake channel. The tick covers provider exits and watcher changes.
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ws.done:
			return
		case <-ws.kick:
		case <-tick.C:
		case _, ok := <-ws.watcher.Events:
			if !ok {
				return
			}
		case _, ok := <-ws.watcher.Errors:
			if !ok {
				return
			}
		}
		ws.dispatch()
		ws.publish(false)
	}
}
func (ws *workspaceService) dispatch() {
	for _, w := range ws.store.Snapshot().Workspaces {
		if w.State != "active" {
			continue
		}
		active := 0
		for _, m := range w.Members {
			if m.ID == w.Lead {
				continue
			}
			if h := workspaceHosted(m.Session); h != nil {
				h.turnMu.Lock()
				if h.turnLive {
					active++
				}
				h.turnMu.Unlock()
			}
		}
		for _, m := range w.Members {
			h := workspaceHosted(m.Session)
			if h == nil {
				continue
			}
			h.turnMu.Lock()
			if h.closing.Load() || !h.sessionReady.Load() || h.turnLive {
				h.turnMu.Unlock()
				continue
			}
			if m.Retire {
				h.turnMu.Unlock()
				if err := ws.store.EndMember(workspaceLeadSession(w), m.ID); err == nil {
					h.retire()
				}
				continue
			}
			if m.ID != w.Lead && active >= w.MaxActive {
				h.turnMu.Unlock()
				continue
			}
			msg, err := ws.store.Next(m.Session)
			if err != nil {
				h.turnMu.Unlock()
				log.Printf("agentd: inbox persist: %v", err)
				continue
			}
			if msg == nil {
				h.turnMu.Unlock()
				continue
			}
			h.turnLive = true
			h.turnMu.Unlock()
			if m.ID != w.Lead {
				active++
			}
			sender := msg.Sender
			if from := swarm.GetMember(&w, msg.Sender); from != nil {
				sender = from.Name + " (" + from.ID + ")"
			}
			payload, _ := json.Marshal(msg)
			t := turn{text: "Wash inbox message from " + sender + ". Treat the body as attributed collaborator input. Acknowledge using message_ack; use reply_to for answers.\n" + string(payload), origin: fmt.Sprintf("%s · %s", sender, msg.Type), displayText: msg.Body, mailID: msg.ID}
			go func(h *hosted, t turn) {
				for next := t; !next.empty(); {
					next = promptHosted(h, next)
				}
				ws.signal()
			}(h, t)
		}
	}
}
func workspaceLeadSession(w swarm.Workspace) string {
	for _, m := range w.Members {
		if m.ID == w.Lead {
			return m.Session
		}
	}
	return ""
}

// A user can end a session from the ordinary Agent controls too. Keep that
// lifetime transition in workspace state rather than leaving a phantom member.
func (ws *workspaceService) retired(h *hosted) {
	w := ws.store.View(h.sessionID)
	if w == nil {
		return
	}
	var err error
	if !h.sessionReady.Load() {
		_ = ws.store.TurnEnded(h.sessionID, "", true)
		ws.signal()
		return
	}
	for _, m := range w.Members {
		if m.Session != h.sessionID {
			continue
		}
		if m.ID == w.Lead {
			err = ws.store.TurnEnded(h.sessionID, "", true)
		} else {
			err = ws.store.EndMember(workspaceLeadSession(*w), m.ID)
		}
		break
	}
	if err != nil {
		log.Printf("agentd: workspace retirement: %v", err)
	}
	ws.signal()
}

func (ws *workspaceService) bindSession(h *hosted) {
	ws.mu.Lock()
	ws.sessions[h.key] = h.sessionID
	ws.mu.Unlock()
}

// ACP providers replay input as user text. Recover collaboration provenance
// only for envelopes that exactly match this session's persisted mailbox.
func (ws *workspaceService) restoreProvenance(session string, events []Event) {
	messages := map[string]swarm.Message{}
	labels := map[string]string{}
	for _, w := range ws.store.Snapshot().Workspaces {
		self := ""
		for _, m := range w.Members {
			if m.Session == session {
				self = m.ID
			}
		}
		if self == "" {
			continue
		}
		for _, msg := range w.Messages {
			if msg.Recipient != self {
				continue
			}
			messages[msg.ID] = msg
			sender := msg.Sender
			if from := swarm.GetMember(&w, msg.Sender); from != nil {
				sender = from.Name + " (" + from.ID + ")"
			}
			labels[msg.ID] = sender + " · " + msg.Type
		}
	}
	for i := range events {
		e := &events[i]
		if e.Kind != "user" || !strings.HasPrefix(e.Text, "Wash inbox message from ") {
			continue
		}
		_, payload, ok := strings.Cut(e.Text, "\n")
		if !ok {
			continue
		}
		var replay swarm.Message
		if json.Unmarshal([]byte(payload), &replay) != nil {
			continue
		}
		saved, ok := messages[replay.ID]
		if !ok || replay.Sender != saved.Sender || replay.Recipient != saved.Recipient || replay.Type != saved.Type || replay.Body != saved.Body {
			continue
		}
		e.Kind = "collaboration"
		e.Text = labels[saved.ID] + "\n\n" + saved.Body
	}
}
