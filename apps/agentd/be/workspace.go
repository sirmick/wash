package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
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
	qaMu      sync.Mutex
	qaFiles   map[string]qaFileState
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
					args, _ := json.Marshal(map[string]any{"action": "resume", "member_ids": []string{a.Member}})
					result, err = ws.call(context.Background(), h, workspacemcp.Call{Name: "member_control", Arguments: args})
					// Surface this single member's failure in the GUI, even though
					// bulk process controls return errors in individual outcomes.
					if err == nil {
						var report struct {
							Outcomes []struct {
								Error string `json:"error"`
							} `json:"outcomes"`
						}
						encoded, _ := json.Marshal(result)
						if json.Unmarshal(encoded, &report) == nil {
							for _, outcome := range report.Outcomes {
								if outcome.Error != "" {
									err = errors.New(outcome.Error)
									break
								}
							}
						}
					}
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
	Thread          string `json:"thread_id"`
	Package         string `json:"package"`
	View            string `json:"view"`
	ID              string `json:"id"`
	Member          string `json:"member_id"`
	Recipient       string `json:"recipient"`
	Body            string `json:"body"`
	Text            string `json:"text"`
	Emoji           string `json:"emoji"`
	Level           string `json:"level"`
	After           string `json:"after"`
	IncludeMessages bool   `json:"include_messages"`
	Limit           int    `json:"limit"`
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
func (ws *workspaceService) callCore(h *hosted, call workspacemcp.Call) (any, error) {
	a, err := parseWorkspaceArgs(call.Arguments)
	if err != nil {
		return nil, err
	}
	sid := h.sessionID
	switch call.Name {
	case "workspace_get":
		if a.View != "" && a.View != "state" && a.View != "about" && a.View != "qa" && a.View != "team" {
			return nil, errors.New("view must be state, about, qa or team")
		}
		if a.View == "about" {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(call.Arguments, &fields)
			if len(fields) != 1 {
				return nil, errors.New("about does not accept message history options")
			}
			return ws.about(h), nil
		}
		w := ws.store.View(sid)
		if w == nil {
			return nil, nil
		}
		if a.View == "qa" {
			return qaView(w, a)
		}
		if a.View == "team" {
			if a.Thread != "" || a.Package != "" || a.IncludeMessages || a.After != "" || a.Limit != 0 {
				return nil, errors.New("view=team takes no other options")
			}
			return teamView(w), nil
		}
		if a.Thread != "" || a.Package != "" {
			return nil, errors.New("thread_id/package require view=qa")
		}
		qaSummary(w)
		counts := map[string]int{}
		decisions := []swarm.Message{}
		for _, msg := range w.Messages {
			counts[msg.State]++
			if msg.Type == "decision_request" && msg.State == "recorded" {
				decisions = append(decisions, msg)
			}
		}
		activity, detail, usage := workspaceRuntime(w)
		var messagePage any
		if a.IncludeMessages {
			page, cursor, more, err := workspaceHistoryPage(w.Messages, a.After, a.Limit)
			if err != nil {
				return nil, err
			}
			w.Messages = page
			messagePage = map[string]any{"cursor": cursor, "has_more": more}
		} else {
			if a.After != "" || a.Limit != 0 {
				return nil, errors.New("after and limit require include_messages")
			}
			w.Messages = decisions
		}
		sessions := map[string]any{}
		hostedMu.Lock()
		for _, member := range w.Members {
			for _, live := range hostedAll {
				if live.sessionID == member.Session && live.sessionReady.Load() {
					options := append([]acp.ConfigOption(nil), live.configs...)
					for i := range options {
						options[i].Options = append([]acp.ConfigOptionValue(nil), options[i].Options...)
					}
					sessions[member.ID] = map[string]any{"provider": live.agent, "config_options": options}
				}
			}
		}
		hostedMu.Unlock()
		return map[string]any{"workspace": w, "approvals": workspaceApprovals(w), "delivery_counts": counts, "activity": activity, "activity_detail": detail, "usage": usage, "message_history_included": a.IncludeMessages, "message_page": messagePage, "sessions": sessions}, nil
	case "workspace_end":
		old, err := ws.end(sid)
		if err != nil {
			return nil, err
		}
		if old == nil {
			return map[string]any{"ended": true}, nil
		}
		// find() deliberately hides ended workspaces; report the final save
		// explicitly, while failed exports keep retrying in the service loop.
		return map[string]any{"ended": true, "qa_document_status": ws.qaDocumentStatus(old)}, nil
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
	}
	var workspaceName, memberName string
	lead := call.Name == "message_retry"
	err = ws.store.Mutate(sid, lead, func(w *swarm.Workspace, m *swarm.Member) error {
		workspaceName, memberName = w.Name, m.Name
		switch call.Name {
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
		default:
			return errors.New("unknown workspace operation")
		}
	})
	if err != nil {
		return nil, err
	}
	result := map[string]any{"ok": true, "id": a.ID}
	if call.Name == "flash_message" && ws.conn != nil {
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
func (ws *workspaceService) spawn(ctx context.Context, parent *hosted, id string) (any, error) {
	var member swarm.Member
	var workspaceID string
	err := ws.store.Mutate(parent.sessionID, true, func(w *swarm.Workspace, _ *swarm.Member) error {
		if w.State != "active" {
			return errors.New("workspace paused")
		}
		m := swarm.GetMember(w, id)
		if m == nil || m.State != "pending" {
			return errors.New("member is not pending launch")
		}
		// Reserving a member always records these. Refusing here rather than
		// dereferencing keeps a store written by another build from taking
		// agentd down, and leaves the member pending rather than stuck starting.
		if m.LaunchSettings == nil {
			return errors.New("member has no recorded launch settings; end it and reserve a replacement")
		}
		member, workspaceID = *m, w.ID
		m.State = "starting"
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Without a profile, preserve same-provider model inheritance. A profile
	// starts from that provider's defaults rather than the caller's settings.
	settings := *member.LaunchSettings
	settings.Configs = maps.Clone(settings.Configs)
	if settings.Configs == nil {
		settings.Configs = make(map[string]string)
	}
	if member.Profile == "" && settings.Model == "" && settings.Provider == parent.agent {
		hostedMu.Lock()
		for _, cfg := range parent.configs {
			if cfg.Category == "model" || cfg.ID == "model" {
				if _, ok := settings.Configs[cfg.ID]; !ok && cfg.CurrentValue != "" {
					settings.Configs[cfg.ID] = cfg.CurrentValue
				}
			}
		}
		hostedMu.Unlock()
	}
	child, err := startHostedCapability(settings.Provider, member.Cwd, ws.conn, settings.Capability)
	var initialConfigs map[string]string
	if err == nil {
		hostedMu.Lock()
		options := append([]acp.ConfigOption(nil), child.configs...)
		hostedMu.Unlock()
		initialConfigs, err = configureWorkspaceSession(settings, options, func(id, value string) ([]acp.ConfigOption, error) {
			res, e := child.client.SetConfigOption(ctx, child.sessionID, id, value)
			if e == nil {
				child.applyConfigs(res.ConfigOptions)
			}
			return res.ConfigOptions, e
		})
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
	// Before the member is available, so its first turn is already covered.
	autoApprove := settings.Approval == "auto"
	if autoApprove {
		child.setYolo(true, "launched with approval \"auto\"")
	}
	var initialAssignment *swarm.Assignment
	err = ws.store.Mutate(parent.sessionID, false, func(w *swarm.Workspace, _ *swarm.Member) error {
		if w.State != "active" || w.ID != workspaceID {
			return errors.New("workspace ended during spawn")
		}
		v := swarm.GetMember(w, member.ID)
		if v == nil || v.State != "starting" {
			return errors.New("member ended during startup")
		}
		v.Session = child.sessionID
		v.LaunchSettings = &settings
		v.InitialConfigs = initialConfigs
		v.AutoApprove = autoApprove
		v.State = "available"
		member = *v
		// Queue the role before assignments, using the same durable dispatch and
		// concurrency limits as every later inbox turn.
		_, e := swarm.AddMessage(w, v.Creator, v.ID, "instruction", member.Instructions+"\n\nYou are member "+member.ID+" in a Wash workspace. Use wash_workspace tools to collaborate. A normal turn ending keeps your session available. Use member_update with waiting, then finish your turn when idle. Report assignment results with member_update or assignment_update; acknowledge inbox messages with inbox_ack. Track package questions in QA threads using message_send and member_update. Resident package workers remain available for fixes until the orchestrator ends them.", "", "", "")
		if e != nil {
			return e
		}
		if member.InitialTask != "" {
			task := swarm.Assignment{ID: swarm.ID(), Assigner: v.Creator, Member: v.ID, Text: member.InitialTask, State: "assigned"}
			w.Assignments = append(w.Assignments, task)
			initialAssignment = &task
			_, e = swarm.AddMessage(w, v.Creator, v.ID, "instruction", member.InitialTask, "", task.ID, "")
		}
		return e
	})
	if err != nil {
		child.retire()
		_ = ws.store.Mutate(parent.sessionID, false, func(w *swarm.Workspace, _ *swarm.Member) error {
			if m := swarm.GetMember(w, member.ID); m != nil && m.State == "starting" {
				m.State = "failed"
				m.Status = err.Error()
			}
			return nil
		})
		return nil, err
	}
	if initialAssignment != nil {
		return map[string]any{"member": member, "assignment": initialAssignment}, nil
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
		if err := ws.store.EndMember(h.sessionID, id, false); err != nil {
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
	const restored = "restored: this workspace member was auto-approved before the restart"
	if action == "member_resume" && target != nil && m.AutoApprove {
		target.setYolo(true, restored)
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
		capability := ""
		if m.LaunchSettings != nil {
			capability = m.LaunchSettings.Capability
		}
		target, err = resumeHostedCapability(m.Provider, m.Cwd, m.Session, ws.conn, capability)
		if err == nil && m.LaunchSettings != nil {
			// session/load comes back on the adapter's defaults: observed, a
			// resumed Architect on claude-fable-5-1 at effort "default" where
			// its profile said claude-fable-5-1[1m] at high. Reapply the launch
			// settings before the member is available, so no turn runs on the
			// wrong model. Best effort: a setting the adapter no longer offers
			// is logged rather than stranding a resident that loaded fine.
			hostedMu.Lock()
			options := append([]acp.ConfigOption(nil), target.configs...)
			hostedMu.Unlock()
			skipped, cerr := restoreWorkspaceSession(*m.LaunchSettings, options, func(id, value string) ([]acp.ConfigOption, error) {
				res, e := target.client.SetConfigOption(ctx, target.sessionID, id, value)
				if e == nil {
					target.applyConfigs(res.ConfigOptions)
				}
				return res.ConfigOptions, e
			})
			if len(skipped) > 0 {
				log.Printf("agentd: workspace resume member=%s: not offered by the loaded session, left as loaded: %s", id, strings.Join(skipped, " "))
			}
			if cerr != nil {
				log.Printf("agentd: workspace resume member=%s: launch settings not reapplied: %v", id, cerr)
			}
		}
		if err == nil && m.AutoApprove {
			target.setYolo(true, restored)
		}
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
	defer ws.syncQADocuments()
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
				reply, err := swarm.AddMessage(w, "human", target, "decision_response", a.Body, a.ID, "", "")
				if err == nil && q.Thread != "" {
					err = swarm.LinkQA(w, q.Thread, reply)
				}
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
		pending := []Ask{}
		if svc != nil {
			for _, ask := range svc.Snapshot().Asks {
				if ask.RowKey == target.key {
					pending = append(pending, ask)
				}
			}
		}
		return map[string]any{"member_id": m.ID, "events": events, "asks": pending}, nil
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
	ws.syncQADocuments()
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
			activity, detail, usage := workspaceRuntime(w)
			msg["activity"], msg["activity_detail"], msg["usage"] = activity, detail, usage
			msg["approvals"] = workspaceApprovals(w)
			msg["qa_markdown"] = swarm.QAMarkdown(w)
			msg["qa_document_status"] = ws.qaDocumentStatus(w)
			qaSummary(w)
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
				if err := ws.store.EndMember(workspaceLeadSession(w), m.ID, false); err == nil {
					h.retire()
				}
				continue
			}
			if m.ID != w.Lead && active >= w.MaxActive {
				h.turnMu.Unlock()
				continue
			}
			batch, err := ws.store.Next(m.Session)
			if err != nil {
				h.turnMu.Unlock()
				log.Printf("agentd: inbox persist: %v", err)
				continue
			}
			if len(batch) == 0 {
				h.turnMu.Unlock()
				continue
			}
			h.turnLive = true
			h.turnMu.Unlock()
			if m.ID != w.Lead {
				active++
			}
			t := inboxTurn(batch, func(msg swarm.Message) string { return inboxLabel(&w, msg) })
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

// end tears down the workspace the lead session leads: every member ends,
// open work is cancelled, children's sessions retire and the QA file gets its
// final save. It returns the workspace as it was, or nil if there was none.
func (ws *workspaceService) end(lead string) (*swarm.Workspace, error) {
	old := ws.store.View(lead)
	if old == nil {
		return nil, nil
	}
	err := ws.store.Mutate(lead, true, func(w *swarm.Workspace, _ *swarm.Member) error {
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
			if w.Messages[i].State == "queued" {
				w.Messages[i].State = "cancelled"
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, m := range old.Members {
		if m.ID != old.Lead {
			if child := workspaceHosted(m.Session); child != nil {
				child.retire()
			}
		}
	}
	ws.syncQADocuments()
	return old, nil
}

// A user can end a session from the ordinary Agent controls too. Keep that
// lifetime transition in workspace state rather than leaving a phantom member.
// Ending the orchestrator's session ends its workspace: nothing else can lead
// it, and a leaderless workspace kept its QA file and project claimed, so a
// new orchestrator could not set up there.
func (ws *workspaceService) retired(h *hosted) {
	ws.captureUsage(h)
	w := ws.store.View(h.sessionID)
	if w == nil {
		return
	}
	var err error
	if !h.sessionReady.Load() {
		_ = ws.store.TurnEnded(h.sessionID, nil, true)
		ws.signal()
		return
	}
	for _, m := range w.Members {
		if m.Session != h.sessionID {
			continue
		}
		if m.ID == w.Lead {
			_, err = ws.end(h.sessionID)
		} else {
			err = ws.store.EndMember(workspaceLeadSession(*w), m.ID, true)
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
			labels[msg.ID] = inboxLabel(&w, msg)
		}
	}
	for i := range events {
		e := &events[i]
		if e.Kind != "user" || !strings.HasPrefix(e.Text, inboxTurnPrefix) {
			continue
		}
		_, payload, ok := strings.Cut(e.Text, "\n")
		if !ok {
			continue
		}
		var replay []swarm.Message
		if json.Unmarshal([]byte(payload), &replay) != nil || len(replay) == 0 {
			continue
		}
		verified := true
		for _, r := range replay {
			saved, ok := messages[r.ID]
			verified = verified && ok && r.Sender == saved.Sender && r.Recipient == saved.Recipient && r.Type == saved.Type && r.Body == saved.Body
		}
		if !verified {
			continue
		}
		origin, body := inboxDisplay(replay, func(msg swarm.Message) string { return labels[msg.ID] })
		e.Kind = "collaboration"
		e.Text = origin + "\n\n" + body
	}
}

// Keep optional history readback useful even when the retained inbox is large.
// The next cursor is the last returned message, never a skipped message.
func workspaceHistoryPage(messages []swarm.Message, after string, limit int) ([]swarm.Message, string, bool, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return nil, "", false, errors.New("limit must be 1–100")
	}
	start := 0
	if after != "" {
		i := slices.IndexFunc(messages, func(m swarm.Message) bool { return m.ID == after })
		if i < 0 {
			return nil, "", false, errors.New("unknown workspace history cursor")
		}
		start = i + 1
	}
	page := []swarm.Message{}
	size := 0
	for _, m := range messages[start:] {
		b, _ := json.Marshal(m)
		if len(page) == limit || len(page) > 0 && size+len(b) > 256<<10 {
			break
		}
		page = append(page, m)
		size += len(b)
	}
	cursor := after
	if len(page) > 0 {
		cursor = page[len(page)-1].ID
	}
	return page, cursor, start+len(page) < len(messages), nil
}

// teamView answers "who is doing what, and what is waiting on whom" in one
// screen: per live member its state, activity, status, open assignments and
// undelivered mail, plus each session's current settings as values only.
// The full state carries every session's option catalogue, and nothing else
// showed which member a queued message was stuck on, so an orchestrator had
// been reading wash's own store file to find out.
func teamView(w *swarm.Workspace) map[string]any {
	activity, detail, usage := workspaceRuntime(w)
	settings := map[string]map[string]string{}
	hostedMu.Lock()
	for _, m := range w.Members {
		for _, live := range hostedAll {
			if m.Session != "" && live.sessionID == m.Session && live.sessionReady.Load() {
				cur := map[string]string{}
				for _, c := range live.configs {
					cur[c.ID] = c.CurrentValue
				}
				settings[m.ID] = cur
			}
		}
	}
	hostedMu.Unlock()
	members := []map[string]any{}
	for _, m := range w.Members {
		if m.State == "ended" {
			continue
		}
		open := []map[string]string{}
		for _, a := range w.Assignments {
			if a.Member == m.ID && a.State != "completed" && a.State != "failed" {
				open = append(open, map[string]string{"id": a.ID, "state": a.State, "text": firstLine(a.Text, 120)})
			}
		}
		inbox := map[string]int{}
		for _, msg := range w.Messages {
			if msg.Recipient == m.ID && (msg.State == "queued" || msg.State == "dispatched" || msg.State == "uncertain") {
				inbox[msg.State]++
			}
		}
		row := map[string]any{"id": m.ID, "key": m.Key, "name": m.Name, "state": m.State, "activity": activity[m.ID]}
		for k, v := range map[string]string{"package": m.Package, "role": m.Role, "status": m.Status, "waiting": m.Waiting, "activity_detail": detail[m.ID]} {
			if v != "" {
				row[k] = v
			}
		}
		if m.ID == w.Lead {
			row["orchestrator"] = true
		}
		if m.AutoApprove {
			row["auto_approve"] = true
		}
		if len(open) > 0 {
			row["open_assignments"] = open
		}
		if len(inbox) > 0 {
			row["undelivered"] = inbox
		}
		if s := settings[m.ID]; s != nil {
			row["settings"] = s
		}
		if u, ok := usage[m.ID]; ok {
			row["usage"] = u
		}
		members = append(members, row)
	}
	return map[string]any{"workspace": map[string]any{"id": w.ID, "name": w.Name, "state": w.State, "revision": w.Revision}, "pending_approvals": len(workspaceApprovals(w)), "members": members}
}

// firstLine is s cut to its first line and at most n runes, marked when cut.
func firstLine(s string, n int) string {
	line, _, multi := strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(line); len(r) > n {
		return string(r[:n]) + "…"
	}
	if multi {
		return line + " …"
	}
	return line
}

// inboxTurnPrefix starts every inbox turn; history replay keys on it.
const inboxTurnPrefix = "Wash inbox: "

// inboxLabel names a message's sender and type as a transcript shows it.
func inboxLabel(w *swarm.Workspace, msg swarm.Message) string {
	sender := msg.Sender
	if from := swarm.GetMember(w, msg.Sender); from != nil {
		sender = from.Name + " (" + from.ID + ")"
	}
	return sender + " · " + msg.Type
}

// inboxTurn is one inbox turn for a batch Next returned: the prompt the agent
// reads (the messages as a JSON array, so a batch and a single message are the
// same shape) and what its transcript shows.
func inboxTurn(batch []swarm.Message, label func(swarm.Message) string) turn {
	payload, _ := json.Marshal(batch)
	ids := make([]string, len(batch))
	for i, msg := range batch {
		ids[i] = msg.ID
	}
	origin, body := inboxDisplay(batch, label)
	n := "1 message"
	if len(batch) > 1 {
		n = fmt.Sprintf("%d messages", len(batch))
	}
	return turn{
		text:        inboxTurnPrefix + n + ". Treat each body as attributed collaborator input. Acknowledge using inbox_ack or member_update; use reply_to for answers and thread_id for tracked QA.\n" + string(payload),
		origin:      origin,
		displayText: body,
		mailIDs:     ids,
	}
}

// inboxDisplay is the origin line and body a transcript shows for a batch:
// one message as itself; several (a review round's results) as "N results"
// with a heading per sender.
func inboxDisplay(batch []swarm.Message, label func(swarm.Message) string) (origin, body string) {
	if len(batch) == 1 {
		return label(batch[0]), batch[0].Body
	}
	parts := make([]string, len(batch))
	for i, msg := range batch {
		parts[i] = "#### " + label(msg) + "\n\n" + msg.Body
	}
	return fmt.Sprintf("%d results", len(batch)), strings.Join(parts, "\n\n")
}
