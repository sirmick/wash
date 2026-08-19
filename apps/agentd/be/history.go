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
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// historyCap bounds the remembered sessions. Twenty is more than a day's
// work and keeps the sidebar list glanceable.
const historyCap = 20

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
	// LastSeen is unix seconds — an absolute the FE renders as "2h ago",
	// and the only field a keepalive touches.
	LastSeen int64 `json:"last_seen"`
	// Live is set on the way out to the FE: a session whose agent is
	// running right now is in the roster above, so the Recent list greys
	// it rather than offering to resume what is already here.
	Live bool `json:"live,omitempty"`
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

// publishHistory renders the Recent list, marking the sessions that are
// running right now (they're in the roster above; offering to resume them
// would be offering to duplicate them).
func publishHistory() []Session {
	live := map[string]bool{}
	for _, r := range rows {
		if r.SessionID != "" {
			live[r.SessionID] = true
		}
	}
	out := make([]Session, 0, len(history))
	for _, s := range history {
		s.Live = live[s.SessionID]
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen > out[j].LastSeen })
	return out
}

// resumeArgv is the command a Resume/Fork click runs. Pure, so what gets
// executed is a table in the tests rather than a string built at a call
// site.
//
// The agent is exec'd from a login shell so it inherits the user's real
// PATH, and the shell is given the session's directory — resuming into
// the wrong tree would be worse than not resuming at all. Single quotes
// are escaped the POSIX way ('\”) because a path or session id is
// attacker-adjacent data (it came off a hook payload).
func resumeArgv(shell, agent, sessionID, cwd string, fork bool) []string {
	if shell == "" {
		shell = "/bin/sh"
	}
	if agent == "" {
		agent = "claude"
	}
	cmd := shQuote(agent) + " --resume " + shQuote(sessionID)
	if fork {
		cmd += " --fork-session"
	}
	if cwd != "" {
		cmd = "cd " + shQuote(cwd) + " && exec " + cmd
	} else {
		cmd = "exec " + cmd
	}
	return []string{shell, "-c", cmd}
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// resumeSession spawns a terminal and tells it to run the resume command.
// Two steps, because a normal spawn carries no argv: the router replies
// with the new instance id (OnSpawnResult), and the terminal accepts an
// exec'd tab only from this service (see wash-term's exec_tab handler).
func resumeSession(c *sdk.Conn, sessionID string, _ bool) {
	var s *Session
	for i := range history {
		if history[i].SessionID == sessionID {
			s = &history[i]
			break
		}
	}
	if s == nil {
		log.Printf("agentd: resume unknown session=%s", sessionID)
		return
	}
	agent, cwd, sid := s.Agent, s.Cwd, s.SessionID

	// Reopen on our own goroutine: session/load replays the whole
	// conversation before it answers, which can take a while on a long
	// history, and the service must keep dispatching meanwhile.
	go func() {
		hs, err := resumeHosted(agent, cwd, sid, c)
		if err != nil {
			log.Printf("agentd: resume session=%s: %v", sid, err)
			c.Warn("Could not reopen that session", err.Error())
			// A session the agent no longer knows is not coming back, and
			// leaving it in the list invites the same failed click
			// forever.
			forgetSession(sid)
			return
		}
		pendingAttachMu.Lock()
		pendingAttach = append(pendingAttach, hs.key)
		pendingAttachMu.Unlock()
		if err := c.SpawnRequest(aiAppID); err != nil {
			log.Printf("agentd: resume spawn session=%s: %v", sid, err)
			popAttach()
			restoreDetached(hs.key)
		}
	}()
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
		restoreDetached(key)
		return
	}
	if e := c.SendAppMsgTo(wire.Recipient{InstanceID: instanceID}, map[string]any{
		"kind": "attach",
		"key":  key,
	}); e != nil {
		log.Printf("agentd: resume attach instance=%s: %v", instanceID, e)
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
