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
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/pty"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// stderrTailBytes is how much of the adapter's stderr a session keeps for
// the moment it dies: enough for the stack trace's last lines or the
// "not logged in" it printed on the way out, small enough to sit on every
// session for its lifetime.
const stderrTailBytes = 2048

// hostedAskTTL bounds how long the agent waits on a human. Slightly longer
// than the queue's own ceiling so the queue's expiry is what fires, and the
// agent hears "cancelled" once rather than racing two deadlines.
//
// It tracks askHardTTL rather than askTTL because askTTL now pauses while
// no desktop is attached (§ask.go). Keeping this at askTTL+5s would put the
// backstop *inside* the pause and hand the agent a cancel at 35 seconds
// regardless — the exact behaviour the pause exists to remove.
const hostedAskTTL = askHardTTL + 5*time.Second

// reasonAskOff is the hosted tier's own defer reason, alongside ask.go's
// three. Kept distinct because it is the one refusal the user configured
// on purpose, and telling them "nobody answered" would be a lie.
const reasonAskOff = "ask_desktop off"

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
	// userTitle is the person's name for the session (session_admin.go).
	// It wins over title wherever the title is shown; title is kept so
	// clearing it falls back to the agent's own.
	userTitle string
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
	// toolKinds retains the kind from a tool's opening notification. ACP
	// completion updates commonly omit it; without this, completed reads
	// would be mistaken for mutations and needlessly invalidate Git.
	toolKinds map[string]string
	// detached means no window is pointing at this session. It keeps
	// running; the roster row is how the user gets back to it.
	detached bool
	// closing is set the moment retire starts, before the adapter is
	// killed, so the exit watcher can tell "we ended it" from "it died".
	closing atomic.Bool
	// tail is the adapter's last stderr bytes (see stderrTail).
	tailMu sync.Mutex
	tail   []byte
	// exited is closed when watchExit has finished its cleanup, and idle
	// receives one value each time the turn goroutine returns. Both nil in
	// production (nothing waits); tests set them so they can wait for the
	// goroutines rather than poll their side effects — and so nothing of a
	// test's session outlives the test.
	exited chan struct{}
	idle   chan struct{}

	// turnMu guards turnLive, and — crucially — is held ACROSS the
	// state write that depends on it, so the two orderings below cannot
	// interleave.
	//
	// The ACP conn delivers a response straight from the read loop while
	// notifications go through an ordering queue, so the response that
	// ends a turn can (and in practice does) overtake the tail of that
	// turn's own session/update stream. Since "the agent said something"
	// means working, those late chunks used to flip the row back to
	// working AFTER the turn had finished — the session then sat on
	// "working…" with a Stop button forever, until the next turn.
	//
	// So working is only inferred from narration while a turn is
	// actually open. Late chunks still land in the transcript; they just
	// no longer claim the agent is busy.
	turnMu   sync.Mutex
	turnLive bool
	// pending are prompts typed while a turn was open, in order. They run
	// one after another when the turn ends — messenger semantics — rather
	// than as concurrent session/prompt calls, which the protocol does not
	// allow and which flipped beginTurn/endTurn out of order. Guarded by
	// turnMu; queued mirrors len(pending) for the roster row, readable
	// without the lock (setState runs UNDER turnMu from begin/endTurn).
	pending []turn
	queued  atomic.Int32
	// extraRoots are folders allowed beyond cwd (roots.go). Guarded by
	// hostedMu like everything else a roster push reads.
	extraRoots []string
	// mcp are the MCP servers this session was opened with (agents.json).
	// Held so a RESUME offers the same set: session/load takes the list
	// too, and a resumed session that silently lost its tools is worse
	// than one that never had them.
	mcp []acp.McpServer
}

// turn is one submitted prompt: what was typed, plus whatever was attached
// to it. Attachments ride WITH the text rather than as a prompt of their
// own — a screenshot with "what is wrong here?" is one message, and
// splitting it into two turns would make the agent answer the first
// without the second.
type turn struct {
	text   string
	blocks []acp.ContentBlock
}

// empty reports a turn with nothing in it, which is what the queue drain
// stops on.
func (t turn) empty() bool { return t.text == "" && len(t.blocks) == 0 }

