package agentpolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// SuggestRule is what a human sees on a button and then lives with, so
// the table is the contract. The bias throughout: buy the least that
// still makes the click worth making.
func TestSuggestRule(t *testing.T) {
	cases := []struct {
		tool, subject, cwd string
		want               string
	}{
		// Bash: subcommand-aware, because Bash(git *) would also buy
		// `git reset --hard` from a click meant for `git push`.
		{"Bash", "git push origin main", "", "Bash(git push*)"},
		{"Bash", "git status --short", "", "Bash(git status*)"},
		{"Bash", "ls -la", "", "Bash(ls*)"},
		{"Bash", "make", "", "Bash(make*)"},
		{"Bash", "cat /etc/hosts", "", "Bash(cat*)"},   // path isn't a subcommand
		{"Bash", "echo 'hi there'", "", "Bash(echo*)"}, // quoted isn't either
		{"Bash", "npm run build", "", "Bash(npm run*)"},
		{"Bash", "docker compose up -d", "", "Bash(docker compose*)"},
		{"Bash", "", "", "Bash"},
		// Read-only tools: the tool IS the risk surface; a path would
		// only make the rule brittle.
		{"Read", "/etc/hosts", "/home/mick/wash", "Read"},
		{"Grep", "TODO", "/home/mick/wash", "Grep"},
		{"WebSearch", "wash desktop", "", "WebSearch"},
		// Writing tools get scoped to where the agent is working.
		{"Edit", "/home/mick/wash/apps/term/be/app.go", "/home/mick/wash", "Edit(/home/mick/wash/*)"},
		{"Write", "/tmp/x", "/home/mick/wash/", "Write(/home/mick/wash/*)"},
		{"Edit", "/tmp/x", "", "Edit"},
		// A URL rule names the host, not the query string.
		{"WebFetch", "https://example.com/some/path?q=1", "", "WebFetch(*example.com*)"},
		// Unparseable: the bare tool, not a nonsense host pattern.
		{"WebFetch", "not a url", "", "WebFetch"},
		// Unknown tools: the bare name, never a pattern we invented.
		{"SomeFutureTool", "whatever", "/home/mick", "SomeFutureTool"},
	}
	for _, c := range cases {
		if got := SuggestRule(c.tool, c.subject, c.cwd); got != c.want {
			t.Errorf("SuggestRule(%q, %q, %q) = %q, want %q", c.tool, c.subject, c.cwd, got, c.want)
		}
	}
}

func TestAskDesktopOrDefault(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		p    Policy
		want bool
	}{
		// Off means off: a disabled policy never asks, so a fresh box is
		// untouched by M6.
		{"disabled", Policy{}, false},
		{"disabled but asked for", Policy{AskDesktop: &yes}, false},
		// Enabling the policy opts into asking — that IS participating.
		{"enabled, unset", Policy{Enabled: true}, true},
		{"enabled, explicit yes", Policy{Enabled: true, AskDesktop: &yes}, true},
		{"enabled, explicit no", Policy{Enabled: true, AskDesktop: &no}, false},
	}
	for _, c := range cases {
		if got := c.p.AskDesktopOrDefault(); got != c.want {
			t.Errorf("%s: AskDesktopOrDefault = %v, want %v", c.name, got, c.want)
		}
	}
	var nilp *Policy
	if nilp.AskDesktopOrDefault() {
		t.Error("nil policy asked to ask")
	}
}

func TestAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")

	// Appending to nothing creates an enabled policy holding one rule —
	// clicking "always allow" is itself an act of turning the table on.
	if err := Append(path, "Bash(git push*)", DecisionAllow, ""); err != nil {
		t.Fatal(err)
	}
	p := Load(path)
	if !p.Enabled || len(p.Rules) != 1 || p.Rules[0].Match != "Bash(git push*)" || p.Rules[0].Decision != DecisionAllow {
		t.Fatalf("after first append: %+v", p)
	}

	// Idempotent: a second click on the same button doesn't grow the file.
	if err := Append(path, "Bash(git push*)", DecisionAllow, ""); err != nil {
		t.Fatal(err)
	}
	if p := Load(path); len(p.Rules) != 1 {
		t.Errorf("duplicate append grew the table to %d rules", len(p.Rules))
	}

	// Appends land at the END, so a hand-written deny higher up keeps
	// beating a click made later.
	if err := Append(path, "Read", DecisionAllow, ""); err != nil {
		t.Fatal(err)
	}
	p = Load(path)
	if len(p.Rules) != 2 || p.Rules[1].Match != "Read" {
		t.Fatalf("append order wrong: %+v", p.Rules)
	}

	// Same match, different decision, is a different rule.
	if err := Append(path, "Read", DecisionDeny, ""); err != nil {
		t.Fatal(err)
	}
	if p := Load(path); len(p.Rules) != 3 {
		t.Errorf("deny of an allowed rule was swallowed: %+v", p.Rules)
	}

	// An empty rule is a no-op rather than a corrupt row.
	if err := Append(path, "", DecisionAllow, ""); err != nil {
		t.Fatal(err)
	}
	if p := Load(path); len(p.Rules) != 3 {
		t.Errorf("empty rule was written: %+v", p.Rules)
	}
}

// Append must preserve everything else in the file — a click on "always
// allow" is not permission to drop the user's other settings.
func TestAppendPreservesTheRestOfTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	no := false
	orig := Policy{
		Enabled:    true,
		Default:    DecisionDeny,
		AskDesktop: &no,
		Rules:      []Rule{{Match: "Bash(rm *)", Decision: DecisionDeny, Cwd: "/srv"}},
	}
	if err := Save(path, orig); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "Read", DecisionAllow, ""); err != nil {
		t.Fatal(err)
	}
	p := Load(path)
	if p.Default != DecisionDeny || p.AskDesktop == nil || *p.AskDesktop {
		t.Errorf("append clobbered other settings: %+v", p)
	}
	if len(p.Rules) != 2 || p.Rules[0].Match != "Bash(rm *)" || p.Rules[0].Cwd != "/srv" {
		t.Errorf("append disturbed existing rules: %+v", p.Rules)
	}
}

func TestLoadDegradesToDisabled(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ name, body string }{
		{"malformed", "{oops"},
		{"not an object", "[1,2,3]"},
		{"empty", ""},
	} {
		path := filepath.Join(dir, c.name+".json")
		if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if p := Load(path); p.Enabled || len(p.Rules) != 0 {
			t.Errorf("%s file loaded as %+v, want the disabled zero value", c.name, p)
		}
	}
	if p := Load(filepath.Join(dir, "does-not-exist.json")); p.Enabled {
		t.Error("missing file loaded as enabled")
	}
}

