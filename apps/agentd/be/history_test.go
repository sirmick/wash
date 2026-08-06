package agentd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resetHistory() {
	history = nil
	historyDirty = false
	historySaved = time.Time{}
}

// The list is "what could I put back", most recent first.
func TestRememberSessionOrdersMostRecentFirst(t *testing.T) {
	resetHistory()
	rememberSession("claude", "s-1", "/home/mick/wash", "", t0)
	rememberSession("claude", "s-2", "/home/mick/other", "", t0.Add(time.Minute))
	rememberSession("codex", "s-3", "/tmp", "", t0.Add(2*time.Minute))

	if got := []string{history[0].SessionID, history[1].SessionID, history[2].SessionID}; got[0] != "s-3" {
		t.Errorf("order = %v, want s-3 first", got)
	}
	// Touching an older session moves it back to the front.
	rememberSession("claude", "s-1", "/home/mick/wash", "", t0.Add(3*time.Minute))
	if history[0].SessionID != "s-1" {
		t.Errorf("touched session did not move to front: %+v", history)
	}
	if len(history) != 3 {
		t.Errorf("a re-touch duplicated the entry: %d rows", len(history))
	}
}

// A keepalive touches last-seen constantly; that alone must not mark the
// file dirty, or the service would write on every tick.
func TestRememberSessionReportsRealChangesOnly(t *testing.T) {
	resetHistory()
	if !rememberSession("claude", "s-1", "/w", "", t0) {
		t.Error("a new session is a change")
	}
	if rememberSession("claude", "s-1", "/w", "", t0.Add(15*time.Second)) {
		t.Error("a keepalive reported a change")
	}
	if !rememberSession("claude", "s-1", "/w/other", "", t0.Add(30*time.Second)) {
		t.Error("a moved directory is a change")
	}
	if rememberSession("", "", "/w", "", t0) {
		t.Error("a session with no id was recorded")
	}
}

func TestHistoryCapped(t *testing.T) {
	resetHistory()
	for i := 0; i < historyCap+10; i++ {
		rememberSession("claude", "s-"+itoa(uint64(i)), "/w", "", t0.Add(time.Duration(i)*time.Second))
	}
	if len(history) != historyCap {
		t.Errorf("history holds %d, cap is %d", len(history), historyCap)
	}
	// The newest survive; the oldest fall off.
	if history[0].SessionID != "s-"+itoa(uint64(historyCap+9)) {
		t.Errorf("newest missing: %+v", history[0])
	}
}

// A session that is running right now is in the roster above — offering
// to resume it would be offering to duplicate it.
func TestPublishHistoryMarksLiveSessions(t *testing.T) {
	reset()
	resetHistory()
	rememberSession("claude", "live-1", "/w", "", t0)
	rememberSession("claude", "dead-1", "/w", "", t0.Add(-time.Hour))
	put("i-1:1", Row{Key: "i-1:1", Agent: "claude", State: "working", SessionID: "live-1"}, t0, t0)

	got := publishHistory()
	byID := map[string]Session{}
	for _, s := range got {
		byID[s.SessionID] = s
	}
	if !byID["live-1"].Live {
		t.Error("a running session was not marked live")
	}
	if byID["dead-1"].Live {
		t.Error("an ended session was marked live")
	}
}

// What Resume actually runs. The quoting matters: a path or a session id
// arrived from a hook payload.
func TestResumeArgv(t *testing.T) {
	cases := []struct {
		name                          string
		shell, agent, session, cwd    string
		fork                          bool
		wantShell                     string
		wantContains, wantNotContains []string
	}{
		{
			name: "resume in a directory", shell: "/bin/bash", agent: "claude",
			session: "abc-123", cwd: "/home/mick/wash", wantShell: "/bin/bash",
			wantContains:    []string{"cd '/home/mick/wash'", "exec 'claude' --resume 'abc-123'"},
			wantNotContains: []string{"--fork-session"},
		},
		{
			name: "fork", shell: "/bin/bash", agent: "claude", session: "abc", cwd: "/w", fork: true,
			wantShell: "/bin/bash", wantContains: []string{"--fork-session"},
		},
		{
			name: "no cwd known", shell: "/bin/zsh", agent: "codex", session: "z", wantShell: "/bin/zsh",
			wantContains: []string{"exec 'codex' --resume 'z'"}, wantNotContains: []string{"cd "},
		},
		{
			name: "no shell in the environment", agent: "claude", session: "s", wantShell: "/bin/sh",
			wantContains: []string{"--resume 's'"},
		},
		{
			name: "quotes in the data are escaped, not executed", shell: "/bin/bash", agent: "claude",
			session: "s'; rm -rf /; echo '", cwd: "/w",
			wantShell:       "/bin/bash",
			wantNotContains: []string{"; rm -rf /; echo ;"},
			wantContains:    []string{`'\''`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argv := resumeArgv(c.shell, c.agent, c.session, c.cwd, c.fork)
			if len(argv) != 3 || argv[0] != c.wantShell || argv[1] != "-c" {
				t.Fatalf("argv = %q", argv)
			}
			for _, want := range c.wantContains {
				if !strings.Contains(argv[2], want) {
					t.Errorf("command %q missing %q", argv[2], want)
				}
			}
			for _, no := range c.wantNotContains {
				if strings.Contains(argv[2], no) {
					t.Errorf("command %q contains %q", argv[2], no)
				}
			}
		})
	}
	// The agent defaults rather than producing a command with a hole in it.
	if argv := resumeArgv("/bin/sh", "", "s", "", false); !strings.Contains(argv[2], "'claude'") {
		t.Errorf("empty agent → %q", argv[2])
	}
}

// History has to survive the failure it exists for: the process going away.
func TestHistoryRoundTripsThroughDisk(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	resetHistory()
	rememberSession("claude", "s-1", "/home/mick/wash", "", t0)
	rememberSession("codex", "s-2", "/tmp", "", t0.Add(time.Minute))
	saveHistory()

	path := filepath.Join(dir, "wash", "agent-sessions.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("history not written: %v", err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}

	// A fresh process reads it back.
	resetHistory()
	loadHistory()
	if len(history) != 2 || history[0].SessionID != "s-2" {
		t.Fatalf("reloaded %+v", history)
	}
	if history[0].Dir != "tmp" {
		t.Errorf("dir label lost: %+v", history[0])
	}

	// Corrupt file: a cold start, never a crash.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	resetHistory()
	loadHistory()
	if len(history) != 0 {
		t.Errorf("corrupt history loaded as %+v", history)
	}
}

// The write is debounced so keepalives don't hammer the disk, but a change
// still lands within the flush window.
func TestFlushHistoryDebounces(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	resetHistory()
	rememberSession("claude", "s-1", "/w", "", t0)
	historyDirty = true
	historySaved = time.Now()

	flushHistory(time.Now())
	path := filepath.Join(dir, "wash", "agent-sessions.json")
	if _, err := os.Stat(path); err == nil {
		t.Error("flushed inside the debounce window")
	}
	flushHistory(time.Now().Add(historyFlush + time.Second))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("not flushed past the window: %v", err)
	}
	var out []Session
	if err := json.Unmarshal(data, &out); err != nil || len(out) != 1 {
		t.Errorf("written file = %s (%v)", data, err)
	}
	if historyDirty {
		t.Error("still dirty after a successful save")
	}
}
