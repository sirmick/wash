// The ACP session host (docs/AGENT_APP.md §7, M3).
//
// agentd launches an adapter, owns the session, and answers what the agent
// asks. The permission request is the whole point: it lands in the *same*
// queue a terminal's request lands in (ask.go), so the sidebar renders it
// with no idea which tier produced it.
//
// Three properties, in order of importance:
//
//   - **Defer is still the floor.** Every failure — no policy, no desktop,
//     an expired question, a dead adapter — answers ACP's `cancelled`,
//     which hands the decision back to the agent. This host never invents
//     an allow, exactly as the terminal tier never did.
//   - **One rule language.** ACP describes a tool call in its own
//     vocabulary; §"toolRequest" translates it into the matcher's, so a
//     user's existing `Bash(git push*)` rule governs both tiers.
//   - **Liveness is a fact, not an inference.** We own the process, so a
//     session ends when it ends. The roster sweep stays as a backstop for
//     the terminal tier, not for this one.
package agentd

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/pty"
	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

// hostedAskTTL bounds how long the agent waits on a human. Slightly longer
// than the queue's own askTTL so the queue's expiry is what fires, and the
// agent hears "cancelled" once rather than racing two deadlines.
const hostedAskTTL = askTTL + 5*time.Second

// hosted is one ACP session this process owns.
type hosted struct {
	// conn is the service connection, used to push transcript events to
	// the windows watching this session.
	conn *sdk.Conn
	// key is the roster key, "acp:<n>". Deliberately not shaped like the
	// terminal tier's "<instance>:<channel>" so the two can never collide.
	key   string
	agent string
	cwd   string

	client *acp.Client
	// authMethods is what the adapter said it offers, kept only so a
	// failed session call can name them in its error.
	authMethods []acp.AuthMethod
	// sessionID is the agent's own id — what history stores and what
	// session/load resumes.
	sessionID string
	stop      func()
	// used / size are the agent's context accounting; title is its own
	// name for the session. Both arrive as session/update variants that
	// nothing else consumes.
	used, size int64
	title      string
	// modes are the agent's own approval presets, and mode is the one in
	// force. Changing it is ACP's answer to "stop asking me" — the AGENT's
	// setting, visible to it and reversible from either side, rather than a
	// blanket allow wash keeps to itself.
	modes []acp.SessionMode
	mode  string
	// yolo is HOST-side auto-approval: wash answers this session's
	// permission questions with "allow" rather than asking. Deliberately
	// separate from `mode` above, which is the agent's own setting —
	// visible to it and reversible from either side. This one is a blanket
	// allow wash keeps to itself, which is why it is per-session, off by
	// default, announced in the transcript every time it fires, and shown
	// on the roster row rather than hidden in a menu.
	//
	// It replaces ASKING, not deciding: an explicit deny rule still denies.
	// A standing "never let anything run rm -rf" is a decision the user
	// already made, and a convenience toggle must not quietly reverse it.
	yolo bool
	// configs is the agent's generic settings block — model, reasoning
	// effort, plan mode, and whatever an adapter adds. One shape, so one
	// control renders all of them.
	configs []acp.ConfigOption
	// commands are the agent's own slash commands.
	commands []acp.AvailableCommand
	// detached means no window is pointing at this session. It keeps
	// running; the roster row is how the user gets back to it.
	detached bool
}

var (
	hostedMu  sync.Mutex
	hostedAll = map[string]*hosted{}
	hostedSeq uint64
)

// register puts a started session in the registry and on the roster.
func (h *hosted) register() {
	hostedMu.Lock()
	hostedAll[h.key] = h
	hostedMu.Unlock()
	h.setState("running", "")
}

// retire ends a session: off the roster, out of the registry, adapter
// stopped. Safe to call twice.
func (h *hosted) retire() {
	hostedMu.Lock()
	_, live := hostedAll[h.key]
	delete(hostedAll, h.key)
	hostedMu.Unlock()
	if !live {
		return
	}
	if h.stop != nil {
		h.stop()
	}
	dropTranscript(h.key)
	now := time.Now()
	mutateState(func(s *State) {
		delete(rows, h.key)
		s.Rows = publish(now)
		s.Recent = publishHistory()
	})
	saveHistory()
	log.Printf("agentd: acp session ended key=%s agent=%s session=%s", h.key, h.agent, h.sessionID)
}

