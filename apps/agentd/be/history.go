// Session history + resume (docs/AGENT_TERM.md §13, M7).
//
// A roster row disappears when its agent ends — right for "what is running
// now", useless for "put back what I lost". So every session the roster
// sees is also remembered: agent, session id, working directory, when it
// was last seen. The list is small, local, persisted, and holds no
// transcript content — just enough to reopen the door with
// `claude --resume <id>`.
//
// Persistence matters because the failure this exists for is the one that
// takes the process with it: a reboot, a crashed terminal, a closed
// window. History that only lived in memory would die with the thing it
// was supposed to survive.
package agentd

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// historyCap bounds the remembered sessions.
//
// Twenty was "more than a day's work", and on the machine this was
// reported from it was saturated at 20/20 spanning 344 hours — a
// fortnight on a quiet box and about two days on a busy one, with real
// work competing for slots against throwaways. The store is a few hundred
// bytes an entry; there is no reason for it to be the scarce thing.
const historyCap = 100

// recentPublishCap is how many of those ride the roster's state push.
//
// The store and the MENU want different sizes, and conflating them is why
// raising one used to mean bloating the other. Recent goes to every
// subscriber on every roster mutate, and a menu is for the last few
// things you touched — browsing the rest is the History panel's job,
// which searches transcripts and carries metadata a menu item cannot.
const recentPublishCap = 15

// historyFlush is the longest the on-disk copy lags memory. Keepalives
// touch last-seen constantly; only a real change (a new session, a moved
// directory) writes immediately.
const historyFlush = 30 * time.Second

// Session is one remembered agent session.
type Session struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	Cwd       string `json:"cwd,omitempty"`
	Dir       string `json:"dir,omitempty"`
	// Title is what this session was ABOUT, in the agent's own words —
	// it names its sessions on session_info_update once it works out what
	// the work is. "codex · mick" tells you nothing a week later; "Fix
	// the reconnect banner race" does.
	Title string `json:"title,omitempty"`
	// UserTitle is the name a PERSON gave the session (session_admin.go).
	// When set it is what publishHistory puts in Title; the agent's own
	// title stays here underneath so clearing the user's falls back to it.
	UserTitle string `json:"user_title,omitempty"`
	// LastSeen is unix seconds — an absolute the FE renders as "2h ago",
	// and the only field a keepalive touches.
	LastSeen int64 `json:"last_seen"`
	// Live is set on the way out to the FE: a session whose agent is
	// running right now is in the roster above, so the Recent list greys
	// it rather than offering to resume what is already here.
	Live bool `json:"live,omitempty"`
	// Detached is a live session with no window pointing at it.
	//
	// Live and REACHABLE are not the same thing, and treating them as one
	// is what made the History menu useless in exactly the case you open
	// it for. agent_detach sets the flag and closes the window but never
	// retires the row, so a detached session is still "live" — and the
	// menu, which hides live sessions to avoid offering to duplicate a
	// running one, hid the one thing you were trying to get back.
	//
	// A detached session is not something to resume. It is something to
	// reattach to, which is a different verb with a different outcome.
	Detached bool `json:"detached,omitempty"`
	// RowKey is the roster key this session is running as, present only
	// while it has a row. Reattach is key-addressed, not session-id
	// addressed, so the menu needs this to offer the verb at all.
	RowKey string `json:"row_key,omitempty"`
}

// rosterState is what the roster knows about one stored session id.
type rosterState struct {
	Live     bool
	Detached bool
	RowKey   string
}

// rosterIndex maps agent session ids to what the roster currently says
// about them.
//
// One function, two consumers: the History MENU (via publishHistory) and
// the History PANEL (via the agent_history handler). They read different
// stores — in-memory history vs the transcript index — and used to
// disagree about what was safe to click: the menu filtered live sessions
// and the panel did not, so one of them hid sessions you wanted and the
// other offered to duplicate ones you already had. The presentation may
// differ; the predicate may not.
func rosterIndex(rs []Row) map[string]rosterState {
	out := map[string]rosterState{}
	for _, r := range rs {
		if r.SessionID == "" {
			continue
		}
		// A row whose adapter exited lingers on the roster (failed/exited,
		// until the sweep drops it) so the failure is visible — but there
		// is no session behind it. It must read as resumable, not as
		// "running — go to it".
		if r.State == "failed" && r.Reason == "exited" {
			continue
		}
		out[r.SessionID] = rosterState{Live: true, Detached: r.Detached, RowKey: r.Key}
	}
	return out
}

var (
	history      []Session
	historyDirty bool
	historySaved time.Time
)

