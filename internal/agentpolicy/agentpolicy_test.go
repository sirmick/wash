package agentpolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	if err := Append(path, "Bash(git push*)", DecisionAllow); err != nil {
		t.Fatal(err)
	}
	p := Load(path)
	if !p.Enabled || len(p.Rules) != 1 || p.Rules[0].Match != "Bash(git push*)" || p.Rules[0].Decision != DecisionAllow {
		t.Fatalf("after first append: %+v", p)
	}

	// Idempotent: a second click on the same button doesn't grow the file.
	if err := Append(path, "Bash(git push*)", DecisionAllow); err != nil {
		t.Fatal(err)
	}
	if p := Load(path); len(p.Rules) != 1 {
		t.Errorf("duplicate append grew the table to %d rules", len(p.Rules))
	}

	// Appends land at the END, so a hand-written deny higher up keeps
	// beating a click made later.
	if err := Append(path, "Read", DecisionAllow); err != nil {
		t.Fatal(err)
	}
	p = Load(path)
	if len(p.Rules) != 2 || p.Rules[1].Match != "Read" {
		t.Fatalf("append order wrong: %+v", p.Rules)
	}

	// Same match, different decision, is a different rule.
	if err := Append(path, "Read", DecisionDeny); err != nil {
		t.Fatal(err)
	}
	if p := Load(path); len(p.Rules) != 3 {
		t.Errorf("deny of an allowed rule was swallowed: %+v", p.Rules)
	}

	// An empty rule is a no-op rather than a corrupt row.
	if err := Append(path, "", DecisionAllow); err != nil {
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
	if err := Append(path, "Read", DecisionAllow); err != nil {
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
