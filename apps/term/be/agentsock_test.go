package term

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// startSock brings up a real listener with a canned policy and returns
// its path plus the warnings it raised.
func startSock(t *testing.T, p agentPolicy) (string, func() []string) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	sock := newAgentSock()
	if sock == nil {
		t.Fatal("newAgentSock returned nil")
	}
	t.Cleanup(sock.close)

	// Point the shared policy cache at a file holding this test's policy.
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	policyStore = policyCache{path: path}

	// The warn callback fires on the socket's own goroutine, so the
	// recorder needs its own lock — the test reads it while a connection
	// may still be writing.
	var mu sync.Mutex
	var warns []string
	go sock.serve(sockDeps{
		chanID: func() uint32 { return 7 },
		warn: func(title, body string) {
			mu.Lock()
			defer mu.Unlock()
			warns = append(warns, title+"|"+body)
		},
	})
	return sock.path, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), warns...)
	}
}

// ask does one request/response over the socket, the way the helper does.
func ask(t *testing.T, sockPath string, req decideRequest) decideResponse {
	t.Helper()
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp decideResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return resp
}

func TestAgentSockAnswers(t *testing.T) {
	path, warns := startSock(t, agentPolicy{
		Enabled: true,
		Rules: []policyRule{
			{Match: "Read", Decision: DecisionAllow},
			{Match: "Bash(rm *)", Decision: DecisionDeny},
		},
	})

	if got := ask(t, path, req("Read", map[string]any{"file_path": "/tmp/x"}, "/tmp")); got.Decision != DecisionAllow {
		t.Errorf("Read → %+v, want allow", got)
	}
	if got := ask(t, path, req("Bash", map[string]any{"command": "rm -rf /"}, "/tmp")); got.Decision != DecisionDeny {
		t.Errorf("rm → %+v, want deny", got)
	}
	if got := ask(t, path, req("Write", map[string]any{"file_path": "/tmp/y"}, "/tmp")); got.Decision != DecisionAsk {
		t.Errorf("Write → %+v, want ask", got)
	}

	// A denial is the one case the user is told about — the agent would
	// otherwise just appear to have stopped.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(warns()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	got := warns()
	if len(got) != 1 || !strings.Contains(got[0], "Blocked Bash") {
		t.Errorf("warnings = %v, want one 'Blocked Bash'", got)
	}
}

// Garbage in must still get the fail-open answer out: a broken client
// leaves the human in charge rather than hanging the agent's turn.
func TestAgentSockBadRequestAsks(t *testing.T) {
	path, _ := startSock(t, agentPolicy{Enabled: true, Rules: []policyRule{{Match: "Read", Decision: DecisionAllow}}})
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("this is not json\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp decideResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	if resp.Decision != DecisionAsk {
		t.Errorf("garbage → %+v, want ask", resp)
	}
}

// The socket must not be readable by anyone else on the box, and it must
// disappear with its tab.
func TestAgentSockPermissionsAndCleanup(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	sock := newAgentSock()
	if sock == nil {
		t.Fatal("newAgentSock returned nil")
	}
	fi, err := os.Stat(sock.path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode = %v, want 0600", perm)
	}
	if dirfi, err := os.Stat(filepath.Dir(sock.path)); err != nil {
		t.Fatal(err)
	} else if perm := dirfi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("socket dir mode = %v, want no group/other bits", perm)
	}
	sock.close()
	if _, err := os.Stat(sock.path); !os.IsNotExist(err) {
		t.Errorf("socket file survived close: %v", err)
	}
	// A second tab gets its own socket, not a shared one.
	other := newAgentSock()
	if other == nil {
		t.Fatal("second newAgentSock returned nil")
	}
	defer other.close()
	if other.path == sock.path {
		t.Error("two tabs share one socket path")
	}
}

// The env transform is what actually delivers the socket to the agent.
func TestWithAgentSockEnv(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	sock := newAgentSock()
	if sock == nil {
		t.Fatal("newAgentSock returned nil")
	}
	defer sock.close()
	env := withAgentSock(sock)([]string{"PATH=/bin", "TERM=dumb"})
	if !hasEnv(env, "WASH_AGENT_SOCK="+sock.path) {
		t.Errorf("env missing WASH_AGENT_SOCK: %v", env)
	}
	// …and the standard wash terminal environment is still applied.
	if !hasEnv(env, "TERM=xterm-256color") {
		t.Errorf("env lost the wash terminal setup: %v", env)
	}
	// No listener (socket creation failed) = no variable, which is the
	// same state as "policy not installed".
	env = withAgentSock(nil)([]string{"PATH=/bin"})
	for _, kv := range env {
		if strings.HasPrefix(kv, "WASH_AGENT_SOCK=") {
			t.Errorf("nil socket still exported %q", kv)
		}
	}
}

func hasEnv(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}
