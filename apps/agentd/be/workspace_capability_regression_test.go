package agentd

import (
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/internal/swarm"
)

// A launch setting its provider cannot enforce is advisory: the member is
// not refused (it used to be, before commit, and before that it was committed
// and then failed at launch), the configure receipt names what will not hold,
// and the launch records what did. Only enforcement "adapter" is a gate.
func TestUnenforceableLaunchSettingsAreAdvisedNotRefused(t *testing.T) {
	adapterMemMu.Lock()
	adapterMem = map[string]agentproto.AdapterOptions{"claude": {Adapter: "claude", Version: "0.85.0"}}
	adapterMemMu.Unlock()
	t.Cleanup(resetAdapterMemoryForTest)
	for _, c := range []struct {
		settings swarm.AgentProfile
		want     []string
	}{
		{swarm.AgentProfile{Provider: "codex", Subagents: "deny"}, []string{`subagents "deny" on codex`, "instructed", "claude"}},
		{swarm.AgentProfile{Provider: "gemini", Subagents: "deny"}, []string{`subagents "deny" on gemini`}},
		{swarm.AgentProfile{Provider: "codex", Capability: "reviewer"}, []string{`capability "reviewer" on codex`, `enforcement "host"`}},
		{swarm.AgentProfile{Provider: "gemini", Capability: "reviewer"}, []string{`enforcement "host"`}},
		{swarm.AgentProfile{Provider: "claude", Capability: "reviewer"}, []string{"0.85.0", `enforcement "unverified"`}},
		{swarm.AgentProfile{Provider: "opencode", Capability: "reviewer"}, []string{"no opencode session has run"}},
	} {
		if err := reviewerHostCheck(c.settings); err != nil {
			t.Fatalf("%+v refused: %v", c.settings, err)
		}
		notes := launchAdvisories(c.settings)
		if len(notes) != 1 {
			t.Fatalf("%+v: advisories = %v", c.settings, notes)
		}
		for _, want := range c.want {
			if !strings.Contains(notes[0], want) {
				t.Fatalf("%+v: advisory %q lacks %q", c.settings, notes[0], want)
			}
		}
	}
	// What the adapter enforces itself needs no advisory.
	for _, settings := range []swarm.AgentProfile{
		{Provider: "claude", Subagents: "deny"},
		{Provider: "codex"},
		{Provider: "claude", Capability: "reviewer", Subagents: "deny"}, // one advisory, the reviewer's version
	} {
		notes := launchAdvisories(settings)
		if (len(notes) == 0) != (settings.Capability == "") {
			t.Fatalf("%+v: advisories = %v", settings, notes)
		}
	}
	// The strict reviewer is refused before commit where this host knows
	// the answer, and told what would launch instead.
	for _, settings := range []swarm.AgentProfile{
		{Provider: "claude", Capability: "reviewer", Enforcement: "adapter"},
		{Provider: "codex", Capability: "reviewer", Enforcement: "adapter"},
	} {
		err := reviewerHostCheck(settings)
		if err == nil || !strings.Contains(err.Error(), `enforcement "adapter" cannot be met`) {
			t.Fatalf("%+v: %v", settings, err)
		}
	}
	// A host that has never run the provider decides at launch.
	if err := reviewerHostCheck(swarm.AgentProfile{Provider: "opencode", Capability: "reviewer", Enforcement: "adapter"}); err != nil {
		t.Fatal("strict reviewer refused before any session ran:", err)
	}
}
