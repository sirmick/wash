package agentd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
)

// A configured command replaces the built-in name AND skips the npx
// fallback. Someone who named a binary meant that binary; quietly running
// a package from the registry instead is the opposite of what they asked.
func TestLaunchWithHonoursAConfiguredCommand(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "my-codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := Adapter{ID: "codex", Command: "codex-acp", Package: "@x/codex-acp", Args: []string{"--acp"}}

	cmd, args, note, ok := a.launchWith(agentpolicy.AgentConfig{Command: bin})
	if !ok || cmd != bin {
		t.Fatalf("cmd=%q ok=%v note=%q", cmd, ok, note)
	}
	// The adapter's OWN args survive: they are what makes it speak ACP.
	if !reflect.DeepEqual(args, []string{"--acp"}) {
		t.Errorf("args = %v", args)
	}

	// A command that is not there is a refusal naming the file, not a
	// silent fall back to npx.
	if _, _, note, ok = a.launchWith(agentpolicy.AgentConfig{Command: filepath.Join(dir, "nope")}); ok {
		t.Error("a missing configured command was accepted")
	} else if !strings.Contains(note, "agents.json") {
		t.Errorf("note = %q, want it to name where the command came from", note)
	}
}

func TestCodexFallbackUsesTheInstalledCodex(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"npx", "codex"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	a := adapters[0]
	cfg := agentpolicy.AgentConfig{}
	cmd, args, note, ok := a.launchWith(cfg)
	if !ok || cmd != filepath.Join(dir, "npx") || note != "via npx @agentclientprotocol/codex-acp" {
		t.Fatalf("launch: cmd=%q args=%v note=%q ok=%v", cmd, args, note, ok)
	}
	want := []string{"CODEX_PATH=" + filepath.Join(dir, "codex")}
	if got := a.builtinEnv(cfg); !reflect.DeepEqual(got, want) {
		t.Fatalf("builtin env = %v, want %v", got, want)
	}

	// A configured adapter may be a wrapper with different semantics. Do not
	// inject an implementation detail from the built-in codex-acp path.
	if got := a.builtinEnv(agentpolicy.AgentConfig{Command: filepath.Join(dir, "codex")}); got != nil {
		t.Fatalf("configured adapter inherited built-in env: %v", got)
	}
	t.Setenv("CODEX_PATH", "/chosen/codex")
	if got := a.builtinEnv(agentpolicy.AgentConfig{}); got != nil {
		t.Fatalf("inherited CODEX_PATH was overridden: %v", got)
	}
}

// wash's config shape (env as a map, because a person writes it) becomes
// ACP's (name/value objects), in a stable order.
func TestACPMCPServersConversion(t *testing.T) {
	got := acpMCPServers([]agentpolicy.MCPServer{
		{Name: "repo", Command: "mcp-repo", Args: []string{"--fast"}, Env: map[string]string{"B": "2", "A": "1"}},
	})
	want := []acp.McpServer{{
		Name: "repo", Command: "mcp-repo", Args: []string{"--fast"},
		Env: []acp.EnvVar{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// Nothing configured stays nil, which the client turns into the `[]`
	// the spec requires.
	if acpMCPServers(nil) != nil {
		t.Error("an empty list became something")
	}
}