// The file is a user-facing config, so its written shape is part of the
// contract with the settings pane.
func TestSaveShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	if err := Save(path, Policy{Enabled: true, Rules: []Rule{{Match: "Read", Decision: DecisionAllow}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unreadable: %v\n%s", err, data)
	}
	if _, ok := raw["enabled"]; !ok {
		t.Errorf("no enabled key: %s", data)
	}
	// Absent optional fields stay absent rather than writing false/null
	// noise into a file people hand-edit.
	for _, k := range []string{"ask_desktop", "default"} {
		if _, ok := raw[k]; ok {
			t.Errorf("unset field %q was written: %s", k, data)
		}
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600", perm)
	}
}

// "Allow always" for a shell command from project A must not also allow
// it in project B. SuggestRule scoped only Write/Edit (in the pattern);
// Append wrote Cwd:"" for everything else, so one click on
// Bash(make deploy*) in a scratch tree bought the same command in every
// checkout on the machine.
func TestBashRuleIsScopedToItsProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	projA, projB := "/home/u/scratch", "/home/u/production"

	rule := SuggestRule("Bash", "make deploy --env=staging", projA)
	if err := Append(path, rule, DecisionAllow, RuleScope("Bash", projA)); err != nil {
		t.Fatal(err)
	}
	p := Load(path)
	if len(p.Rules) != 1 || p.Rules[0].Cwd != projA {
		t.Fatalf("rule written unscoped: %+v", p.Rules)
	}

	req := func(cwd string) Request {
		return Request{ToolName: "Bash", ToolInput: map[string]any{"command": "make deploy --env=prod"}, Cwd: cwd}
	}
	if got := Evaluate(p, req(projA)); got.Decision != DecisionAllow {
		t.Errorf("in project A: %+v, want allow", got)
	}
	if got := Evaluate(p, req(projA+"/sub/dir")); got.Decision != DecisionAllow {
		t.Errorf("under project A: %+v, want allow", got)
	}
	if got := Evaluate(p, req(projB)); got.Decision != DecisionAsk {
		t.Errorf("in project B: %+v, want ask — the rule leaked across projects", got)
	}
	if got := Evaluate(p, req("")); got.Decision != DecisionAsk {
		t.Errorf("with no cwd: %+v, want ask", got)
	}

	// The same rule clicked in B is a second, B-scoped rule — not a
	// duplicate, and not a widening of A's.
	if err := Append(path, rule, DecisionAllow, RuleScope("Bash", projB)); err != nil {
		t.Fatal(err)
	}
	p = Load(path)
	if len(p.Rules) != 2 || p.Rules[1].Cwd != projB {
		t.Fatalf("second project's rule: %+v", p.Rules)
	}
	// And a repeat in A is still a no-op.
	if err := Append(path, rule, DecisionAllow, RuleScope("Bash", projA)); err != nil {
		t.Fatal(err)
	}
	if p := Load(path); len(p.Rules) != 2 {
		t.Errorf("duplicate scoped append grew the table to %d", len(p.Rules))
	}
}

func TestRuleScope(t *testing.T) {
	cases := []struct{ tool, cwd, want string }{
		{"Bash", "/home/u/proj", "/home/u/proj"},
		{"Bash", "/home/u/proj/", "/home/u/proj"},
		{"BashOutput", "/x", "/x"},
		{"Bash", "", ""},
		// Read-only tools: the same risk everywhere; a scope would only
		// make the rule brittle.
		{"Read", "/home/u/proj", ""},
		{"Grep", "/home/u/proj", ""},
		// Writers carry the directory in the pattern already.
		{"Edit", "/home/u/proj", ""},
		{"WebFetch", "/home/u/proj", ""},
	}
	for _, c := range cases {
		if got := RuleScope(c.tool, c.cwd); got != c.want {
			t.Errorf("RuleScope(%s, %s) = %q, want %q", c.tool, c.cwd, got, c.want)
		}
	}
}

// ---- adapter configuration -------------------------------------------

func TestMergeLeavesTheBuiltInLaunchAloneWhenNothingIsConfigured(t *testing.T) {
	var p Policy
	base := Launch{Command: "/usr/bin/gemini", Args: []string{"--experimental-acp"}}
	got := p.Merge("gemini", base)
	if got.Command != base.Command {
		t.Errorf("command = %q", got.Command)
	}
	if !reflect.DeepEqual(got.Args, base.Args) {
		t.Errorf("args = %v", got.Args)
	}
	if got.Env != nil || got.MCPServers != nil {
		t.Errorf("an empty policy added something: %+v", got)
	}
}

func TestMergeOverridesCommandAndAppendsArgs(t *testing.T) {
	p := Policy{Agents: map[string]AgentConfig{
		"claude": {Command: "/opt/wrap-claude", Args: []string{"--verbose", "--model", "opus"}},
	}}
	got := p.Merge("claude", Launch{Command: "/usr/bin/claude-agent-acp", Args: []string{"--acp"}})
	if got.Command != "/opt/wrap-claude" {
		t.Errorf("command = %q", got.Command)
	}
	// The built-in args come FIRST: they are what makes the process an
	// ACP adapter, and a user's flags are additions, not a replacement.
	if want := []string{"--acp", "--verbose", "--model", "opus"}; !reflect.DeepEqual(got.Args, want) {
		t.Errorf("args = %v, want %v", got.Args, want)
	}
	// The base must not have been mutated under the caller.
	base := Launch{Command: "x", Args: []string{"--acp"}}
	p.Merge("claude", base)
	if len(base.Args) != 1 {
		t.Errorf("Merge mutated the caller's args: %v", base.Args)
	}
}

