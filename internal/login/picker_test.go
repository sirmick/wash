package login

import (
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeSpawner records Spawn calls and returns a pre-canned Session
// + error per invocation. Used to test picker actions without
// touching real exec.
type fakeSpawner struct {
	mu       sync.Mutex
	calls    []spawnCall
	respond  func(id Identity, name string) (Session, error)
	maxCap   int
	sessions SessionRegistry
}

type spawnCall struct {
	UID  uint32
	Name string
}

// pickerTestSetup wires a Server that uses both a fake SessionRegistry
// and a Spawner that points at a stub. Use the returned trap to
// inspect kill targets and spawn invocations.
func pickerTestSetup(t *testing.T, sessions []Session, spawnResp func(Identity, string) (Session, error)) (*httptest.Server, *http.Client, *killTrap, *fakeSpawnTrap) {
	t.Helper()
	auth, _ := NewTestAuth("alice:hunter2", 1000, 1000, "/bin/sh")
	reg := &fakeRegistry{uidSessions: map[uint32][]Session{1000: sessions}}
	trap := &killTrap{}
	spawn := &fakeSpawnTrap{respond: spawnResp}
	srv, err := NewServer(Config{
		Auth:     auth,
		Signer:   signerForTest(t),
		Logger:   log.New(io.Discard, "", 0),
		Sessions: reg,
		Spawner: &Spawner{
			Sessions: reg,
			// MaxPerUID=0 means the cap-check skipped; tests that
			// want to exercise it call spawnFor with a configured
			// fake registry holding `n` sessions and set MaxPerUID
			// via the spawn trap.
			MaxPerUID: 0,
			// RouterBinary is empty: routing-decision tests never
			// reach the actual exec. Spawner.Spawn is a concrete
			// method (not an interface), so spawn-behaviour tests
			// construct a Spawner inline instead (TestSpawnCap*).
		},
		Killer: trap.kill,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	// Spawner is non-nil so /ws and /ws/s/ routing decisions reach
	// their handlers; the configured RouterBinary is empty, so any
	// code path that actually invokes .Spawn fails at exec — fine
	// for routing tests that never get that far. Tests that DO need
	// to exercise spawn directly use TestSpawnCap* with a Spawner
	// constructed inline.
	_ = spawn
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar := newSimpleJar(ts.URL)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.PostForm(ts.URL+"/auth", url.Values{
		"user":     []string{"alice"},
		"password": []string{"hunter2"},
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	resp.Body.Close()
	return ts, client, trap, spawn
}

// fakeSpawnTrap records what spawnFor wanted to do; the test asserts
// against its calls rather than touching real exec.
type fakeSpawnTrap struct {
	mu      sync.Mutex
	calls   []spawnCall
	respond func(id Identity, name string) (Session, error)
}

func TestPickerListsSessions(t *testing.T) {
	sessions := []Session{
		{Pid: 1111, UID: 1000, SessID: "s-alpha", Name: "mick", Sock: "/run/wash/1000/sessions/s-alpha.sock"},
		{Pid: 2222, UID: 1000, SessID: "s-beta", Name: "scratch", Sock: "/run/wash/1000/sessions/s-beta.sock"},
	}
	ts, client, _, _ := pickerTestSetup(t, sessions, nil)

	resp, err := client.Get(ts.URL + "/sessions")
	if err != nil {
		t.Fatalf("GET /sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	for _, want := range []string{`data-sessid="s-alpha"`, `data-sessid="s-beta"`, ">mick<", ">scratch<", `href="/?s=s-alpha"`, `href="/?s=s-beta"`} {
		if !strings.Contains(bs, want) {
			t.Errorf("picker missing %q in:\n%s", want, bs)
		}
	}
}

func TestPickerEmptyShowsHint(t *testing.T) {
	ts, client, _, _ := pickerTestSetup(t, nil, nil)
	resp, err := client.Get(ts.URL + "/sessions")
	if err != nil {
		t.Fatalf("GET /sessions: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "No running sessions") {
		t.Errorf("expected empty-state hint in:\n%s", body)
	}
}

func TestSessionsEndSIGTERMsTarget(t *testing.T) {
	sessions := []Session{
		{Pid: 1111, UID: 1000, SessID: "s-alpha"},
		{Pid: 2222, UID: 1000, SessID: "s-beta"},
	}
	ts, client, trap, _ := pickerTestSetup(t, sessions, nil)
	resp, err := client.PostForm(ts.URL+"/sessions/end", url.Values{"sessid": []string{"s-alpha"}})
	if err != nil {
		t.Fatalf("POST /sessions/end: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/sessions" {
		t.Errorf("status/redirect: got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	pids := trap.pids()
	if len(pids) != 1 || pids[0] != 1111 {
		t.Errorf("kill: got %v want [1111]", pids)
	}
}

func TestSessionsEndUnknownSessidNoOps(t *testing.T) {
	sessions := []Session{{Pid: 1111, UID: 1000, SessID: "s-alpha"}}
	ts, client, trap, _ := pickerTestSetup(t, sessions, nil)
	resp, err := client.PostForm(ts.URL+"/sessions/end", url.Values{"sessid": []string{"s-nonexistent"}})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if pids := trap.pids(); len(pids) != 0 {
		t.Errorf("unknown sessid signalled: %v", pids)
	}
}

func TestValidSessionName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"work", true},
		{"my session 2", true},
		{"a-b_c.d", true},
		{"", false},
		{"with/slash", false},
		{"with\\backslash", false},
		{"with\x00nul", false},
		{"with\nnewline", false},
		{strings.Repeat("x", 64), true},
		{strings.Repeat("x", 65), false},
	}
	for _, tc := range cases {
		if got := validSessionName(tc.name); got != tc.ok {
			t.Errorf("validSessionName(%q): got %v want %v", tc.name, got, tc.ok)
		}
	}
}

func TestSpawnCapEnforced(t *testing.T) {
	reg := &fakeRegistry{uidSessions: map[uint32][]Session{
		1000: {
			{Pid: 1111, UID: 1000, SessID: "s-a"},
			{Pid: 2222, UID: 1000, SessID: "s-b"},
		},
	}}
	sp := &Spawner{
		MaxPerUID: 2,
		Sessions:  reg,
	}
	_, err := sp.Spawn(Identity{UID: 1000, Name: "alice"}, "new")
	if !errors.Is(err, ErrSessionCap) {
		t.Errorf("expected ErrSessionCap, got %v", err)
	}
}

func TestSpawnCapDisabled(t *testing.T) {
	reg := &fakeRegistry{uidSessions: map[uint32][]Session{
		1000: {
			{Pid: 1111, UID: 1000, SessID: "s-a"},
			{Pid: 2222, UID: 1000, SessID: "s-b"},
		},
	}}
	sp := &Spawner{
		MaxPerUID: 0,
		Sessions:  reg,
	}
	// We just need the cap check NOT to fire; the spawn itself
	// will fail because RouterBinary is empty + we have no real
	// binary, and that failure happens AFTER the cap check.
	_, err := sp.Spawn(Identity{UID: 1000, Name: "alice"}, "new")
	if errors.Is(err, ErrSessionCap) {
		t.Errorf("cap should be disabled at MaxPerUID=0; got ErrSessionCap")
	}
}

func TestWSRedirectsToPickerOnMultiple(t *testing.T) {
	sessions := []Session{
		{Pid: 1111, UID: 1000, SessID: "s-a"},
		{Pid: 2222, UID: 1000, SessID: "s-b"},
	}
	ts, client, _, _ := pickerTestSetup(t, sessions, nil)
	// /ws can't actually hijack a httptest client because we have no
	// Upgrade headers — but the routing decision happens before
	// hijack, so we should get a 302 to /sessions even without
	// Upgrade.
	resp, err := client.Get(ts.URL + "/ws")
	if err != nil {
		t.Fatalf("GET /ws: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/sessions" {
		t.Errorf("expected 302→/sessions, got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestWSSpecificUnknownSessid404(t *testing.T) {
	sessions := []Session{
		{Pid: 1111, UID: 1000, SessID: "s-a", Sock: "/tmp/x.sock"},
	}
	ts, client, _, _ := pickerTestSetup(t, sessions, nil)
	resp, err := client.Get(ts.URL + "/ws/s/s-nonexistent/")
	if err != nil {
		t.Fatalf("GET /ws/s/...: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown sessid, got %d", resp.StatusCode)
	}
}