// submitPrompt is the one entry for a prompt on a live session. Inside a
// turn it is queued and the row says so; otherwise it claims the turn
// under the lock and runs. Claiming here — not in beginTurn — is what
// stops two prompts arriving in the same instant from both seeing a
// closed turn and both starting one.
func (h *hosted) submitPrompt(t turn) (queued bool) {
	h.turnMu.Lock()
	if h.turnLive {
		h.pending = append(h.pending, t)
		h.queued.Store(int32(len(h.pending)))
		h.turnMu.Unlock()
		log.Printf("agentd: acp prompt queued key=%s queued=%d", h.key, len(h.pending))
		h.republish()
		return true
	}
	h.turnLive = true
	h.turnMu.Unlock()
	go func() {
		if h.idle != nil {
			defer func() { h.idle <- struct{}{} }()
		}
		for next := t; !next.empty(); {
			next = promptHosted(h, next)
		}
	}()
	return false
}

// beginTurn opens a turn: narration counts as "working" from here.
func (h *hosted) beginTurn() {
	h.turnMu.Lock()
	defer h.turnMu.Unlock()
	h.turnLive = true
	h.setState("working", "")
}

// endTurn closes a turn and records how it ended. Holding turnMu across
// the write is what makes it final: a SessionUpdate racing this either
// runs entirely before (and is overwritten here) or sees a closed turn.
//
// It returns the next queued prompt, if the turn ended in a way that
// should run one: a turn that finished normally hands over to the next
// message with the turn still claimed (so nothing can slip in between,
// and the row does not flash done→working). A turn that failed or was
// stopped drops the queue — the error would repeat, and Stop means stop
// — and the transcript lists what was dropped so nothing typed is lost
// from view.
func (h *hosted) endTurn(state, reason string) (next turn) {
	h.turnMu.Lock()
	defer h.turnMu.Unlock()
	clean := state == "done" && reason != "cancelled"
	if clean && len(h.pending) > 0 {
		next, h.pending = h.pending[0], h.pending[1:]
		h.queued.Store(int32(len(h.pending)))
		h.setState("working", "")
		return next
	}
	dropped := h.pending
	h.pending = nil
	h.queued.Store(0)
	h.turnLive = false
	h.setState(state, reason)
	if len(dropped) > 0 {
		why := "the error"
		if reason == "cancelled" {
			why = "Stop"
		}
		text := "Dropped " + itoa(uint64(len(dropped))) + " queued prompt(s) after " + why + ":"
		for _, d := range dropped {
			text += "\n> " + strings.ReplaceAll(d.text, "\n", "\n> ")
		}
		log.Printf("agentd: acp prompts dropped key=%s n=%d reason=%s", h.key, len(dropped), reason)
		h.note(text)
	}
	return turn{}
}

// narrated reports that the agent said or did something. It only moves the
// row to working inside an open turn.
func (h *hosted) narrated() {
	h.turnMu.Lock()
	defer h.turnMu.Unlock()
	if h.turnLive {
		h.setState("working", "")
	}
}

var (
	hostedMu  sync.Mutex
	hostedAll = map[string]*hosted{}
	hostedSeq uint64
)

// claimDetached atomically reserves a detached session for one reattach.
// Browser dblclick dispatches two click events before its dblclick event;
// frontend guards improve the interaction, but this service is the final
// authority that prevents two Agent windows from being spawned.
func claimDetached(key string) *hosted {
	hostedMu.Lock()
	defer hostedMu.Unlock()
	h := hostedAll[key]
	if h == nil || !h.detached {
		return nil
	}
	h.detached = false
	return h
}

// restoreDetached makes a failed reattach actionable again in the rail.
func restoreDetached(key string) {
	hostedMu.Lock()
	h := hostedAll[key]
	if h != nil {
		h.detached = true
	}
	hostedMu.Unlock()
	if h != nil {
		h.republish()
	}
}

// register puts a started session in the registry and on the roster.
func (h *hosted) register() {
	hostedMu.Lock()
	hostedAll[h.key] = h
	hostedMu.Unlock()
	h.setState("running", "")
}

// retire ends a session: off the roster, out of the registry, adapter
// stopped. Safe to call twice.
//
// "Ends" means everything the session owns, in this order:
//
//  1. its pending questions — answered cancelled toward the agent while
//     it can still hear, and off every rail that was showing them;
//  2. the adapter, as a process group, so an `npx` wrapper's node child
//     does not outlive the adapter it wrapped;
//  3. the terminals it created, which have no agent left to release them.
//
// Killing only the adapter (what this did before) left the rail asking a
// question for a dead session, "Always allow" writing a rule for it, and
// any `sleep 600` the agent had started still running with its channel
// mounted in a transcript nobody could act on.
func (h *hosted) retire() {
	hostedMu.Lock()
	_, live := hostedAll[h.key]
	delete(hostedAll, h.key)
	hostedMu.Unlock()
	if !live {
		return
	}
	h.closing.Store(true)
	h.releaseOwned(ReasonSessionEnded)
	forgetTranscriptWatchers(h.key)
	// Seal the history entry before the events are freed: the count comes
	// from the in-memory transcript, which is about to go.
	h.noteSession("ended", time.Now())
	// The conversation is on disk, so it no longer has to be held in
	// memory. Nothing used to free these: `trans` grew for the router's
	// lifetime, which on a long-lived box is every transcript it ever saw.
	releaseTranscript(h.key)
	now := time.Now()
	mutateState(func(s *State) {
		delete(rows, h.key)
		s.Rows = publish(now)
		s.Recent = publishHistory()
	})
	saveHistory()
	log.Printf("agentd: acp session ended key=%s agent=%s session=%s", h.key, h.agent, h.sessionID)
}