func TestMergeEnvIsSortedAndSkipsEmptyKeys(t *testing.T) {
	p := Policy{Agents: map[string]AgentConfig{
		"codex": {Env: map[string]string{"ZED": "1", "ANTHROPIC_API_KEY": "sk-x", "": "ignored"}},
	}}
	got := p.Merge("codex", Launch{Command: "codex-acp"})
	if want := []string{"ANTHROPIC_API_KEY=sk-x", "ZED=1"}; !reflect.DeepEqual(got.Env, want) {
		t.Errorf("env = %v, want %v", got.Env, want)
	}
}

func TestMergeMCPServersGlobalThenAgentWithReplacementInPlace(t *testing.T) {
	p := Policy{
		MCPServers: []MCPServer{
			{Name: "fs", Command: "mcp-fs"},
			{Name: "web", Command: "mcp-web"},
			{Name: "broken"}, // no command: not startable, not offered
		},
		Agents: map[string]AgentConfig{
			"claude": {MCPServers: []MCPServer{
				{Name: "web", Command: "mcp-web-2", Args: []string{"--fast"}},
				{Name: "repo", Command: "mcp-repo"},
			}},
		},
	}
	got := p.Merge("claude", Launch{Command: "c"}).MCPServers
	names := make([]string, len(got))
	for i, s := range got {
		names[i] = s.Name
	}
	// web is replaced WHERE IT WAS, so the order a person wrote survives.
	if want := []string{"fs", "web", "repo"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	if got[1].Command != "mcp-web-2" || !reflect.DeepEqual(got[1].Args, []string{"--fast"}) {
		t.Errorf("web not replaced by the agent's own: %+v", got[1])
	}
	// Another agent sees only the global list.
	other := p.Merge("codex", Launch{Command: "c"}).MCPServers
	if len(other) != 2 {
		t.Errorf("codex got %d servers, want the 2 global ones", len(other))
	}
}

func TestAdapterConfigRoundTripsThroughTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	raw := `{
	  "enabled": true,
	  "rules": [{"match": "Read", "decision": "allow"}],
	  "mcp_servers": [{"name": "fs", "command": "mcp-fs", "args": ["--root", "/w"]}],
	  "agents": {
	    "claude": {
	      "command": "/opt/claude",
	      "args": ["--debug"],
	      "env": {"API_KEY": "x"},
	      "mcp_servers": [{"name": "repo", "command": "mcp-repo", "env": {"B": "2", "A": "1"}}]
	    }
	  }
	}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p := Load(path)
	// The approval half still decodes: this is one file, not two.
	if !p.Enabled || len(p.Rules) != 1 {
		t.Fatalf("policy half lost: %+v", p)
	}
	run := p.Merge("claude", Launch{Command: "claude-agent-acp"})
	if run.Command != "/opt/claude" || !reflect.DeepEqual(run.Args, []string{"--debug"}) {
		t.Errorf("launch = %+v", run)
	}
	if !reflect.DeepEqual(run.Env, []string{"API_KEY=x"}) {
		t.Errorf("env = %v", run.Env)
	}
	if len(run.MCPServers) != 2 {
		t.Fatalf("mcp = %+v", run.MCPServers)
	}
	if got := EnvPairs(run.MCPServers[1].Env); !reflect.DeepEqual(got, [][2]string{{"A", "1"}, {"B", "2"}}) {
		t.Errorf("EnvPairs = %v, want sorted pairs", got)
	}
}

// A file with no `agents` key at all — every box before this landed —
// configures nothing and cannot fail.
func TestAbsentAdapterConfigIsInert(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(path, []byte(`{"enabled": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := Load(path)
	base := Launch{Command: "codex-acp", Args: []string{"--acp"}}
	if got := p.Merge("codex", base); got.Command != base.Command || len(got.Env) != 0 || len(got.MCPServers) != 0 {
		t.Errorf("merge = %+v", got)
	}
}