// rememberSession records (or refreshes) a session. Called from the roster
// path, so anything the roster can see is remembered — including sessions
// that end by having their terminal killed, which never say goodbye.
//
// Returns true when something worth persisting changed.
func rememberSession(agent, sessionID, cwd, title string, now time.Time) bool {
	if sessionID == "" {
		return false
	}
	for i := range history {
		if history[i].SessionID != sessionID {
			continue
		}
		if title != "" && history[i].Title != title {
			history[i].Title = title
			history[i].LastSeen = now.Unix()
			return true
		}
		changed := history[i].Cwd != cwd && cwd != ""
		if cwd != "" {
			history[i].Cwd = cwd
			history[i].Dir = dirLabel(cwd)
		}
		if agent != "" {
			changed = changed || history[i].Agent != agent
			history[i].Agent = agent
		}
		history[i].LastSeen = now.Unix()
		// Move-to-front so the list reads most-recent-first.
		s := history[i]
		copy(history[1:i+1], history[:i])
		history[0] = s
		return changed
	}
	history = append([]Session{{
		SessionID: sessionID,
		Agent:     agent,
		Cwd:       cwd,
		Dir:       dirLabel(cwd),
		Title:     title,
		LastSeen:  now.Unix(),
	}}, history...)
	if len(history) > historyCap {
		history = history[:historyCap]
	}
	return true
}

// publishHistory renders the Recent list, marking what the roster knows
// about each entry: running-and-attached (offering to resume it would be
// offering to duplicate it), running-but-detached (offer to reattach), or
// gone (offer to resume).
func publishHistory() []Session {
	live := make([]Row, 0, len(rows))
	for _, r := range rows {
		live = append(live, r.Row)
	}
	idx := rosterIndex(live)
	out := make([]Session, 0, len(history))
	for _, s := range history {
		st := idx[s.SessionID]
		s.Live, s.Detached, s.RowKey = st.Live, st.Detached, st.RowKey
		if s.UserTitle != "" {
			s.Title = s.UserTitle
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen > out[j].LastSeen })
	if len(out) > recentPublishCap {
		out = out[:recentPublishCap]
	}
	return out
}

var (
	resumeMu      sync.Mutex
	resumeFlights = map[string]bool{}
)

func beginResume(sessionID string) bool {
	resumeMu.Lock()
	defer resumeMu.Unlock()
	if resumeFlights[sessionID] {
		return false
	}
	resumeFlights[sessionID] = true
	return true
}

func finishResume(sessionID string) {
	resumeMu.Lock()
	delete(resumeFlights, sessionID)
	resumeMu.Unlock()
}

// resumeSession restores a stopped conversation through ACP and opens an
// Agent controller for it. If it is already live, the operation instead
// focuses (or reattaches) its existing controller.
func resumeSession(c *sdk.Conn, sessionID string, _ bool) {
	// History is eventually consistent with the live roster: a browser can
	// still show a Resume affordance for a moment after another click has
	// successfully loaded the session. Treat Resume as an idempotent "take me
	// to this conversation" action. ACP loadSession is not idempotent and a
	// second load of the same Codex session fails with an opaque Internal error.
	if h := hostedBySession(sessionID); h != nil {
		log.Printf("agentd: resume session=%s already live key=%s — focusing", sessionID, h.key)
		focusHosted(c, h.key)
		return
	}
	s, ok := resolveResumeTarget(sessionID)
	if !ok {
		log.Printf("agentd: resume unknown session=%s", sessionID)
		// Said where the click happened, not only in the log: a row that
		// does nothing when clicked reads as a dead app.
		c.Warn("Could not reopen that session", "wash has no record of it — not in its history and no transcript on disk.")
		return
	}
	agent, cwd, sid := s.Agent, s.Cwd, s.SessionID
	// The live-session check above closes the eventual-consistency window after
	// registration. This closes the earlier window: repeated clicks while the
	// adapter is still starting must share the first loadSession rather than
	// issuing another non-idempotent load for the same native session.
	if !beginResume(sid) {
		log.Printf("agentd: resume session=%s already in flight — coalescing", sid)
		return
	}

	// Reopen on our own goroutine: session/load replays the whole
	// conversation before it answers, which can take a while on a long
	// history, and the service must keep dispatching meanwhile.
	go func() {
		defer finishResume(sid)
		hs, err := resumeHosted(agent, cwd, sid, c)
		if err != nil {
			log.Printf("agentd: resume session=%s: %v", sid, err)
			c.Warn("Could not reopen that session", err.Error())
			// Keep the transcript in History. Native state can disappear
			// independently of wash's transcript, and the row still offers
			// the explicit restart-fresh and delete choices.
			return
		}
		openHosted(c, hs.key)
	}()
}

// resolveResumeTarget finds what to reopen for a session id: the
// in-memory history first, then the transcript store's own header.
//
// The History panel lists every transcript on disk, while `history` is
// capped at historyCap. A session older than the cap was therefore
// listed, clickable, and inert: resume looked it up in the slice, missed,
// and logged "resume unknown session". The file's meta line carries
// exactly what a resume needs (agent, cwd, id), so the store is the
// fallback — and a failed resume that forgets the slice entry no longer
// leaves a permanently dead row, because the next click resolves from
// the file again.
func resolveResumeTarget(sessionID string) (Session, bool) {
	for i := range history {
		if history[i].SessionID == sessionID {
			s := history[i]
			return s, s.Agent != ""
		}
	}
	m, ok := readSessionMeta(transcriptPath(sessionID))
	if !ok || m.SessionID != sessionID {
		return Session{}, false
	}
	return Session{
		SessionID: m.SessionID,
		Agent:     m.Agent,
		Cwd:       m.Cwd,
		Dir:       m.Dir,
		Title:     m.Title,
		UserTitle: m.UserTitle,
		LastSeen:  sessionRecency(m) / 1000,
	}, m.Agent != ""
}