// setState upserts this session's roster row. Same four wire states the
// terminal tier publishes, so the sidebar cannot tell the tiers apart —
// which is the M3 acceptance criterion.
func (h *hosted) setState(state, reason string) {
	now := time.Now()
	var wantGit string
	var changed bool
	mutateState(func(s *State) {
		r := rows[h.key]
		if r == nil {
			r = &row{}
			rows[h.key] = r
		}
		if r.State != state || r.Reason != reason {
			r.stateSince = now
			changed = true
		}
		r.lastSeen = now
		r.Stale = false
		r.Key = h.key
		r.Agent = h.agent
		r.State = state
		r.Reason = reason
		r.SessionID = h.sessionID
		r.Detached = h.detached
		r.Used, r.Size = h.used, h.size
		r.Title = h.title
		r.Mode, r.Modes = h.mode, publicModes(h.modes)
		r.Yolo = h.yolo
		r.Configs = publicConfigs(h.configs)
		r.Commands = publicCommands(h.commands)
		if h.cwd != "" && h.cwd != r.Cwd {
			r.Cwd = h.cwd
			r.Dir = dirLabel(h.cwd)
			r.Branch, r.Dirty = "", false
		}
		if r.Cwd != "" {
			wantGit = r.Cwd
		}
		if rememberSession(h.agent, h.sessionID, h.cwd, h.title, now) {
			historyDirty = true
		}
		s.Rows = publish(now)
		s.Recent = publishHistory()
	})
	if changed {
		log.Printf("agentd: acp row key=%s agent=%s state=%s session=%s dir=%s",
			h.key, h.agent, state, h.sessionID, dirLabel(h.cwd))
	}
	if wantGit != "" {
		go resolveGit(wantGit)
	}
	if changed {
		// Persist on every state change. Waiting for the session to end
		// meant a detached session — or a reboot — was never remembered.
		saveHistorySoon()
	}
}

// applyModes records what the agent will let us switch between.
func (h *hosted) applyModes(m acp.SessionModes) {
	hostedMu.Lock()
	h.modes = m.AvailableModes
	if m.CurrentModeID != "" {
		h.mode = m.CurrentModeID
	}
	hostedMu.Unlock()
}

// applyConfigs records the agent's settings block. The agent's answer is
// authoritative: setting one option can change another (a model that does
// not support an effort level resets it), so a set replaces the whole
// list rather than patching one entry.
func (h *hosted) applyConfigs(in []acp.ConfigOption) {
	if len(in) == 0 {
		return
	}
	hostedMu.Lock()
	h.configs = in
	hostedMu.Unlock()
	h.republish()
}

// republish refreshes this session's roster row without changing its
// state — used when only the detached flag moved.
func (h *hosted) republish() {
	now := time.Now()
	mutateState(func(s *State) {
		if r := rows[h.key]; r != nil {
			r.Detached = h.detached
			r.Used, r.Size = h.used, h.size
			r.Title = h.title
			r.Mode, r.Modes = h.mode, publicModes(h.modes)
			r.Yolo = h.yolo
			r.Configs = publicConfigs(h.configs)
			r.Commands = publicCommands(h.commands)
			r.Configs = publicConfigs(h.configs)
			r.Commands = publicCommands(h.commands)
			r.lastSeen = now
		}
		s.Rows = publish(now)
	})
}

// ---- acp.SessionHandler ----

