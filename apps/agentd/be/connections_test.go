package agentd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/swarm"
)

func withKeys(t *testing.T, keys map[string]string) {
	t.Helper()
	old := keyStore
	keyStore = func() map[string]string { return keys }
	t.Cleanup(func() { keyStore = old })
}

// A connection adds its own environment and then its key, under every name
// it gives it. Claude through OpenRouter needs the base URL, the key as the
// auth token, and ANTHROPIC_API_KEY set empty so a key in the router's own
// environment cannot win.
func TestConnectionEnvAddsEnvThenKey(t *testing.T) {
	keys := map[string]string{"openrouter": "sk-or-test"}
	got, err := connectionEnv(agentpolicy.Policy{}, keys, "claude", "claude@openrouter")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ANTHROPIC_API_KEY=", "ANTHROPIC_BASE_URL=https://openrouter.ai/api", "ANTHROPIC_AUTH_TOKEN=sk-or-test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
	if got, err := connectionEnv(agentpolicy.Policy{}, nil, "claude", ""); err != nil || got != nil {
		t.Errorf("direct = %v, %v; want nothing", got, err)
	}
}

func TestConnectionEnvRefusesWhatCannotWork(t *testing.T) {
	cases := map[string]struct{ agent, conn string }{
		"key not set":        {"opencode", "opencode@openrouter"},
		"another adapter":    {"codex", "opencode@openrouter"},
		"unknown connection": {"claude", "claude@nowhere"},
	}
	for name, c := range cases {
		if _, err := connectionEnv(agentpolicy.Policy{}, map[string]string{}, c.agent, c.conn); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// agents.json replaces a built-in connection by name and adds new ones.
func TestAgentsJSONConnectionsReplaceByName(t *testing.T) {
	pol := agentpolicy.Policy{Connections: map[string]agentpolicy.Connection{
		"opencode@openrouter": {Adapter: "opencode", Env: map[string]string{"X": "1"}},
		"claude@work":         {Adapter: "claude", Env: map[string]string{"Y": "2"}},
	}}
	got, err := connectionEnv(pol, nil, "opencode", "opencode@openrouter")
	if err != nil || !reflect.DeepEqual(got, []string{"X=1"}) {
		t.Errorf("replaced built-in = %v, %v", got, err)
	}
	if got, err := connectionEnv(pol, nil, "claude", "claude@work"); err != nil || !reflect.DeepEqual(got, []string{"Y=2"}) {
		t.Errorf("added = %v, %v", got, err)
	}
}

func TestConnectionStatusNamesTheMissingKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if ok, note := connectionStatus(agentpolicy.Policy{}, nil, "opencode", "opencode@openrouter"); ok || note != "no openrouter key set" {
		t.Errorf("status = %v %q", ok, note)
	}
	if ok, note := connectionStatus(agentpolicy.Policy{}, map[string]string{"openrouter": "k"}, "opencode", "opencode@openrouter"); !ok {
		t.Errorf("status with key = %v %q", ok, note)
	}
	if ok, _ := connectionStatus(agentpolicy.Policy{}, nil, "claude", ""); ok {
		t.Error("claude reported available with no claude-agent-acp or npx on PATH")
	}
}

// The launch itself: the adapter process gets the connection's environment,
// and a connection that cannot work starts no process at all.
func TestLaunchGivesTheAdapterItsConnectionEnvironment(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env")
	script := filepath.Join(dir, "fake-opencode")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nenv > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	withPolicy(t, agentpolicy.Policy{Agents: map[string]agentpolicy.AgentConfig{"opencode": {Command: script}}})

	withKeys(t, map[string]string{})
	if _, err := startHostedCapability("opencode", dir, nil, sessionLaunch{connection: "opencode@openrouter"}); err == nil || !strings.Contains(err.Error(), "openrouter key") {
		t.Fatalf("launch without the key: %v", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("an adapter was started for a connection with no key")
	}

	withKeys(t, map[string]string{"openrouter": "sk-or-test"})
	// The fake exits at once, so the handshake fails; the environment it saw
	// is what this test is about.
	if _, err := startHostedCapability("opencode", dir, nil, sessionLaunch{connection: "opencode@openrouter"}); err == nil {
		t.Fatal("a fake that exits was reported started")
	}
	env, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"OPENROUTER_API_KEY=sk-or-test", "OPENCODE_CONFIG_CONTENT=" + opencodePermissions} {
		if !strings.Contains(string(env), want+"\n") {
			t.Errorf("adapter env lacks %s", want)
		}
	}
}

// A member resumes through the connection it was launched with.
func TestMemberLaunchKeepsTheConnection(t *testing.T) {
	w := swarm.Workspace{Lead: "lead"}
	l := memberLaunch(w, swarm.Member{ID: "x", State: "available", LaunchSettings: &swarm.AgentProfile{Provider: "opencode", Connection: "opencode@openrouter"}})
	if l.connection != "opencode@openrouter" {
		t.Errorf("launch = %+v", l)
	}
}

// History carries the connection to a resume, from the in-memory list and,
// past its cap, from the transcript's head. A resume that does not know its
// stack keeps the one recorded.
func TestResumeTargetKeepsTheConnection(t *testing.T) {
	withStateDir(t)
	old := history
	t.Cleanup(func() { history = old })
	history = nil
	rec := launchRecord{Agent: "opencode", Connection: "opencode@openrouter", Catalog: "openrouter-budget", Model: "coding"}
	bindTranscript("acp:1", "s-conn", rec, "/w", time.Now())
	waitForTranscriptWrites()
	s, ok := resolveResumeTarget("s-conn")
	if !ok || s.Agent != "opencode" || s.Connection != rec.Connection || s.Catalog != "openrouter-budget" || s.Model != "coding" {
		t.Fatalf("from the transcript: %+v %v", s, ok)
	}
	rememberSession(rec, "s-conn", "/w", "", time.Now())
	rememberSession(launchRecord{Agent: "opencode", Connection: "opencode@openrouter"}, "s-conn", "/w", "", time.Now())
	if s, _ := resolveResumeTarget("s-conn"); s.Connection != rec.Connection || s.Catalog != "openrouter-budget" {
		t.Fatalf("from history: %+v", s)
	}
}