// aiAppID is the window a reopened session appears in. Resume used to
// open a TERMINAL running `claude --resume` — which, once the intercept
// tier was deleted, produced an agent wash could no longer see at all
// (docs/AGENT_APP.md §10).
const aiAppID = "com.wash.ai"

var (
	pendingAttachMu sync.Mutex
	pendingAttach   []string
)

// popAttach takes the oldest queued attach. Spawn replies arrive in the
// order they were requested, and a click is a rare event.
func popAttach() (string, bool) {
	pendingAttachMu.Lock()
	defer pendingAttachMu.Unlock()
	if len(pendingAttach) == 0 {
		return "", false
	}
	k := pendingAttach[0]
	pendingAttach = pendingAttach[1:]
	return k, true
}

func removePendingAttach(key string) {
	pendingAttachMu.Lock()
	defer pendingAttachMu.Unlock()
	for i, pending := range pendingAttach {
		if pending == key {
			pendingAttach = append(pendingAttach[:i], pendingAttach[i+1:]...)
			return
		}
	}
}

// onSpawnResult fires when the router has started the window a resume
// asked for; it is then told which live session to attach to.
func onSpawnResult(c *sdk.Conn, appID, instanceID string, err error) {
	if appID != aiAppID {
		return
	}
	key, ok := popAttach()
	if !ok {
		return
	}
	if err != nil {
		log.Printf("agentd: resume spawn failed: %v", err)
		clearControllerLaunch(key)
		restoreDetached(key)
		return
	}
	if owner, ok := claimController(key, instanceID); !ok {
		clearControllerLaunch(key)
		if owner == "" {
			// The window died before it could be told its session: leave
			// the session detached, so the roster offers to open it again.
			log.Printf("agentd: controller instance=%s gone before attach key=%s", instanceID, key)
			restoreDetached(key)
		}
		return
	}
	if e := c.SendAppMsgTo(wire.Recipient{InstanceID: instanceID}, map[string]any{
		"kind": "attach",
		"key":  key,
	}); e != nil {
		log.Printf("agentd: resume attach instance=%s: %v", instanceID, e)
		releaseController(instanceID)
		restoreDetached(key)
	}
}

// forgetSession drops one entry from the remembered list.
// saveHistorySoon persists the remembered list. Called whenever it
// changes rather than only when a session ends: a session that never ends
// (detached, or the box rebooted) would otherwise never be written at
// all, which is exactly the case history exists for.
func saveHistorySoon() {
	if !historyDirty {
		return
	}
	saveHistory()
}

func forgetSession(sessionID string) {
	changed := false
	for i := range history {
		if history[i].SessionID == sessionID {
			history = append(history[:i], history[i+1:]...)
			changed = true
			break
		}
	}
	if !changed {
		return
	}
	mutateState(func(s *State) { s.Recent = publishHistory() })
	saveHistory()
}

// ---- persistence ----

// historyPath is $XDG_STATE_HOME/wash/agent-sessions.json (else
// ~/.local/state/wash/…): state, not config — nobody hand-edits it, and it
// should not ride a config backup.
func historyPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "wash", "agent-sessions.json")
}

// loadHistory reads the remembered sessions at startup. Any problem is a
// cold start, never an error: history is a convenience.
func loadHistory() {
	path := historyPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var out []Session
	if err := json.Unmarshal(data, &out); err != nil {
		log.Printf("agentd: history unreadable, starting empty: %v", err)
		return
	}
	if len(out) > historyCap {
		out = out[:historyCap]
	}
	history = out
}

// saveHistory writes the list atomically. Best-effort by design — losing
// history is a papercut, and a service that dies over one would be worse.
func saveHistory() {
	path := historyPath()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("agentd: history dir: %v", err)
		return
	}
	tmp, err := os.CreateTemp(dir, ".agent-sessions-*.json")
	if err != nil {
		log.Printf("agentd: history temp: %v", err)
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Chmod(name, 0o600)
	if err := os.Rename(name, path); err != nil {
		log.Printf("agentd: history save: %v", err)
		return
	}
	historyDirty = false
	historySaved = time.Now()
}

// flushHistory persists when something changed and either the change was
// structural or enough time has passed. Called from the sweep tick.
func flushHistory(now time.Time) {
	if !historyDirty {
		return
	}
	if now.Sub(historySaved) < historyFlush {
		return
	}
	saveHistory()
}