// stderrTail is an io.Writer that keeps the last stderrTailBytes of what
// the adapter wrote to stderr.
func (h *hosted) stderrTail() *tailWriter { return &tailWriter{h: h} }

type tailWriter struct{ h *hosted }

func (w *tailWriter) Write(p []byte) (int, error) {
	w.h.tailMu.Lock()
	w.h.tail = append(w.h.tail, p...)
	if over := len(w.h.tail) - stderrTailBytes; over > 0 {
		w.h.tail = append([]byte(nil), w.h.tail[over:]...)
	}
	w.h.tailMu.Unlock()
	return len(p), nil
}

// stderrText is the kept tail, trimmed for a transcript note.
func (h *hosted) stderrText() string {
	h.tailMu.Lock()
	defer h.tailMu.Unlock()
	return strings.TrimSpace(string(h.tail))
}

// watchExit is the per-session goroutine that turns an adapter exit into
// a fact the desktop can see. Nothing used to select on client.Done()
// outside acpterm: a crashed adapter kept its roster row and its
// idle-hold, its pending question outlived it, and the next prompt failed
// with nothing on screen to say why.
//
// On exit — unless retire already claimed the session, in which case the
// exit is ours — the row goes to failed/exited (it lingers on the roster
// for the sweep's dropAfter, then goes), the transcript gets a note with
// the reason and the adapter's last stderr lines, the pending asks are
// cancelled and the terminals closed, and the session leaves the
// registry. The HISTORY entry and the transcript file are kept as they
// are, so the row in History remains something to resume.
func (h *hosted) watchExit() {
	if h.exited != nil {
		defer close(h.exited)
	}
	if h.client == nil {
		return
	}
	<-h.client.Done()
	if h.closing.Load() {
		return
	}
	hostedMu.Lock()
	live := hostedAll[h.key] == h
	if live {
		delete(hostedAll, h.key)
	}
	hostedMu.Unlock()
	if !live {
		return
	}
	h.closing.Store(true)
	err := h.client.Err()
	tail := h.stderrText()
	log.Printf("agentd: acp adapter exited key=%s agent=%s session=%s err=%v stderr=%q",
		h.key, h.agent, h.sessionID, err, truncate([]byte(tail), 300))

	// The row first, so the status line changes colour before the note
	// lands; then the note, which is what explains the colour.
	h.endTurn("failed", "exited")
	text := "The agent exited unexpectedly"
	if err != nil && err != acp.ErrClosed {
		text += " (" + err.Error() + ")"
	}
	text += "."
	if tail != "" {
		text += "\n\nIts last output:\n```\n" + tail + "\n```"
	}
	text += "\n\nThis session can be reopened from History."
	h.note(text)

	h.releaseOwned(ReasonAgentExited)
	h.noteSession("exited", time.Now())
	releaseTranscript(h.key)
	// The history write happens INSIDE the state lock: this goroutine is
	// not the bus goroutine, and the history slice and its dirty flag are
	// otherwise only touched from there or under Mutate.
	mutateState(func(s *State) {
		s.Recent = publishHistory()
		saveHistory()
	})
}

// releaseOwned cancels the session's questions, stops its adapter and
// closes its terminals — the part of ending a session that is the same
// whether a human ended it or the adapter died under it.
func (h *hosted) releaseOwned(why string) {
	if n := cancelAsksFor(h.key, why); n > 0 {
		log.Printf("agentd: acp session %s key=%s asks_cancelled=%d", why, h.key, n)
	}
	if h.stop != nil {
		h.stop()
	}
	if n := closeTerminalsFor(h.key, why); n > 0 {
		log.Printf("agentd: acp session %s key=%s terminals_closed=%d", why, h.key, n)
	}
}

