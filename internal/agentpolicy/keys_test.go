package agentpolicy

import (
	"os"
	"path/filepath"
	"testing"
)

// The key store is a credential: only the owner may read it, whatever the
// umask, and setting one key keeps the others.
func TestSetKeyWritesOwnerOnlyAndKeepsOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wash", "keys.json")
	if err := SetKey(path, "openrouter", "sk-or-v1-0123456789abcd"); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(path, "other", "x"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	keys := LoadKeys(path)
	if keys["openrouter"] != "sk-or-v1-0123456789abcd" || keys["other"] != "x" {
		t.Errorf("keys = %v", keys)
	}
	// Empty deletes, and leaves the rest.
	if err := SetKey(path, "other", ""); err != nil {
		t.Fatal(err)
	}
	if keys := LoadKeys(path); len(keys) != 1 || keys["openrouter"] == "" {
		t.Errorf("after delete: %v", keys)
	}
	if err := SetKey(path, "../evil", "x"); err == nil {
		t.Error("a key name with a path was accepted")
	}
}

func TestKeysPathSitsBesideAgentsJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	if got := KeysPath(); got != "/cfg/wash/keys.json" {
		t.Errorf("KeysPath = %q", got)
	}
}

// Only the last four characters are ever shown, and a key too short to spare
// them shows nothing.
func TestKeyHint(t *testing.T) {
	if got := KeyHint("sk-or-v1-0123456789abcd"); got != "abcd" {
		t.Errorf("hint = %q", got)
	}
	if got := KeyHint("short"); got != "" {
		t.Errorf("short key hint = %q", got)
	}
}

// agents.json's stacks and connections survive Save, which rewrites the whole
// file: an "always allow" click must not delete someone's stacks.
func TestSaveKeepsStacksAndConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	in := `{"stacks":{"mine":{"name":"Mine","tiers":{"frontier":{"provider":"claude"}}}},"connections":{"claude@work":{"adapter":"claude","env":{"A":"1"}}}}`
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "Read", DecisionAllow, ""); err != nil {
		t.Fatal(err)
	}
	p := Load(path)
	if _, ok := p.Stacks["mine"]; !ok {
		t.Errorf("stacks lost: %+v", p.Stacks)
	}
	if p.Connections["claude@work"].Env["A"] != "1" {
		t.Errorf("connections lost: %+v", p.Connections)
	}
}
