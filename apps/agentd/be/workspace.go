package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/internal/workspacemcp"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

type workspaceService struct {
	qaMu      sync.Mutex
	qaFiles   map[string]*qaDirState
	planFiles map[string]*planFileState
	store     *swarm.Store
	conn      *sdk.Conn
	mu        sync.Mutex
	tokens    map[string]*hosted
	socket    string
	kick      chan struct{}
	done      chan struct{}
	publishMu sync.Mutex
	views     map[string][]byte
	sequences map[string]int64
	previews  map[string]string
	sessions  map[string]string
	// sup is the stall watchdog's state; only loop touches it.
	sup *supervisor
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
	ws := &workspaceService{store: store, conn: c, tokens: map[string]*hosted{}, socket: socket, kick: make(chan struct{}, 1), done: make(chan struct{}), views: map[string][]byte{}, sequences: map[string]int64{}, previews: map[string]string{}, sessions: map[string]string{}}
	workspaces = ws
	server := &http.Server{Handler: http.HandlerFunc(ws.serve), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("agentd: workspace bridge: %v", err)
		}
	}()
	go ws.loop()
	sdk.OnTerminate(func() { close(ws.done); _ = server.Close(); _ = os.RemoveAll(dir) })
	sdk.HandleFromVoid(bus, "workspace_refresh", func(c *sdk.Conn, _ string, req agentproto.WorkspaceRefresh, from wire.Sender) error {
		if controls(from, req.Key) {
			ws.publish(true)
		}
		return nil
	})
	sdk.HandleFromVoid(bus, "workspace_action", func(c *sdk.Conn, _ string, req agentproto.WorkspaceAction, from wire.Sender) error {
		if !controls(from, req.Key) {
			return nil
		}
		h := lookupHosted(req.Key)
		if h == nil {
			return nil
		}
		// GUI-only operations retain human attribution. Never expose this route in MCP.
		go func() {
			// Only member_inspect's answer is read: the other operations'
			// effects reach the window through the next frame.
			var transcript *agentproto.WorkspaceTranscript
			var err error
			// The operations share the MCP tools' argument parsing, which
			// reads JSON: only the fields the operation set are sent.
			raw, _ := json.Marshal(req.Arguments)
			member := req.Arguments.MemberID
			switch req.Name {
			case "member_open":
				w := ws.store.View(h.sessionID)
				if w == nil {
					err = errors.New("workspace ended")
				} else if m := swarm.GetMember(w, member); m != nil {
					if target := hostedBySession(m.Session); target != nil {
						openHosted(c, target.key)
					} else {
						err = errors.New("member is not running")
					}
				} else {
					err = errors.New("unknown member")
				}
			case "member_resume":
				// member_control is the orchestrator's; the human may be
				// looking at a member's tab.
				lead := h
				if w := ws.store.View(h.sessionID); w != nil {
					lead = hostedBySession(workspaceLeadSession(*w))
				}
				if lead == nil {
					err = errors.New("the orchestrator is not running")
					break
				}
				args, _ := json.Marshal(map[string]any{"action": "resume", "member_ids": []string{member}})
				var result any
				result, err = ws.call(context.Background(), lead, workspacemcp.Call{Name: "member_control", Arguments: args})
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
			case "member_message":
				_, err = ws.humanMessage(h, raw)
			case "member_inspect":
				if member != "" {
					transcript, err = ws.inspect(h, raw)
				}
				if err == nil {
					ws.mu.Lock()
					ws.previews[h.key] = member
					ws.mu.Unlock()
				}
			default:
				err = errors.New("unknown workspace UI operation")
			}
			reply := agentproto.WorkspaceResult{Key: h.key, Operation: req.Name, Transcript: transcript}
			if err != nil {
				reply.Error = err.Error()
			}
			_ = agentproto.Send(c, wire.Recipient{InstanceID: from.InstanceID}, reply)
			ws.signal()
			ws.publish(false)
		}()
		return nil
	})
	return nil
}
func (ws *workspaceService) inject(h *hosted, member bool) error {
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
	env := []acp.EnvVar{{Name: workspacemcp.SocketEnv, Value: ws.socket}, {Name: workspacemcp.TokenEnv, Value: token}}
	if member {
		env = append(env, acp.EnvVar{Name: workspacemcp.MemberEnv, Value: "1"})
	}
	h.mcp = append(h.mcp, acp.McpServer{Name: workspacemcp.ServerName, Command: bin, Args: []string{workspacemcp.Argument}, Env: env})
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
	Node            string `json:"node"`
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
	Workspace       string `json:"workspace_id"`
	Confirm         bool   `json:"confirm"`
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
		// Team is the default: it is what a member or orchestrator re-reads
		// between turns, and full state (configuration, launch snapshots,
		// every live session's adapter options) is several times its size.
		if a.View == "" || a.View == "team" {
			if a.Thread != "" || a.Node != "" || a.IncludeMessages || a.After != "" || a.Limit != 0 {
				return nil, errors.New("view=team (the default) takes no other options; message history needs view=state, threads view=qa")
			}
			return teamView(w), nil
		}
		if a.Thread != "" || a.Node != "" {
			return nil, errors.New("thread_id/node require view=qa")
		}
		qaSummary(w)
		// Finished work is in the team view and the plan; its texts are
		// most of a long run's state (DOC1's was 276 KB).
		for i := range w.Assignments {
			if !w.Assignments[i].Open() {
				w.Assignments[i].Text, w.Assignments[i].Result = firstLine(w.Assignments[i].Text, 80), firstLine(w.Assignments[i].Result, 80)
			}
		}
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
		for _, member := range w.Members {
			if live := hostedBySession(member.Session); live != nil && live.sessionReady.Load() {
				options := live.configsSnapshot()
				for i := range options {
					options[i].Options = append([]acp.ConfigOptionValue(nil), options[i].Options...)
				}
				sessions[member.ID] = map[string]any{"provider": live.agent, "config_options": options}
			}
		}
		return map[string]any{"workspace": w, "approvals": workspaceApprovals(w), "delivery_counts": counts, "activity": activity, "activity_detail": detail, "usage": usage, "message_history_included": a.IncludeMessages, "message_page": messagePage, "sessions": sessions}, nil
	case "workspace_end":
		// Ending with work in flight is a decision, not an accident.
		if a.Workspace == "" && !a.Confirm {
			if w := ws.store.View(sid); w != nil {
				if active := swarm.ActiveNodes(w); len(active) > 0 {
					return nil, fmt.Errorf("nodes still active or reported: %s. Finish or accept them, or end anyway with confirm:true", strings.Join(active, ", "))
				}
			}
		}
		if a.Workspace != "" {
			lead, err := ws.staleLead(sid, a.Workspace)
			if err != nil {
				return nil, err
			}
			sid = lead
		}
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
		size := 0
		for _, m := range w.Messages {
			if !after {
				if m.ID == a.After {
					after = true
				}
				continue
			}
			if m.Recipient == self {
				// Pages are bounded in bytes too, like history pages: DOC1's
				// inbox_read returned 73 KB in one line.
				b, _ := json.Marshal(m)
				if len(out) == a.Limit || len(out) > 0 && size+len(b) > 64<<10 {
					more = true
					break
				}
				size += len(b)
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
	// A retry answers with the id it was given; a flash with the message it
	// created (it once echoed the argument, empty for a flash).
	id := a.ID
	lead := call.Name == "message_retry"
	err = ws.store.Mutate(sid, lead, func(w *swarm.Workspace, m *swarm.Member) error {
		workspaceName, memberName = w.Name, m.Name
		switch call.Name {
		case "flash_message":
			if !slices.Contains([]string{"", "info", "warning", "error"}, a.Level) || len(a.Emoji) > 64 {
				return errors.New("invalid flash options")
			}
			v, err := swarm.AddMessage(w, m.ID, "human", "flash", strings.TrimSpace(a.Emoji+" "+a.Text), "", "", "")
			if err != nil {
				return err
			}
			id = v.ID
			return nil
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
	result := map[string]any{"ok": true, "id": id}
	if call.Name == "flash_message" && ws.conn != nil {
		level := a.Level
		if level == "" {
			level = "info"
		}
		if level == "warning" {
			level = "warn"
		}
		desktop(ws.conn, agentproto.Notify{Key: h.key, Title: workspaceName + " · " + memberName, Body: strings.TrimSpace(a.Emoji + " " + a.Text), Level: level})
	}
	return result, nil
}
func (ws *workspaceService) spawn(ctx context.Context, parent *hosted, id string) (any, error) {
	var member swarm.Member
	var workspaceID string
	err := ws.store.Mutate(parent.sessionID, true, func(w *swarm.Workspace, _ *swarm.Member) error {
		// A relaunch used to force the workspace active, lifting the pause
		// an orchestrator failure put on dispatch while the orchestrator
		// itself stayed paused.
		if w.State != "active" {
			return errors.New("workspace paused; resume the orchestrator first")
		}
		m := swarm.GetMember(w, id)
		if m == nil || m.State != "pending" {
			return errors.New("member is not pending launch")
		}
		member, workspaceID = *m, w.ID
		m.State = "starting"
		return nil
	})
	if err != nil {
		return nil, err
	}

	settings := memberSettings(member)
	child, err := startHostedCapability(settings.Provider, member.Cwd, ws.conn, sessionLaunch{connection: settings.Connection, catalog: member.Catalog, model: member.Model, capability: settings.Capability, member: true, noSubagents: settings.Subagents == "deny"})
	var initialConfigs map[string]string
	if err == nil {
		options := child.configsSnapshot()
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
	// A member's approval defaults to its launcher's: with no `approval` of
	// its own it runs auto-approved exactly when the session that launched
	// it does, so yolo on a workspace is one switch, not one per member
	// (and one the human is told about in every member's transcript).
	// "ask" opts a member out; a reviewer never inherits, as it cannot be
	// granted auto explicitly either.
	launcherYolo := parent.autoApproved()
	autoApprove := settings.Approval == "auto" || settings.Approval == "" && launcherYolo && settings.Capability != "reviewer"
	if autoApprove {
		why := "launched with approval \"auto\""
		if settings.Approval == "" {
			why = "the session that launched this member is auto-approved"
		}
		child.setYolo(true, why)
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
		// The role and the initial task are one message, so the first turn
		// carries both. As two, the role went out alone and its reader took
		// it as the go-ahead: an implementer whose task said "PLAN FIRST, no
		// code yet" was already coding when the task arrived. Queued like
		// every later inbox turn, under the same concurrency limits.
		assignment := ""
		if member.InitialTask != "" {
			by := swarm.GetMember(w, v.Creator)
			if by == nil {
				by = swarm.GetMember(w, w.Lead)
			}
			task, err := swarm.NewAssignment(w, by, v, "", "", member.InitialTask)
			if err != nil {
				return fmt.Errorf("initial task: %w", err)
			}
			initialAssignment = task
			assignment = task.ID
		}
		_, e := swarm.AddMessage(w, v.Creator, v.ID, "instruction", memberBrief(member, assignment), "", assignment, "")
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

// memberSettings is what a member runs with: its launch settings with the
// orchestrator's live adjustments on top, a copy the caller may change. A
// relaunch through spawn once dropped the adjustments, which resume kept.
func memberSettings(m swarm.Member) swarm.AgentProfile {
	settings := *m.LaunchSettings
	settings.Configs = maps.Clone(settings.Configs)
	if settings.Configs == nil {
		settings.Configs = map[string]string{}
	}
	maps.Copy(settings.Configs, m.Adjusted)
	return settings
}

// memberBrief is a member's first message: its role, how a member works
// (the same guide about gives it), and its initial task (assignment), or,
// without one, to wait for it. Members run on cheap models: short,
// numbered, one thing per line.
func memberBrief(m swarm.Member, assignment string) string {
	var b strings.Builder
	b.WriteString(m.Instructions)
	b.WriteString("\n\n## How you work\n\nYou are " + memberRef(m) + ", a member of a Wash workspace")
	if m.Node != "" {
		b.WriteString(", on plan node " + m.Node)
	}
	b.WriteString(". The orchestrator is \"orchestrator\".\n")
	for i, step := range workspacemcp.MemberGuide {
		b.WriteString("\n" + itoa(uint64(i+1)) + ". " + step)
	}
	if m.Handoff != "" {
		b.WriteString("\n\n## Handoff from the member you replace\n\n" + m.Handoff)
	}
	if assignment == "" {
		b.WriteString("\n\nYou have no assignment yet. Do not start work: set waiting with member_update and end your turn. Your assignment arrives as a message.")
		// A plan-mode member with nothing to plan wrote an empty plan and asked
		// to leave plan mode, which woke the orchestrator to approve nothing.
		if memberSettings(m).Configs["mode"] == "plan" {
			b.WriteString(" You are in plan mode: member_update waiting is all that is needed. Do not write a plan or call ExitPlanMode until you have an assignment.")
		}
		return b.String()
	}
	b.WriteString("\n\n## Your assignment (" + assignment + ")\n\nFollow it as written, including any limit it sets on what to do first.\n\n" + m.InitialTask)
	return b.String()
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
		if target := hostedBySession(m.Session); target != nil {
			target.retire()
		}
		return map[string]any{"ended": id}, nil
	}
	target := hostedBySession(m.Session)
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
	isMember := false
	err := ws.store.Mutate(h.sessionID, false, func(w *swarm.Workspace, self *swarm.Member) error {
		isMember = id != w.Lead
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
		cancelQuestionsFor(target.key, ReasonTurnCancelled)
		_, _, err = target.cancelTurn()
	}
	if loading {
		launch := memberLaunch(*w, *m)
		launch.member = isMember
		target, err = resumeHostedCapability(m.Provider, m.Cwd, m.Session, ws.conn, launch)
		if err == nil && m.LaunchSettings != nil {
			// session/load comes back on the adapter's defaults: observed, a
			// resumed Architect on claude-fable-5-1 at effort "default" where
			// its profile said claude-fable-5-1[1m] at high. Reapply the launch
			// settings before the member is available, so no turn runs on the
			// wrong model. Best effort: a setting the adapter no longer offers
			// is logged rather than stranding a resident that loaded fine.
			options := target.configsSnapshot()
			settings := memberSettings(*m)
			skipped, cerr := restoreWorkspaceSession(settings, options, func(id, value string) ([]acp.ConfigOption, error) {
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

// configureMember changes a member's adapter settings, e.g. {"mode":"default"}
// once the orchestrator approves its plan. A live session is changed now; the
// change is recorded either way and reapplied on every resume.
func (ws *workspaceService) configureMember(ctx context.Context, h *hosted, id string, configs map[string]string) (any, error) {
	w := ws.store.View(h.sessionID)
	m := swarm.GetMember(w, id)
	if m == nil || m.LaunchSettings == nil || m.State == "ended" {
		return nil, errors.New("member cannot be configured")
	}
	// The same limits as a launch profile: a reviewer cannot be given a
	// permission mode, whoever asks.
	check := memberSettings(*m)
	maps.Copy(check.Configs, configs)
	if err := swarm.ValidateProfile(check); err != nil {
		return nil, err
	}
	applied := map[string]string{}
	if target := hostedBySession(m.Session); target != nil && target.sessionReady.Load() {
		options := target.configsSnapshot()
		var err error
		applied, err = configureWorkspaceSession(swarm.AgentProfile{Configs: configs}, options, func(cid, value string) ([]acp.ConfigOption, error) {
			res, e := target.client.SetConfigOption(ctx, target.sessionID, cid, value)
			if e == nil {
				target.applyConfigs(res.ConfigOptions)
			}
			return res.ConfigOptions, e
		})
		if err != nil {
			return nil, err
		}
	}
	err := ws.store.Mutate(h.sessionID, true, func(w *swarm.Workspace, _ *swarm.Member) error {
		v := swarm.GetMember(w, id)
		if v == nil || v.State == "ended" {
			return errors.New("member ended")
		}
		if v.Adjusted == nil {
			v.Adjusted = map[string]string{}
		}
		maps.Copy(v.Adjusted, configs)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"applied": applied, "adjusted_configs": configs, "live": len(applied) > 0}, nil
}

// interrupt ends a member's current turn and leaves it available: the gentle
// stop, where pause also halts dispatch until someone resumes it. What the
// turn carried counts as delivered; send the member what to do instead.
func (ws *workspaceService) interrupt(id string, m *swarm.Member) (any, error) {
	target := hostedBySession(m.Session)
	if target == nil || !target.sessionReady.Load() {
		return nil, errors.New("member is not running")
	}
	target.mu.Lock()
	live := target.busy()
	if target.turnLive {
		target.interrupted = true
	}
	mail := target.turnMail
	target.mu.Unlock()
	if !live {
		return map[string]any{"interrupted": false, "reason": "no turn running"}, nil
	}
	cancelAsksFor(target.key, ReasonTurnCancelled)
	cancelQuestionsFor(target.key, ReasonTurnCancelled)
	log.Printf("agentd: workspace interrupt member=%s key=%s", id, target.key)
	_, abandoned, err := target.cancelTurn()
	if err != nil {
		target.mu.Lock()
		target.interrupted = false
		target.mu.Unlock()
		return nil, err
	}
	if !abandoned {
		return map[string]any{"interrupted": true}, nil
	}
	out := map[string]any{"interrupted": true, "abandoned": true, "reason": "the agent did not end its turn within " + cancelDeadline.String() + "; Wash ended it, and queued messages can be delivered again"}
	if len(mail) > 0 {
		out["uncertain"] = mail
		out["reason"] = out["reason"].(string) + ". The turn's messages are uncertain: they may not have reached the member; message_retry them if they matter"
	}
	return out, nil
}

// planExitDenied hands the orchestrator a member's finished plan. The member
// asked to leave plan mode, which is the orchestrator's call; its turn has
// ended, so the plan would otherwise reach nobody (observed: fished out of
// ~/.claude/plans by hand). The full plan goes to a file, since it outgrows a
// report; the question wakes the orchestrator with its start and the path.
//
// A member with no open assignment has nothing to have planned (observed: an
// idle plan-mode member's empty plan woke the orchestrator to approve it), so
// its exit is only logged.
func (ws *workspaceService) planExitDenied(h *hosted, plan string) {
	w := ws.store.View(h.sessionID)
	if w == nil {
		return
	}
	open := false
	for _, m := range w.Members {
		if m.Session != h.sessionID {
			continue
		}
		for _, a := range w.Assignments {
			open = open || a.Member == m.ID && a.Open()
		}
	}
	if !open {
		log.Printf("agentd: workspace plan exit by session=%s without an assignment ignored", h.sessionID)
		return
	}
	path := ""
	if plan = strings.TrimSpace(plan); plan != "" {
		dir := filepath.Join(filepath.Dir(transcriptDir()), "workspace-plans")
		p := filepath.Join(dir, time.Now().Format("20060102-150405")+"-"+safeFileName(h.sessionID)+".md")
		err := os.MkdirAll(dir, 0o700)
		if err == nil {
			err = os.WriteFile(p, []byte(plan+"\n"), 0o600)
		}
		if err == nil {
			path = p
		} else {
			log.Printf("agentd: workspace plan for session=%s not saved: %v", h.sessionID, err)
		}
	}
	_ = ws.store.Mutate(h.sessionID, false, func(w *swarm.Workspace, m *swarm.Member) error {
		if m.ID == w.Lead {
			return nil
		}
		approve := "Approve with member_control {\"action\":\"configure\",\"member_ids\":[\"" + m.ID + "\"],\"configs\":{\"mode\":\"default\"}} and tell it to proceed, or answer with changes."
		body := m.Name + " asked to leave plan mode; wash kept it in plan mode and its turn ended. " + approve
		if path != "" {
			head := m.Name + " finished its plan and asked to leave plan mode; wash kept it in plan mode and its turn ended.\nFull plan: " + path + "\n\n"
			tail := "\n\n" + approve
			room := swarm.ReportLimit - len(head) - len(tail) - len("…")
			excerpt := plan
			if len(excerpt) > room {
				excerpt = strings.ToValidUTF8(excerpt[:max(room, 0)], "") + "…"
			}
			body = head + excerpt + tail
		}
		_, err := swarm.AddMessage(w, m.ID, w.Lead, "question", body, "", "", "")
		return err
	})
	ws.signal()
	ws.publish(false)
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
func (ws *workspaceService) inspect(h *hosted, raw json.RawMessage) (*agentproto.WorkspaceTranscript, error) {
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
	if target := hostedBySession(m.Session); target != nil {
		events := snapshot(target.key)
		if len(events) > 300 {
			events = events[len(events)-300:]
		}
		pending := []agentproto.Ask{}
		if svc != nil {
			for _, ask := range svc.Snapshot().Asks {
				if ask.RowKey == target.key {
					pending = append(pending, ask)
				}
			}
		}
		return &agentproto.WorkspaceTranscript{MemberID: m.ID, Events: events, Asks: pending, Questions: questionsFor(target.key, m.ID)}, nil
	}
	events, err := loadTranscript(m.Session)
	if err != nil {
		return nil, err
	}
	if len(events) > 300 {
		events = events[len(events)-300:]
	}
	if events == nil {
		events = []agentproto.Event{}
	}
	return &agentproto.WorkspaceTranscript{MemberID: m.ID, Events: events, Questions: questionsFor("", m.ID), Note: "Archived conversation; reopen through Agent History to resume."}, nil
}
func (ws *workspaceService) publish(force bool) {
	ws.syncQADocuments()
	ws.syncPlanFiles()
	publishQuestions()
	ws.publishMu.Lock()
	defer ws.publishMu.Unlock()
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
		frame := agentproto.WorkspaceState{Key: key, Workspace: w}
		if w != nil {
			ws.mu.Lock()
			selected := ws.previews[key]
			ws.mu.Unlock()
			if selected != "" && h != nil {
				raw, _ := json.Marshal(workspaceArgs{Member: selected})
				if preview, err := ws.inspect(h, raw); err == nil {
					frame.Preview = preview
				}
			}
			frame.Activity, frame.ActivityDetail, frame.Usage = workspaceRuntime(w)
			frame.Approvals = workspaceApprovals(w)
			frame.Questions = workspaceQuestions(w)
			frame.QAMarkdown = swarm.QAMarkdown(w)
			status := ws.qaDocumentStatus(w)
			frame.QADocumentStatus = &status
			qaSummary(w)
			if w.PlanFile != "" {
				status := ws.planFileStatus(w)
				frame.PlanFileStatus = &status
			}
		}
		// Compared and diffed without a sequence: two frames that say the
		// same thing are the same frame, whichever number each would carry.
		b, _ := json.Marshal(frame)
		if !force && string(ws.views[instance]) == string(b) {
			continue
		}
		sequence := ws.sequences[instance] + 1
		frame.Sequence = sequence
		var outgoing any = frame
		if !force {
			if patch := workspacePatch(ws.views[instance], b, ws.sequences[instance], sequence); patch != nil {
				outgoing = *patch
			}
		}
		if err := agentproto.Send(ws.conn, wire.Recipient{InstanceID: instance}, outgoing); err == nil {
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
	// The Agents window nests members under their orchestrator, and a
	// member joins after its session row already exists. Unchanged views
	// are not resent.
	publishControllerViews()
}
func (ws *workspaceService) loop() {
	// This ticks runtime state, never an agent/model. Mail delivery also has an
	// immediate wake channel. The tick covers provider exits and file retries.
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ws.done:
			return
		case <-ws.kick:
		case <-tick.C:
		}
		ws.dispatch()
		ws.contextNudges()
		ws.supervise(time.Now())
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
			if h := hostedBySession(m.Session); h != nil {
				h.mu.Lock()
				if h.busy() {
					active++
				}
				h.mu.Unlock()
			}
		}
		for _, m := range w.Members {
			h := hostedBySession(m.Session)
			if h == nil {
				continue
			}
			if h.closing.Load() || !h.sessionReady.Load() {
				continue
			}
			if m.Retire {
				if !h.isBusy() {
					if err := ws.store.EndMember(workspaceLeadSession(w), m.ID, false); err == nil {
						h.retire()
					}
				}
				continue
			}
			if m.ID != w.Lead && active >= w.MaxActive {
				continue
			}
			// Claimed before the store is asked, and released if there is
			// nothing: store.Next writes the state file, which must not run
			// under the session's lock, and nothing else may start a turn
			// in between.
			if !h.claim() {
				continue
			}
			batch, err := ws.store.Next(m.Session)
			if err != nil {
				log.Printf("agentd: inbox persist: %v", err)
			}
			if len(batch) == 0 {
				h.release()
				continue
			}
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
			if w.Assignments[i].Open() {
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
			// Pending owner decisions stay recorded, unlike an ended
			// member's: the QA file carries them, and the next workspace
			// that resumes it asks them again.
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, m := range old.Members {
		if m.ID != old.Lead {
			if child := hostedBySession(m.Session); child != nil {
				child.retire()
			}
		}
	}
	ws.syncQADocuments()
	return old, nil
}

// staleLead is the orchestrator session of the workspace id names (in full or
// by a unique prefix of at least 8), for workspace_end to end from another
// session. Only a workspace whose orchestrator is not running in Wash: a lead
// whose reopen failed leaves its workspace paused and holding its QA file,
// and nothing could end it short of editing wash's store by hand. A member
// cannot end workspaces; its own orchestrator decides that.
func (ws *workspaceService) staleLead(caller, id string) (string, error) {
	if own := ws.store.View(caller); own != nil {
		if self := workspaceMember(own, caller); self == nil || self.ID != own.Lead {
			return "", errors.New("orchestrator operation")
		}
		if own.ID == id {
			return caller, nil
		}
	}
	if len(id) < 8 {
		return "", errors.New("workspace_id needs at least 8 characters")
	}
	var found []swarm.Workspace
	for _, w := range ws.store.Snapshot().Workspaces {
		if w.State != "ended" && strings.HasPrefix(w.ID, id) {
			found = append(found, w)
		}
	}
	if len(found) == 0 {
		return "", errors.New("no open workspace with that ID")
	}
	if len(found) > 1 {
		return "", errors.New("workspace_id prefix is ambiguous; give the full ID")
	}
	lead := workspaceLeadSession(found[0])
	if lead == caller {
		return caller, nil
	}
	if lead == "" {
		return "", errors.New("workspace has no orchestrator session")
	}
	if hostedBySession(lead) != nil {
		return "", errors.New("workspace " + found[0].ID + " is running in Wash; its orchestrator ends it")
	}
	return lead, nil
}

func workspaceMember(w *swarm.Workspace, session string) *swarm.Member {
	for i := range w.Members {
		if w.Members[i].Session == session && w.Members[i].State != "ended" {
			return &w.Members[i]
		}
	}
	return nil
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
func (ws *workspaceService) restoreProvenance(session string, events []agentproto.Event) {
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
		e.Kind = agentproto.EventCollaboration
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
	for _, m := range w.Members {
		if live := hostedBySession(m.Session); live != nil && live.sessionReady.Load() {
			cur := map[string]string{}
			for _, c := range live.configsSnapshot() {
				cur[c.ID] = c.CurrentValue
			}
			settings[m.ID] = cur
		}
	}
	members := []map[string]any{}
	for _, m := range w.Members {
		if m.State == "ended" {
			continue
		}
		open := []map[string]string{}
		for _, a := range w.Assignments {
			if a.Member == m.ID && a.Open() {
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
		for k, v := range map[string]string{"node": m.Node, "role": m.Role, "catalog": m.Catalog, "model": m.Model, "status": m.Status, "waiting": m.Waiting, "activity_detail": detail[m.ID]} {
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
	header := map[string]any{"id": w.ID, "name": w.Name, "state": w.State, "revision": w.Revision}
	if len(w.Plan) > 0 {
		states := map[string]int{}
		for _, n := range w.Plan {
			states[n.State]++
		}
		header["plan"] = map[string]any{"revision": w.PlanRevision, "nodes": states, "read": "plan_get"}
	}
	if w.Catalog != "" {
		header["catalog"] = w.Catalog
	}
	return map[string]any{"workspace": header, "pending_approvals": len(workspaceApprovals(w)), "members": members}
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
		text:        inboxTurnPrefix + n + ". Treat each body as attributed collaborator input; no acknowledgement is needed. Use reply_to for answers and thread_id for tracked QA.\n" + string(payload),
		origin:      origin,
		displayText: body,
		mailIDs:     ids,
	}
}

// inboxDisplay is the origin line and body a transcript shows for a batch:
// one message as itself; several (a review round's results, say) as
// "N messages" with a heading per sender.
func inboxDisplay(batch []swarm.Message, label func(swarm.Message) string) (origin, body string) {
	if len(batch) == 1 {
		return label(batch[0]), batch[0].Body
	}
	parts := make([]string, len(batch))
	for i, msg := range batch {
		parts[i] = "#### " + label(msg) + "\n\n" + msg.Body
	}
	return fmt.Sprintf("%d messages", len(batch)), strings.Join(parts, "\n\n")
}