// SessionUpdate maps the agent's narration onto the roster. The transcript
// consumes the same notifications in M4; this milestone renders none of
// them, which is what makes it testable without a frontend.
func (h *hosted) SessionUpdate(_ context.Context, n acp.SessionNotification) {
	// The transcript first: it is what the app renders, and it must record
	// what the agent said even for variants the roster ignores.
	if h.conn != nil {
		for _, e := range appendUpdate(h.key, n.Update, time.Now()) {
			pushEvent(h.conn, h.key, e)
		}
	}

	switch n.Update.SessionUpdate {
	case acp.UpdateAgentMessageChunk, acp.UpdateAgentThoughtChunk,
		acp.UpdateToolCall, acp.UpdateToolCallUpdate, acp.UpdatePlan:
		// Anything the agent says or does means it is working. A pending
		// permission overrides this from RequestPermission, and the turn
		// ending overrides it from prompt().
		h.setState("working", "")
	case acp.UpdateUsage:
		if n.Update.Size > 0 || n.Update.Used > 0 {
			hostedMu.Lock()
			h.used, h.size = n.Update.Used, n.Update.Size
			hostedMu.Unlock()
			h.republish()
		}

	case acp.UpdateCurrentMode:
		// The agent can change its own mode (a slash command, its own
		// policy), so the UI follows the wire rather than assuming the
		// last set_mode stuck.
		if n.Update.ModeID != "" {
			hostedMu.Lock()
			h.mode = n.Update.ModeID
			hostedMu.Unlock()
			h.republish()
		}

	case acp.UpdateConfigOption:
		// The agent changed a setting itself; follow the wire.
		if len(n.Update.ConfigOptions) > 0 {
			h.applyConfigs(n.Update.ConfigOptions)
		}

	case acp.UpdateAvailableCommands:
		hostedMu.Lock()
		h.commands = n.Update.AvailableCommands
		hostedMu.Unlock()
		h.republish()

	case acp.UpdateSessionInfo:
		// The agent names its own session once it works out what the work
		// is — which is the summary a human would otherwise have to write.
		// Nothing extra is asked of any model for this; it arrives.
		// FIRST title wins. Verified against codex-acp 1.1.9 that it does
		// not re-title on later turns — but an adapter that did would
		// otherwise rename the window and the history entry after every
		// exchange, which is precisely what makes a name useless. The
		// session is named for what it set out to do.
		hostedMu.Lock()
		fresh := h.title == "" && n.Update.Title != ""
		if fresh {
			h.title = n.Update.Title
		}
		hostedMu.Unlock()
		if fresh {
			h.republish()
			// Remembered immediately: a title that only reached the
			// history when the session ended would be missing from
			// exactly the sessions you most want to find again.
			mutateState(func(s *State) {
				if rememberSession(h.agent, h.sessionID, h.cwd, h.title, time.Now()) {
					historyDirty = true
				}
				s.Recent = publishHistory()
			})
			saveHistorySoon()
		}

	case "":
		// A variant that did not decode. Logged rather than dropped: on a
		// protocol under active development this is the early warning
		// that a payload shape moved (AGENT_APP.md §12b).
		log.Printf("agentd: acp update undecoded key=%s raw=%s", h.key, truncate(n.Update.Raw, 200))
	}
}

