package term

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// req builds a decide request for the tests that still live in this tier;
// the matcher's own tests moved with it to internal/agentpolicy (M2).
func req(tool string, input map[string]any, cwd string) decideRequest {
	return decideRequest{SessionID: "s-1", ToolName: tool, ToolInput: input, Cwd: cwd}
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
