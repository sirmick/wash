package term

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func req(tool string, input map[string]any, cwd string) decideRequest {
	return decideRequest{SessionID: "s-1", ToolName: tool, ToolInput: input, Cwd: cwd}
}

// The property everything else rests on: with no policy, or the kill
// switch off, wash answers "ask" to everything — indistinguishable from
// wash not being there.
func TestPolicyOffAlwaysAsks(t *testing.T) {
	cases := []struct {
		name string
		p    agentPolicy
	}{
		// (A nil policy isn't in the table: the cache hands out values,
		// so "no policy" IS the zero value.)
		{"zero value", agentPolicy{}},
		{"disabled with allow rules", agentPolicy{
			Default: DecisionAllow,
			Rules:   []policyRule{{Match: "Bash", Decision: DecisionAllow}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := evaluate(c.p, req("Bash", map[string]any{"command": "rm -rf /"}, "/home/mick"))
			if got.Decision != DecisionAsk {
				t.Errorf("decision = %q, want ask (rule %q)", got.Decision, got.Rule)
			}
		})
	}
}

func TestPolicyFirstMatchWins(t *testing.T) {
	p := agentPolicy{
		Enabled: true,
		Rules: []policyRule{
			{Match: "Bash(rm *)", Decision: DecisionDeny},
			{Match: "Bash(git *)", Decision: DecisionAllow},
			{Match: "Bash", Decision: DecisionAsk},
			{Match: "Read", Decision: DecisionAllow},
		},
	}
	cases := []struct {
		cmd      string
		want     string
		wantRule string
	}{
		{"rm -rf build", DecisionDeny, "Bash(rm *)"},
		{"git status --short", DecisionAllow, "Bash(git *)"},
		{"make test", DecisionAsk, "Bash"},
	}
	for _, c := range cases {
		got := evaluate(p, req("Bash", map[string]any{"command": c.cmd}, "/w"))
		if got.Decision != c.want || got.Rule != c.wantRule {
			t.Errorf("%q → %+v, want %s via %s", c.cmd, got, c.want, c.wantRule)
		}
	}
	// A bare tool rule matches regardless of input.
	if got := evaluate(p, req("Read", map[string]any{"file_path": "/etc/shadow"}, "/w")); got.Decision != DecisionAllow {
		t.Errorf("Read → %+v, want allow", got)
	}
	// An unmatched tool falls through to the default (ask, unset here).
	if got := evaluate(p, req("WebFetch", map[string]any{"url": "https://x"}, "/w")); got.Decision != DecisionAsk || got.Rule != "default" {
		t.Errorf("WebFetch → %+v, want ask via default", got)
	}
}

// A typo in the file must never become an allow.
func TestPolicyUnknownDecisionsDegradeToAsk(t *testing.T) {
	p := agentPolicy{
		Enabled: true,
		Default: "yes-please",
		Rules: []policyRule{
			{Match: "Read", Decision: "ALLOW"},   // case-insensitive: real allow
			{Match: "Write", Decision: "permit"}, // not a decision → ask
			{Match: "Edit", Decision: ""},        // missing → ask
		},
	}
	if got := evaluate(p, req("Read", nil, "")); got.Decision != DecisionAllow {
		t.Errorf("uppercase ALLOW → %+v, want allow", got)
	}
	for _, tool := range []string{"Write", "Edit", "Bash"} {
		if got := evaluate(p, req(tool, nil, "")); got.Decision != DecisionAsk {
			t.Errorf("%s → %+v, want ask", tool, got)
		}
	}
}

func TestPolicyDefaultDeny(t *testing.T) {
	p := agentPolicy{
		Enabled: true,
		Default: DecisionDeny,
		Rules:   []policyRule{{Match: "Read", Decision: DecisionAllow}},
	}
	if got := evaluate(p, req("Read", nil, "")); got.Decision != DecisionAllow {
		t.Errorf("Read → %+v, want allow", got)
	}
	if got := evaluate(p, req("Bash", map[string]any{"command": "ls"}, "")); got.Decision != DecisionDeny {
		t.Errorf("Bash → %+v, want deny", got)
	}
}

// Rules can be scoped to a directory tree — "allow this inside my repo,
// ask everywhere else".
func TestPolicyCwdScope(t *testing.T) {
	p := agentPolicy{
		Enabled: true,
		Rules: []policyRule{
			{Match: "Bash(git *)", Decision: DecisionAllow, Cwd: "/home/mick/wash"},
		},
	}
	cases := []struct {
		cwd  string
		want string
	}{
		{"/home/mick/wash", DecisionAllow},
		{"/home/mick/wash/", DecisionAllow},
		{"/home/mick/wash/apps/term", DecisionAllow},
		{"/home/mick/washing-machine", DecisionAsk}, // prefix, not a parent
		{"/home/mick", DecisionAsk},
		{"", DecisionAsk}, // an unscoped request can't satisfy a scoped rule
	}
	for _, c := range cases {
		got := evaluate(p, req("Bash", map[string]any{"command": "git log"}, c.cwd))
		if got.Decision != c.want {
			t.Errorf("cwd %q → %+v, want %s", c.cwd, got, c.want)
		}
	}
}

// The subject a pattern matches against, per tool. An unknown tool has no
// subject, so a pattern rule can never accidentally match one.
func TestToolSubject(t *testing.T) {
	cases := []struct {
		tool  string
		input map[string]any
		want  string
	}{
		{"Bash", map[string]any{"command": "ls -la"}, "ls -la"},
		{"Read", map[string]any{"file_path": "/etc/hosts"}, "/etc/hosts"},
		{"Write", map[string]any{"file_path": "/tmp/x"}, "/tmp/x"},
		{"Edit", map[string]any{"file_path": "/tmp/x"}, "/tmp/x"},
		{"NotebookEdit", map[string]any{"notebook_path": "/n.ipynb"}, "/n.ipynb"},
		{"Glob", map[string]any{"pattern": "**/*.go"}, "**/*.go"},
		{"WebFetch", map[string]any{"url": "https://example.com"}, "https://example.com"},
		{"WebSearch", map[string]any{"query": "wash desktop"}, "wash desktop"},
		{"Task", map[string]any{"subagent_type": "Explore"}, "Explore"},
		{"Bash", map[string]any{"command": 42}, ""}, // wrong type → no subject
		{"SomeFutureTool", map[string]any{"x": "y"}, ""},
		{"Read", nil, ""},
	}
	for _, c := range cases {
		if got := toolSubject(c.tool, c.input); got != c.want {
			t.Errorf("toolSubject(%s, %v) = %q, want %q", c.tool, c.input, got, c.want)
		}
	}
	// …and the consequence: a pattern rule cannot match a tool wash
	// doesn't know the subject of.
	p := agentPolicy{Enabled: true, Rules: []policyRule{{Match: "SomeFutureTool(*)", Decision: DecisionAllow}}}
	if got := evaluate(p, req("SomeFutureTool", map[string]any{"cmd": "anything"}, "")); got.Decision != DecisionAllow {
		// "*" matches the empty subject — that IS a match, and the rule
		// author asked for it explicitly. Documented, not accidental.
		t.Errorf("SomeFutureTool(*) → %+v, want allow (\"*\" matches empty)", got)
	}
	p.Rules = []policyRule{{Match: "SomeFutureTool(danger*)", Decision: DecisionAllow}}
	if got := evaluate(p, req("SomeFutureTool", map[string]any{"cmd": "danger"}, "")); got.Decision != DecisionAsk {
		t.Errorf("a real pattern against an unknown tool → %+v, want ask", got)
	}
}

func TestSplitMatch(t *testing.T) {
	cases := []struct {
		in         string
		name, pat  string
		hasPattern bool
	}{
		{"Read", "Read", "", false},
		{" Read ", "Read", "", false},
		{"Bash(git status)", "Bash", "git status", true},
		{"Bash (git status)", "Bash", "git status", true},
		{"Bash()", "Bash", "", true},
		{"Bash(git status", "Bash", "git status", true}, // mid-typing
		{"Bash(echo (hi))", "Bash", "echo (hi)", true},
	}
	for _, c := range cases {
		name, pat, has := splitMatch(c.in)
		if name != c.name || pat != c.pat || has != c.hasPattern {
			t.Errorf("splitMatch(%q) = (%q, %q, %v), want (%q, %q, %v)", c.in, name, pat, has, c.name, c.pat, c.hasPattern)
		}
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"", "", true},
		{"", "x", false},
		{"*", "", true},
		{"*", "anything at all", true},
		{"git *", "git status", true},
		{"git *", "git", false},
		{"git*", "git", true},
		{"*status*", "git status --short", true},
		{"?at", "cat", true},
		{"?at", "at", false},
		{"/etc/*", "/etc/hosts", true},
		{"/etc/*", "/etc/ssh/sshd_config", true}, // * crosses slashes, unlike path.Match
		{"rm -rf *", "rm -rf /", true},
		{"rm *", "rmdir x", false}, // literal space anchors it
		{"exact", "exact", true},
		{"exact", "exactly", false},
		{"a*b*c", "azzbzzc", true},
		{"a*b*c", "azzbzz", false},
		// Claude Code's own rule spelling, copy-pasted from its settings.
		{"git status:*", "git status --short", true},
		{"git status:*", "git status", true},
		{"npm run test:*", "npm run test:unit", true},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

// A pattern that is all stars must not blow up on a long subject
// (classic glob backtracking bomb).
func TestGlobMatchPathological(t *testing.T) {
	pattern := "*a*a*a*a*a*a*a*a*a*b"
	s := ""
	for i := 0; i < 2000; i++ {
		s += "a"
	}
	done := make(chan bool, 1)
	go func() { done <- globMatch(pattern, s) }()
	select {
	case got := <-done:
		if got {
			t.Error("matched a pattern requiring a trailing b")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("globMatch did not finish in 2s — backtracking blowup")
	}
}

func TestEmptyToolAsks(t *testing.T) {
	p := agentPolicy{Enabled: true, Rules: []policyRule{{Match: "", Decision: DecisionAllow}}}
	if got := evaluate(p, req("", nil, "")); got.Decision != DecisionAsk {
		t.Errorf("empty tool → %+v, want ask", got)
	}
}

// ---- file loading ----

func TestPolicyCacheReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	c := &policyCache{path: path}

	// Missing file → disabled.
	if p := c.current(time.Now()); p.Enabled {
		t.Error("missing file yielded an enabled policy")
	}

	write := func(p agentPolicy) {
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(agentPolicy{Enabled: true, Rules: []policyRule{{Match: "Read", Decision: DecisionAllow}}})

	// A change is visible on the very NEXT decision — no window, because
	// clicking "always allow" (§12) is immediately followed by the tool
	// call that tests the rule.
	now := time.Now()
	p := c.current(now)
	if !p.Enabled || len(p.Rules) != 1 {
		t.Fatalf("policy not reloaded: %+v", p)
	}
	if got := evaluate(p, req("Read", nil, "")); got.Decision != DecisionAllow {
		t.Errorf("reloaded policy → %+v", got)
	}

	// Broken JSON degrades to disabled rather than half-applying.
	if err := os.WriteFile(path, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := c.current(now); p.Enabled {
		t.Errorf("malformed file yielded an enabled policy: %+v", p)
	}

	// …and the file going away disables it again.
	write(agentPolicy{Enabled: true})
	if p := c.current(now); !p.Enabled {
		t.Fatal("policy not re-enabled")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if p := c.current(now); p.Enabled {
		t.Error("removed file left the policy enabled")
	}
}

// The policy file the Agents pane writes must round-trip through the
// engine — the pane's JSON shape and the reader's structs are one
// contract, and this is the only place they meet.
func TestPolicyFileShape(t *testing.T) {
	const written = `{
	  "enabled": true,
	  "default": "ask",
	  "rules": [
	    {"match": "Bash(git status:*)", "decision": "allow", "cwd": "/home/mick/wash"},
	    {"match": "Read", "decision": "allow"},
	    {"match": "Bash(rm *)", "decision": "deny"}
	  ]
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(path, []byte(written), 0o600); err != nil {
		t.Fatal(err)
	}
	p := readPolicyFile(path)
	if !p.Enabled || len(p.Rules) != 3 {
		t.Fatalf("decoded %+v", p)
	}
	if got := evaluate(p, req("Bash", map[string]any{"command": "git status -s"}, "/home/mick/wash")); got.Decision != DecisionAllow {
		t.Errorf("scoped git rule → %+v", got)
	}
	if got := evaluate(p, req("Bash", map[string]any{"command": "git status -s"}, "/tmp")); got.Decision != DecisionAsk {
		t.Errorf("outside the scope → %+v, want ask", got)
	}
	if got := evaluate(p, req("Bash", map[string]any{"command": "rm -rf /"}, "/tmp")); got.Decision != DecisionDeny {
		t.Errorf("rm rule → %+v", got)
	}
}