// RequestPermission is the reason this file exists.
//
// Order: policy first (an allow/deny rule answers without troubling
// anyone), then the human via the shared queue, then defer. The agent is
// blocked throughout, which is why every branch below terminates.
func (h *hosted) RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	pol := hostedPolicy()
	preq := toolRequest(req.ToolCall, h.cwd)
	res := agentpolicy.Evaluate(pol, preq)

	switch res.Decision {
	case agentpolicy.DecisionAllow:
		log.Printf("agentd: acp decide key=%s tool=%s decision=allow rule=%q", h.key, preq.ToolName, res.Rule)
		return pick(req.Options, acp.OptionAllowOnce, acp.OptionAllowAlways), nil
	case agentpolicy.DecisionDeny:
		log.Printf("agentd: acp decide key=%s tool=%s decision=deny rule=%q", h.key, preq.ToolName, res.Rule)
		return pick(req.Options, acp.OptionRejectOnce, acp.OptionRejectAlways), nil
	}

	// Host-side yolo: the user asked wash to stop asking. Checked AFTER the
	// policy, never before — an explicit deny rule is a decision the user
	// already made, and a convenience toggle must not quietly reverse it.
	// Announced in the transcript every time, because an agent that is
	// being auto-approved must not look like one that is being watched.
	hostedMu.Lock()
	yolo := h.yolo
	hostedMu.Unlock()
	if yolo {
		subject := agentpolicy.ToolSubject(preq.ToolName, preq.ToolInput)
		log.Printf("agentd: acp decide key=%s tool=%s decision=allow reason=yolo subject=%q",
			h.key, preq.ToolName, subject)
		if h.conn != nil {
			e := appendPrompt(h.key, "Auto-approved (yolo): "+preq.ToolName+" "+subject, time.Now())
			e.Kind = EventMessage
			pushEvent(h.conn, h.key, e)
		}
		return pick(req.Options, acp.OptionAllowOnce, acp.OptionAllowAlways), nil
	}

	// No rule: ask the human.
	//
	// **Asking is the floor for a hosted session, not an opt-in.** The
	// terminal tier could safely decline to answer because deferring
	// returned control to an agent with its own prompt in a pty. A hosted
	// session has no such UI — wash IS the UI — so declining means the
	// tool silently never runs and the turn ends. Observed on the first
	// real Codex session: `decision=defer reason="policy off"` followed
	// immediately by `state=done`, with nothing on screen to explain it.
	//
	// So an absent policy file no longer disables asking here. A policy
	// that exists and explicitly turns ask_desktop off still means what it
	// says, and "nobody home" (§ask.go) is still a defer — that one is
	// unavoidable, and the agent hears `cancelled` rather than waiting on
	// a desktop that is not attached.
	if pol.Enabled && !pol.AskDesktopOrDefault() {
		log.Printf("agentd: acp decide key=%s tool=%s decision=defer reason=%q subject=%q",
			h.key, preq.ToolName, "ask_desktop off", agentpolicy.ToolSubject(preq.ToolName, preq.ToolInput))
		return acp.Cancelled(), nil
	}

	h.setState("needs-input", "permission")
	defer h.setState("working", "")

	answer := make(chan string, 1)
	queued := enqueueAsk(askSpec{
		Agent:          h.agent,
		Tool:           preq.ToolName,
		Subject:        agentpolicy.ToolSubject(preq.ToolName, preq.ToolInput),
		Cwd:            h.cwd,
		RowKey:         h.key,
		SourceApp:      AppID,
		SourceInstance: "",
	}, func(decision, _ string) error {
		select {
		case answer <- decision:
		default:
		}
		return nil
	})
	if !queued {
		// enqueueAsk already answered with defer; drain it so the channel
		// send above cannot leak, then hand the decision back.
		return acp.Cancelled(), nil
	}

	select {
	case <-ctx.Done():
		return acp.Cancelled(), nil
	case d := <-answer:
		switch d {
		case DecisionAllow:
			return pick(req.Options, acp.OptionAllowOnce, acp.OptionAllowAlways), nil
		case DecisionDeny:
			return pick(req.Options, acp.OptionRejectOnce, acp.OptionRejectAlways), nil
		}
		return acp.Cancelled(), nil
	case <-time.After(hostedAskTTL):
		return acp.Cancelled(), nil
	}
}

// pick chooses the option to select for a decision, preferring the
// one-shot kind over the durable one.
//
// wash deliberately never picks `allow_always`: "remember this" is already
// recorded on OUR side, as a rule in agents.json written by the answer
// handler. Selecting the agent's durable option too would put the same
// consent in two places that can then disagree — and only one of them is
// visible in the Agents pane.
//
// An agent that offered neither kind gets `cancelled`, which is honest:
// we could not express the answer in the options it gave us.
func pick(options []acp.PermissionOption, want, fallback string) acp.RequestPermissionResponse {
	for _, o := range options {
		if o.Kind == want {
			return acp.Selected(o.OptionID)
		}
	}
	for _, o := range options {
		if o.Kind == fallback {
			return acp.Selected(o.OptionID)
		}
	}
	return acp.Cancelled()
}

// toolRequest translates ACP's description of a tool call into the
// matcher's vocabulary, so one rule file governs both tiers and a user's
// existing `Bash(git push*)` keeps working when the session moves to ACP.
//
// Lossy on purpose: ACP classifies by *kind* (what sort of thing this is)
// where the rule language names a *tool* (Claude Code's own names, which
// the syntax was built to mirror). The mapping is the obvious one, and an
// unmapped kind becomes a tool name no rule will match — so a new ACP kind
// can only ever fall through to asking, never to allowing.
func toolRequest(tc acp.ToolCall, cwd string) agentpolicy.Request {
	name := map[string]string{
		acp.ToolKindExecute: "Bash",
		acp.ToolKindRead:    "Read",
		acp.ToolKindEdit:    "Edit",
		acp.ToolKindDelete:  "Delete",
		acp.ToolKindMove:    "Move",
		acp.ToolKindSearch:  "Grep",
		acp.ToolKindFetch:   "WebFetch",
	}[tc.Kind]
	if name == "" {
		// Unmapped kind: a tool name no rule can match, so a new ACP
		// kind falls through to asking rather than to allowing.
		name = "Acp:" + tc.Kind
	}

	// The subject is what a rule's pattern matches. rawInput is the real
	// thing when the adapter sends it; the title is a human-facing string
	// and only a fallback.
	input := map[string]any{}
	if len(tc.RawInput) > 0 {
		_ = json.Unmarshal(tc.RawInput, &input)
	}
	if agentpolicy.ToolSubject(name, input) == "" && tc.Title != "" {
		input = map[string]any{subjectKeyFor(name): tc.Title}
	}
	return agentpolicy.Request{ToolName: name, ToolInput: input, Cwd: cwd}
}