// stopAllHosted is the shutdown sweep, registered with sdk.OnTerminate:
// when agentd itself goes down — the router's SIGTERM, or its connection
// closing under us — every adapter it launched and every terminal those
// adapters opened go with it. Without this they orphan to PID 1: the
// adapter keeps its stdio to a dead process and its node children keep
// running, which is the child-process leak class the audit already cost
// us once (docs/CORE_AUDIT.md).
func stopAllHosted() {
	stopUsagePatches()
	hostedMu.Lock()
	all := make([]*hosted, 0, len(hostedAll))
	for _, h := range hostedAll {
		all = append(all, h)
	}
	hostedMu.Unlock()
	for _, h := range all {
		h.closing.Store(true)
		if h.stop != nil {
			h.stop()
		}
		log.Printf("agentd: acp session stopped on shutdown key=%s agent=%s session=%s", h.key, h.agent, h.sessionID)
	}
	closeAllTerminals("agentd shutting down")
}

// setState upserts this session's roster row. Same four wire states the
// terminal tier publishes, so the sidebar cannot tell the tiers apart —
// which is the M3 acceptance criterion.
func (h *hosted) setState(state, reason string) {
	now := time.Now()
	var wantGit string
	var changed bool
	mutateStateIf(func(s *State) bool {
		r := rows[h.key]
		if r == nil {
			// A session being ended has had its row deleted by retire;
			// the turn it killed then reports "failed" through endTurn
			// and used to put the row straight back, where it lingered
			// until the sweep. Ended is ended.
			if h.closing.Load() {
				return false
			}
			r = &row{}
			rows[h.key] = r
		}
		before := r.Row
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
		r.Queued = int(h.queued.Load())
		r.Used, r.Size = h.used, h.size
		r.Title = h.shownTitle()
		r.Mode, r.Modes = h.mode, publicModes(h.modes)
		r.Yolo = h.yolo
		r.Configs = publicConfigs(h.configs)
		r.Commands = publicCommands(h.commands)
		// Copied, not aliased: a snapshot outlives this callback, and a
		// later append to h.extraRoots would otherwise rewrite a
		// published row from under its readers (the shallow-snapshot
		// footgun the race gate caught once already).
		r.Roots = append([]string(nil), h.extraRoots...)

		if h.cwd != "" && h.cwd != r.Cwd {
			r.Cwd = h.cwd
			r.Dir = dirLabel(h.cwd)
			r.Branch, r.Dirty = "", false
			wantGit = h.cwd
		}
		remembered := rememberSession(h.agent, h.sessionID, h.cwd, h.title, now)
		if remembered {
			historyDirty = true
		}
		// Publish only what moved. Narration re-asserts an unchanged row
		// several times a second during a turn; rebuilding the roster and
		// the whole session history for each of those, and then putting
		// it on the wire, is the Interactive flood this guards.
		moved := !sameRow(before, r.Row)
		if moved {
			s.Rows = publish(now)
		}
		if remembered {
			s.Recent = publishHistory()
		}
		changed = moved
		return moved || remembered
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
	mutateStateIf(func(s *State) bool {
		r := rows[h.key]
		if r == nil {
			return false
		}
		before := r.Row
		r.Detached = h.detached
		r.Queued = int(h.queued.Load())
		r.Used, r.Size = h.used, h.size
		r.Title = h.shownTitle()
		r.Mode, r.Modes = h.mode, publicModes(h.modes)
		r.Yolo = h.yolo
		r.Configs = publicConfigs(h.configs)
		r.Commands = publicCommands(h.commands)
		// Copied, not aliased, for the reason setState gives above.
		// Republished HERE as well as there: allowing a folder changes no
		// state, so setState never runs for it, and a row that only
		// learned its roots on the next state change is a widening the
		// person cannot see they made.
		r.Roots = append([]string(nil), h.extraRoots...)
		r.lastSeen = now
		if sameRow(before, r.Row) {
			return false
		}
		s.Rows = publish(now)
		return true
	})
}

// shownTitle is the title every surface renders: the person's name for
// the session when they gave one, else the agent's own. Reads under
// hostedMu — setState and republish run inside mutateStateIf, which is a
// different lock, so the read here is the one that guards the fields.
func (h *hosted) shownTitle() string {
	hostedMu.Lock()
	defer hostedMu.Unlock()
	if h.userTitle != "" {
		return h.userTitle
	}
	return h.title
}

// sameRow reports whether two published rows say the same thing.
//
// SinceMS is excluded deliberately: it is derived from the clock at
// publish time, so two otherwise identical rows always differ by a few
// milliseconds. Comparing it would defeat every dedupe — which is also
// why the elapsed clock is refreshed by the 10s sweep rather than by
// whatever happens to touch a row next.
func sameRow(a, b Row) bool {
	a.SinceMS, b.SinceMS = 0, 0
	return reflect.DeepEqual(a, b)
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
	case acp.UpdateAgentMessageChunk, acp.UpdateAgentThoughtChunk, acp.UpdatePlan:
		// Anything the agent says or does means it is working — but only
		// while a turn is open. A response can overtake the tail of its
		// own notification stream, so an unconditional write here left
		// finished sessions stuck on "working…" (see turnMu).
		h.narrated()
	case acp.UpdateToolCall, acp.UpdateToolCallUpdate:
		h.narrated()
		if h.toolMayChangeCheckout(n.Update) {
			refreshGitAfterTool(h.cwd)
		}
	case acp.UpdateUsage:
		if n.Update.Size > 0 || n.Update.Used > 0 {
			h.setUsage(n.Update.Used, n.Update.Size)
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

func (h *hosted) toolMayChangeCheckout(u acp.SessionUpdate) bool {
	kind := u.Kind
	hostedMu.Lock()
	if h.toolKinds == nil {
		h.toolKinds = map[string]string{}
	}
	if u.ToolCallID != "" {
		if kind != "" {
			h.toolKinds[u.ToolCallID] = kind
		} else {
			kind = h.toolKinds[u.ToolCallID]
		}
	}
	terminal := u.Status == acp.ToolStatusCompleted || u.Status == acp.ToolStatusFailed
	if terminal && u.ToolCallID != "" {
		delete(h.toolKinds, u.ToolCallID)
	}
	hostedMu.Unlock()

	if !terminal {
		return false
	}
	switch kind {
	case acp.ToolKindRead, acp.ToolKindSearch, acp.ToolKindFetch, acp.ToolKindThink:
		return false
	default:
		// A completion with no known opening event is conservatively treated
		// like execute/edit: failed tools can still leave partial changes.
		return true
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
		h.note("Auto-approved (yolo): " + preq.ToolName + " " + subject)
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
		h.narrateUnanswered(reasonAskOff, preq.ToolName, agentpolicy.ToolSubject(preq.ToolName, preq.ToolInput))
		return acp.Cancelled(), nil
	}

	subject := agentpolicy.ToolSubject(preq.ToolName, preq.ToolInput)
	v := h.askHuman(ctx, preq.ToolName, subject)
	switch v.decision {
	case DecisionAllow:
		return pick(req.Options, acp.OptionAllowOnce, acp.OptionAllowAlways), nil
	case DecisionDeny:
		return pick(req.Options, acp.OptionRejectOnce, acp.OptionRejectAlways), nil
	}
	if v.why == ReasonAgentExited {
		// The adapter is gone; there is nobody left to explain it to.
		return acp.Cancelled(), nil
	}
	h.narrateUnanswered(v.why, preq.ToolName, subject)
	return acp.Cancelled(), nil
}

// askHuman puts one question in the desktop queue and waits for it.
//
// The shared half of every path that needs a person: the tool-call
// approval above, and "this path is outside every folder you gave me"
// (roots.go). Returns the verdict rather than an ACP response, because
// the two callers answer their agents in different protocols.
func (h *hosted) askHuman(ctx context.Context, tool, subject string) verdict {
	h.setState("needs-input", "permission")
	// Back to working once answered — but through the turn gate, so an
	// answer that lands after the turn already ended cannot resurrect it.
	defer h.narrated()

	answer := make(chan verdict, 1)
	queued := enqueueAsk(askSpec{
		Agent:          h.agent,
		Tool:           tool,
		Subject:        subject,
		Cwd:            h.cwd,
		RowKey:         h.key,
		SourceApp:      AppID,
		SourceInstance: "",
	}, func(decision, why string) error {
		select {
		case answer <- verdict{decision: decision, why: why}:
		default:
		}
		return nil
	})
	if !queued {
		// enqueueAsk already answered with defer, and the buffered channel
		// is holding *which* defer. Read it so the refusal can say which
		// one rather than becoming an anonymous cancel.
		v := verdict{decision: DecisionDefer, why: ReasonNoDesktop}
		select {
		case v = <-answer:
		default:
		}
		return v
	}

	select {
	case <-ctx.Done():
		// The adapter went away under the question (ctx is the ACP
		// conn's, cancelled when its read loop ends). The question must
		// go with it — nothing else will delete it for up to 30 minutes.
		log.Printf("agentd: acp decide key=%s tool=%s decision=cancelled reason=%q", h.key, tool, ReasonAgentExited)
		cancelAsksFor(h.key, ReasonAgentExited)
		return verdict{decision: DecisionDefer, why: ReasonAgentExited}
	case v := <-answer:
		return v
	case <-time.After(hostedAskTTL):
		// Backstop only: the queue owns expiry and should always have
		// answered by now. Reaching here means the queue lost the ask.
		return verdict{decision: DecisionDefer, why: ReasonTimeout}
	}
}

// askOutside is the question a path outside every root raises. Same
// queue, same row, same buttons — the person is being asked about a
// FOLDER rather than a command, and nothing else about it differs.
//
// A yes allows that path for that call. It does not widen the session:
// widening is addRoot, a deliberate act with a visible result, and an
// approval buried in a stream of tool calls must not perform one.
func (h *hosted) askOutside(ctx context.Context, tool, path string) bool {
	pol := hostedPolicy()
	if pol.Enabled && !pol.AskDesktopOrDefault() {
		h.narrateUnanswered(reasonAskOff, tool, path)
		return false
	}
	hostedMu.Lock()
	yolo := h.yolo
	hostedMu.Unlock()
	if yolo {
		h.note("Auto-approved (yolo): " + tool + " outside this session's folders — " + path)
		return true
	}
	v := h.askHuman(ctx, tool, path+" (outside this session's folders)")
	if v.decision == DecisionAllow {
		h.note("Allowed once, outside this session's folders: " + path)
		return true
	}
	if v.decision != DecisionDeny && v.why != ReasonAgentExited {
		h.narrateUnanswered(v.why, tool, path)
	}
	return false
}

// verdict is an answer plus why it is that answer. The `why` is the whole
// difference between "the user said no" and "a timer said no", which the
// wire cannot carry: ACP v1's outcome discriminator is `selected` or
// `cancelled` and nothing else (internal/acp/types.go), so an unanswered
// question is *indistinguishable to the agent* from the user hitting
// cancel. Since the protocol cannot tell them apart, the transcript must.
type verdict struct{ decision, why string }

// unansweredReasons turns an internal defer reason into something a human
// reading the transcript can act on. Anything unmapped falls through
// verbatim rather than being swallowed — an unexplained refusal is the bug
// this function exists to prevent, so a clumsy explanation beats none.
func unansweredReason(why string) string {
	switch why {
	case ReasonTimeout:
		return "nobody answered in time"
	case ReasonNoDesktop:
		return "no desktop was attached to ask"
	case ReasonTooMany:
		return "too many questions already waiting on this agent"
	case reasonAskOff:
		return "asking is switched off in agents.json"
	case ReasonSessionEnded:
		return "the session was ended"
	case ReasonTurnCancelled:
		return "the turn was stopped"
	case ReasonAgentExited:
		return "the agent exited"
	}
	if why == "" {
		return "no answer"
	}
	return why
}

// note is wash speaking in its own transcript — an auto-approval, a
// refusal nobody chose, a capability it does not have.
//
// It uses appendEvent rather than appendPrompt on purpose. These callers
// all used to borrow appendPrompt, which stores what the HUMAN typed: the
// stored event kept Kind=user while only the pushed copy carried
// EventMessage, so a live window and a reloaded one showed the same note
// differently. appendEvent's own doc comment names that trap; these were
// the callers still in it.
func (h *hosted) note(text string) {
	e := appendEvent(h.key, Event{Kind: EventMessage, Text: text}, time.Now())
	if h.conn != nil {
		pushEvent(h.conn, h.key, e)
	}
}

// narrateUnanswered puts a refusal nobody chose into the transcript.
//
// It mirrors the yolo line above deliberately. That path narrates itself
// on the principle that "an agent that is being auto-approved must not
// look like one that is being watched"; the inverse was never written, so
// an auto-refused tool call simply failed with nothing on screen to say
// why. The loud path was narrated and the silent one was not.
func (h *hosted) narrateUnanswered(why, tool, subject string) {
	log.Printf("agentd: acp decide key=%s tool=%s decision=cancelled reason=%q subject=%q",
		h.key, tool, why, subject)
	text := "Not approved — " + unansweredReason(why) + ": " + tool
	if subject != "" {
		text += " " + subject
	}
	h.note(text)
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
	// agent_default prompt: read the stored default prompt. Answered to the
	// asker rather than pushed on the roster's state, for the same reason
	// agent_history is: it is one window's question, and a page of text
	// on every roster push would reach every subscriber — including the
	// desktop rail — several times a second during a turn.
	sdk.HandleFromVoid(bus, "agent_default_prompt", func(conn *sdk.Conn, _ string, _ struct{}, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		return conn.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{
			"kind": "default_prompt",
			"text": loadDefaultPrompt(),
		})
	})

	// agent_set_default prompt: store it. Empty removes the file.
	sdk.HandleFromVoid(bus, "agent_set_default_prompt", func(conn *sdk.Conn, _ string, req defaultPromptReq, from wire.Sender) error {
		if err := saveDefaultPrompt(req.Text); err != nil {
			log.Printf("agentd: save default prompt: %v", err)
			conn.Fail("Could not save the default prompt", err)
			return nil
		}
		stored := loadDefaultPrompt()
		log.Printf("agentd: default prompt saved bytes=%d", len(stored))
		// Republish so every window's launcher agrees about whether one
		// is set — including the window that did not make the change.
		mutateState(func(s *State) { s.HasDefaultPrompt = stored != "" })
		if from.InstanceID == "" {
			return nil
		}
		return conn.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{
			"kind": "default_prompt",
			"text": stored,
		})
	})

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
		// The stored default prompt goes first, ahead of whatever the
		// launcher was given (default prompt.go). Applied HERE rather than in
		// the FE so it holds however a session was started — the
		// launcher, `wash ai --agent`, or anything else that lands on
		// agent_start — and so the one place that reads the file is the
		// one process that owns sessions.
		first := withDefaultPrompt(loadDefaultPrompt(), req.Prompt)
		if first != "" {
			h.submitPrompt(turn{text: first})
		}
		if req.Open {
			openHosted(conn, h.key)
		} else if from.AppID == aiAppID {
			claimController(h.key, from.InstanceID)
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
	sdk.HandleFromVoid(bus, "agent_prompt", func(conn *sdk.Conn, _ string, req promptReq, _ wire.Sender) error {
		h := lookupHosted(req.Key)
		if h == nil {
			// A window still pointed at a session whose adapter exited (or
			// that was ended elsewhere). Say so where the person is,
			// rather than in a log they never see.
			log.Printf("agentd: acp prompt for unknown session key=%s", req.Key)
			conn.Warn("That session has ended", "Its agent is no longer running. Reopen it from History to continue.")
			return nil
		}
		// Queued inside a turn, run otherwise — never a second concurrent
		// session/prompt, which the protocol does not allow and which
		// flipped beginTurn/endTurn out of order.
		h.submitPrompt(turn{text: req.Text, blocks: h.attachmentBlocks(req.Blocks)})
		return nil
	})

	// agent_detach: the window closed but the session is to keep running.
	// The roster row stays and gains a Reattach affordance.
	sdk.HandleFromVoid(bus, "agent_detach", func(conn *sdk.Conn, _ string, req promptReq, _ wire.Sender) error {
		h := lookupHosted(req.Key)
		if h == nil {
			return nil
		}
		hostedMu.Lock()
		h.detached = true
		hostedMu.Unlock()
		log.Printf("agentd: acp detached key=%s agent=%s session=%s", h.key, h.agent, h.sessionID)
		h.republish()
		// A detach requested from the desktop rail must also close the window.
		// Only the controller owns this window. Transcript watchers may be
		// editor tabs and must not be closed with it.
		if instanceID := controllerFor(h.key); instanceID != "" {
			_ = conn.SendAppMsgTo(wire.Recipient{InstanceID: instanceID}, map[string]any{
				"kind": "detach",
				"key":  h.key,
			})
		}
		return nil
	})

	// agent_reattach: open a window onto a session that is still running.
	sdk.HandleFromVoid(bus, "agent_reattach", func(conn *sdk.Conn, _ string, req promptReq, _ wire.Sender) error {
		h := lookupHosted(req.Key)
		if h == nil {
			return nil
		}
		openHosted(conn, h.key)
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
		msg := "Auto-approval (yolo) is OFF — wash will ask before tools run."
		if req.On {
			msg = "Auto-approval (yolo) is ON — wash will approve tool requests without asking."
		}
		h.note(msg)
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
		// A question the turn was blocked on goes with the turn: the
		// agent hears cancelled on it and then ends the turn, and the
		// rail stops asking about a turn that is over.
		cancelAsksFor(h.key, ReasonTurnCancelled)
		return h.client.Cancel(h.sessionID)
	})

	// agent_add_root / agent_remove_root: widen or narrow which folders a
	// session may reach (roots.go). Deliberate and visible — the row
	// publishes the set, so every surface showing the session can say how
	// wide it is — rather than something a stream of tool approvals can
	// quietly accumulate.
	sdk.HandleFromVoid(bus, "agent_add_root", func(_ *sdk.Conn, _ string, req rootReq, _ wire.Sender) error {
		h := lookupHosted(req.Key)
		if h == nil || req.Path == "" {
			return nil
		}
		if h.addRoot(req.Path) {
			h.note("Also allowed: " + req.Path)
			h.republish()
		}
		return nil
	})

	sdk.HandleFromVoid(bus, "agent_remove_root", func(_ *sdk.Conn, _ string, req rootReq, _ wire.Sender) error {
		h := lookupHosted(req.Key)
		if h == nil || req.Path == "" {
			return nil
		}
		if h.removeRoot(req.Path) {
			h.note("No longer allowed: " + req.Path)
			h.republish()
		}
		return nil
	})

	// agent_stop: end a session and its adapter.
	sdk.HandleFromVoid(bus, "agent_stop", func(_ *sdk.Conn, _ string, req promptReq, _ wire.Sender) error {
		if h := lookupHosted(req.Key); h != nil {
			h.retire()
		}
		return nil
	})
}

// defaultPromptReq carries the default prompt on its way to disk. Empty text
// is a deletion, not a validation failure.
type defaultPromptReq struct {
	Text string `json:"text"`
}

type startReq struct {
	Agent  string `json:"agent"`
	Cwd    string `json:"cwd"`
	Prompt string `json:"prompt,omitempty"`
	Open   bool   `json:"open,omitempty"`
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
	h.note("The agent asked: " + req.Message + "\n(wash cannot answer structured questions yet, so it declined.)")
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

// rootReq addresses one folder on one session.
type rootReq struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

type promptReq struct {
	Key  string `json:"key"`
	Text string `json:"text,omitempty"`
	// Blocks are attachments sent with the text: a pasted image, a file
	// the composer's Attach button picked. Kept as a wash-shaped struct
	// rather than acp.ContentBlock so the app→service wire is ours to
	// validate — the router carries this from a window, and a window is
	// not trusted to name a mime type or a path.
	Blocks []promptAttachment `json:"blocks,omitempty"`
}

// promptAttachment is one attachment on its way to an ACP content block.
// Type is "image" or "file"; anything else is dropped.
type promptAttachment struct {
	Type string `json:"type"`
	// Image: base64 bytes and their mime type.
	Mime string `json:"mime,omitempty"`
	Data string `json:"data,omitempty"`
	// File: an absolute path, confined against the session cwd before it
	// becomes a resource_link.
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

// modelName is the agent's current model, read out of its generic
// settings block. Adapters name the option differently (claude-agent-acp
// calls it "model"; others prefix or title-case it), so the id is matched
// first and the display name second rather than assuming one spelling.
// Empty when the adapter exposes no model setting at all, which is a fact
// about that agent and not an error.
func (h *hosted) modelName() string {
	hostedMu.Lock()
	defer hostedMu.Unlock()
	for _, c := range h.configs {
		if strings.EqualFold(c.ID, "model") {
			return configLabel(c)
		}
	}
	for _, c := range h.configs {
		if strings.Contains(strings.ToLower(c.ID), "model") ||
			strings.Contains(strings.ToLower(c.Name), "model") {
			return configLabel(c)
		}
	}
	return ""
}

// configLabel prefers the human name of the selected value over its id:
// history should say "Claude Opus 4.5", not "claude-opus-4-5-20260101".
func configLabel(c acp.ConfigOption) string {
	for _, o := range c.Options {
		if o.Value == c.CurrentValue && o.Name != "" {
			return o.Name
		}
	}
	return c.CurrentValue
}

// noteSession appends what is known about a session right now: the model
// and title at the start, the ending at the end. The index reads the last
// summary, so a session killed with the router still carries its model.
func (h *hosted) noteSession(endReason string, now time.Time) {
	hostedMu.Lock()
	agent, sid, cwd, title, userTitle := h.agent, h.sessionID, h.cwd, h.title, h.userTitle
	hostedMu.Unlock()
	if sid == "" {
		return
	}
	s := transcriptSummary{
		Agent: agent, Model: h.modelName(), Cwd: cwd, Dir: dirLabel(cwd),
		Title: title, AtMS: now.UnixMilli(),
		// Restated on every summary so the final record — the one the
		// index reads first — carries the name; a clear is its own record
		// (renameSession) and must not be undone by a later blank.
		UserTitle: userTitle, UserTitleSet: userTitle != "",
	}
	if endReason != "" {
		s.EndReason = endReason
		s.EndedMS = now.UnixMilli()
		s.Events = transcriptLen(h.key)
	}
	writeSummary(sid, s)
}
