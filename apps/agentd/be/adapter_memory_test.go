package agentd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentproto"
)

// What an adapter offered is remembered across a restart, with the
// reporting session's own current values stripped, and published sorted.
func TestAdapterMemoryPersists(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	resetAdapterMemoryForTest()
	t.Cleanup(resetAdapterMemoryForTest)
	oldMutate := mutateStateIf
	var st agentproto.State
	mutateStateIf = func(fn func(*agentproto.State) bool) { fn(&st) }
	t.Cleanup(func() { mutateStateIf = oldMutate })
	modes := []acp.SessionMode{{ID: "default", Name: "Default"}, {ID: "acceptEdits", Name: "Accept edits"}}
	configs := []acp.ConfigOption{{ID: "model", Name: "Model", Category: "model", CurrentValue: "sonnet", Options: []acp.ConfigOptionValue{{Value: "sonnet", Name: "Sonnet"}, {Value: "haiku", Name: "Haiku"}}}}
	rememberAdapter("codex", acp.Implementation{Name: "codex-acp", Version: "1.13.1"}, nil, nil)
	rememberAdapter("claude", acp.Implementation{Name: "@agentclientprotocol/claude-agent-acp", Version: "0.81.2"}, modes, configs)
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "wash", "agent-adapters.json")); err != nil {
		t.Fatal(err)
	}
	resetAdapterMemoryForTest()
	got := publishAdapterOptions()
	if len(got) != 2 || got[0].Adapter != "claude" || got[1].Adapter != "codex" {
		t.Fatalf("published: %+v", got)
	}
	c := got[0]
	if c.Version != "0.81.2" || len(c.Modes) != 2 || len(c.Configs) != 1 || c.Configs[0].Category != "model" || c.Configs[0].Current != "" || len(c.Configs[0].Values) != 2 {
		t.Fatalf("claude: %+v", c)
	}
	// The roster was told, so the Stacks tab sees the list without waiting
	// for a sweep.
	if len(st.AdapterOptions) != 2 {
		t.Fatalf("roster: %+v", st.AdapterOptions)
	}
}