// subjectKeyFor is the input key ToolSubject reads for a given tool, used
// when falling back to the title.
func subjectKeyFor(tool string) string {
	switch tool {
	case "Read", "Edit", "Delete", "Move":
		return "file_path"
	case "Grep":
		return "pattern"
	case "WebFetch":
		return "url"
	}
	return "command"
}

// hostedPolicy reads the rule file fresh. One stat per permission request
// is not a cost worth a stale answer — this is the moment a user is
// watching for a rule to take effect (AGENT_TERM §9.6).
// Indirected like the queue's state hooks, so the decision logic above is
// reachable in a unit test without a policy file on disk.
var hostedPolicy = func() agentpolicy.Policy {
	return agentpolicy.Load(agentpolicy.Path())
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// ---- bus verbs ----

// registerACPHandlers installs the verbs the agent app drives. They exist
// in M3, before the app does, because the milestone's acceptance is that
// the *backend* works — a Codex permission request reaching the sidebar
// with no frontend change at all.
func registerACPHandlers(bus *sdk.Bus, svcConn *sdk.Conn) {
	// agent_start: launch an adapter and open a session.
	sdk.HandleFromVoid(bus, "agent_start", func(conn *sdk.Conn, _ string, req startReq, from wire.Sender) error {
		h, err := startHosted(req.Agent, req.Cwd, svcConn)
		if err != nil {
			log.Printf("agentd: acp start agent=%s cwd=%s: %v", req.Agent, req.Cwd, err)
			if from.InstanceID != "" {
				return conn.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{
					"kind":   "agent_started",
					"error":  err.Error(),
					"req_id": req.ReqID,
				})
			}
			return nil
		}
		if req.Prompt != "" {
			go promptHosted(h, req.Prompt)
		}
		if from.InstanceID == "" {
			return nil
		}
		return conn.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{
			"kind":       "agent_started",
			"key":        h.key,
			"session_id": h.sessionID,
			"req_id":     req.ReqID,
		})
	})

	// agent_prompt: another turn on a live session.
	sdk.HandleFromVoid(bus, "agent_prompt", func(_ *sdk.Conn, _ string, req promptReq, _ wire.Sender) error {
		h := lookupHosted(req.Key)
		if h == nil {
			log.Printf("agentd: acp prompt for unknown session key=%s", req.Key)
			return nil
		}
		go promptHosted(h, req.Text)
		return nil
	})

	// agent_detach: the window closed but the session is to keep running.
	// The roster row stays and gains a Reattach affordance.
	sdk.HandleFromVoid(bus, "agent_detach", func(_ *sdk.Conn, _ string, req promptReq, _ wire.Sender) error {
		h := lookupHosted(req.Key)
		if h == nil {
			return nil
		}
		hostedMu.Lock()
		h.detached = true
		hostedMu.Unlock()
		log.Printf("agentd: acp detached key=%s agent=%s session=%s", h.key, h.agent, h.sessionID)
		h.republish()
		return nil
	})

	// agent_reattach: open a window onto a session that is still running.
	sdk.HandleFromVoid(bus, "agent_reattach", func(conn *sdk.Conn, _ string, req promptReq, _ wire.Sender) error {
		h := lookupHosted(req.Key)
		if h == nil {
			return nil
		}
		pendingAttachMu.Lock()
		pendingAttach = append(pendingAttach, h.key)
		pendingAttachMu.Unlock()
		if err := conn.SpawnRequest(aiAppID); err != nil {
			log.Printf("agentd: reattach spawn key=%s: %v", h.key, err)
			popAttach()
			return nil
		}
		hostedMu.Lock()
		h.detached = false
		hostedMu.Unlock()
		h.republish()
		return nil
	})

	// agent_pty_spike: SPIKE ONLY (docs/AGENT_TERMINAL.md §4). Opens a pty
	// from this background service — no window, WindowID()==0 — and logs
	// the channel id, so a test can mount that channel from the page and
	// prove a windowless app's pty reaches the shell. If it does, agentd
	// can own ACP terminals; if not, the design changes. Delete once M2
	// replaces it with the real CreateTerminal.
	sdk.HandleVoid(bus, "agent_pty_spike", func(c *sdk.Conn, _ string, _ struct{}) error {
		go func() {
			sess, err := pty.Open(context.Background(), c, 0, 80, 24,
				[]string{"sh", "-c", "echo wash-spike-ok; sleep 5"}, nil,
				func(_ *pty.Session, reason string) {
					log.Printf("agentd: pty spike closed reason=%s", reason)
				})
			if err != nil {
				log.Printf("agentd: pty spike FAILED: %v", err)
				return
			}
			log.Printf("agentd: pty spike channel=%d", sess.ID())
		}()
		return nil
	})

	// agent_set_yolo: turn HOST-side auto-approval on or off for one
	// session. Not persisted and not global — it dies with the session, so
	// "yolo for this one job" cannot silently become how the desktop
	// behaves tomorrow. The transition itself is announced in the
	// transcript, so the record shows when the guard came off.
	sdk.HandleFromVoid(bus, "agent_set_yolo", func(_ *sdk.Conn, _ string, req yoloReq, _ wire.Sender) error {
		h := lookupHosted(req.Key)
		if h == nil {
			return nil
		}
		hostedMu.Lock()
		changed := h.yolo != req.On
		h.yolo = req.On
		hostedMu.Unlock()
		if !changed {
			return nil
		}
		log.Printf("agentd: acp yolo key=%s on=%v", h.key, req.On)
		if h.conn != nil {
			msg := "Auto-approval (yolo) is OFF — wash will ask before tools run."
			if req.On {
				msg = "Auto-approval (yolo) is ON — wash will approve tool requests without asking."
			}
			e := appendPrompt(h.key, msg, time.Now())
			e.Kind = EventMessage
			pushEvent(h.conn, h.key, e)
		}
		h.republish()
		return nil
	})

	// agent_set_mode: switch the session's approval preset.
	sdk.HandleFromVoid(bus, "agent_set_mode", func(_ *sdk.Conn, _ string, req modeReq, _ wire.Sender) error {
		h := lookupHosted(req.Key)
		if h == nil || req.Mode == "" {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := h.client.SetMode(ctx, h.sessionID, req.Mode); err != nil {
			log.Printf("agentd: acp set_mode key=%s mode=%s: %v", h.key, req.Mode, err)
			return nil
		}
		// Optimistic: current_mode_update confirms it if the agent sends
		// one, and this is what the UI shows meanwhile.
		hostedMu.Lock()
		h.mode = req.Mode
		hostedMu.Unlock()
		h.republish()
		log.Printf("agentd: acp mode key=%s mode=%s", h.key, req.Mode)
		return nil
	})

	// agent_set_config: change one of the agent's own settings (model,
	// reasoning effort, plan mode, …).
	sdk.HandleFromVoid(bus, "agent_set_config", func(_ *sdk.Conn, _ string, req configReq, _ wire.Sender) error {
		h := lookupHosted(req.Key)
		if h == nil || req.ID == "" {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res, err := h.client.SetConfigOption(ctx, h.sessionID, req.ID, req.Value)
		if err != nil {
			log.Printf("agentd: acp set_config key=%s id=%s value=%s: %v", h.key, req.ID, req.Value, err)
			return nil
		}
		log.Printf("agentd: acp config key=%s %s=%s", h.key, req.ID, req.Value)
		h.applyConfigs(res.ConfigOptions)
		return nil
	})

	// agent_cancel: abort the running turn. A notification, not a request:
	// the agent acknowledges by ending the turn with stopReason=cancelled,
	// which promptHosted already handles. Without this there is no stop
	// button at all — a runaway turn could only be waited out or killed
	// along with its session.
	sdk.HandleFromVoid(bus, "agent_cancel", func(_ *sdk.Conn, _ string, req promptReq, _ wire.Sender) error {
		h := lookupHosted(req.Key)
		if h == nil {
			return nil
		}
		log.Printf("agentd: acp cancel key=%s session=%s", h.key, h.sessionID)
		return h.client.Cancel(h.sessionID)
	})

	// agent_stop: end a session and its adapter.
	sdk.HandleFromVoid(bus, "agent_stop", func(_ *sdk.Conn, _ string, req promptReq, _ wire.Sender) error {
		if h := lookupHosted(req.Key); h != nil {
			h.retire()
		}
		return nil
	})
}

type startReq struct {
	Agent  string `json:"agent"`
	Cwd    string `json:"cwd"`
	Prompt string `json:"prompt,omitempty"`
	// ReqID is opaque to agentd and echoed back on agent_started, success
	// or failure. A host with ONE session per process (wash-ai) never needs
	// it — the reply can only be about the one thing it asked for. A host
	// with several (wash-edit's agent tabs) cannot tell two concurrent
	// starts apart without it, and a FAILED start carries no key at all, so
	// there would be nothing to attribute the error to.
	ReqID string `json:"req_id,omitempty"`
}

// Elicit answers elicitation/create — the agent asking the HUMAN a
// structured question ("which of these?"), not permission ("may I?").
//
// Answering properly needs a form renderer for the requested JSON schema,
// which does not exist yet. So this shows the question in the transcript
// and declines: the human at least SEES what was asked, and the agent
// learns the answer was no rather than that the client is broken.
//
// The alternative — not implementing it at all — returns -32601 and lets
// the agent ask in prose instead, which for some agents is a better
// outcome. That is the trade being made here, and it should be revisited
// the moment a form renderer exists.
func (h *hosted) Elicit(_ context.Context, req acp.ElicitRequest) (acp.ElicitResponse, error) {
	log.Printf("agentd: acp elicitation key=%s message=%q (declining — no form renderer)", h.key, req.Message)
	if h.conn != nil {
		e := appendPrompt(h.key, "The agent asked: "+req.Message+"\n(wash cannot answer structured questions yet, so it declined.)", time.Now())
		e.Kind = EventMessage
		pushEvent(h.conn, h.key, e)
	}
	return acp.ElicitResponse{Action: acp.ElicitDecline}, nil
}

type yoloReq struct {
	Key string `json:"key"`
	On  bool   `json:"on"`
}

type configReq struct {
	Key   string `json:"key"`
	ID    string `json:"id"`
	Value string `json:"value"`
}

// publicConfigs / publicCommands copy for the wire (copy-on-write: a
// snapshot may outlive this call).
func publicConfigs(in []acp.ConfigOption) []Config {
	if len(in) == 0 {
		return nil
	}
	out := make([]Config, 0, len(in))
	for _, o := range in {
		vals := make([]ConfigValue, 0, len(o.Options))
		for _, v := range o.Options {
			vals = append(vals, ConfigValue{Value: v.Value, Name: v.Name, Description: v.Description})
		}
		out = append(out, Config{ID: o.ID, Name: o.Name, Description: o.Description, Current: o.CurrentValue, Values: vals})
	}
	return out
}

func publicCommands(in []acp.AvailableCommand) []Command {
	if len(in) == 0 {
		return nil
	}
	out := make([]Command, 0, len(in))
	for _, c := range in {
		out = append(out, Command{Name: c.Name, Description: c.Description})
	}
	return out
}

type modeReq struct {
	Key  string `json:"key"`
	Mode string `json:"mode"`
}

// publicModes copies the mode list for the wire (copy-on-write: a
// snapshot may outlive this call).
func publicModes(in []acp.SessionMode) []Mode {
	if len(in) == 0 {
		return nil
	}
	out := make([]Mode, 0, len(in))
	for _, m := range in {
		out = append(out, Mode{ID: m.ID, Name: m.Name, Description: m.Description})
	}
	return out
}

type promptReq struct {
	Key  string `json:"key"`
	Text string `json:"text,omitempty"`
}
