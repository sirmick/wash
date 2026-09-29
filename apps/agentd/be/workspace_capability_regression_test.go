package agentd

import (
	"strings"
	"testing"
)

// A launch setting its provider cannot enforce is refused while the member is
// still only a proposal. It used to be committed and then fail at launch,
// leaving a failed member whose resume re-read the same settings and failed
// the same way, so only redefining the key could recover it.
func TestUnsupportedLaunchSettingIsCaughtBeforeCommit(t *testing.T) {
	for _, c := range []struct {
		provider, capability string
		noSubagents          bool
		want                 string
	}{
		{provider: "codex", noSubagents: true, want: `subagents "deny" unsupported by codex`},
		{provider: "gemini", noSubagents: true, want: `subagents "deny" unsupported by gemini`},
		{provider: "codex", capability: "reviewer", want: `capability "reviewer" unsupported by codex`},
		{provider: "gemini", capability: "reviewer", want: `capability "reviewer" unsupported by gemini`},
	} {
		err := unsupportedLaunchSetting(c.provider, c.capability, c.noSubagents, "")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("provider %s capability %q subagents %v: err = %v", c.provider, c.capability, c.noSubagents, err)
		}
	}
	// The error names where the setting does work, so the orchestrator can
	// correct the definition without a failed launch to learn from.
	err := unsupportedLaunchSetting("codex", "", true, "")
	if err == nil || !strings.Contains(err.Error(), "claude") {
		t.Fatalf("the error does not say which provider takes it: %v", err)
	}
	// What each provider can enforce stays allowed.
	for _, c := range []struct {
		provider, capability string
		noSubagents          bool
	}{
		{provider: "claude", noSubagents: true},
		{provider: "claude", capability: "reviewer"},
		{provider: "opencode", capability: "reviewer"},
		{provider: "codex"},
	} {
		if err := unsupportedLaunchSetting(c.provider, c.capability, c.noSubagents, ""); err != nil {
			t.Fatalf("provider %s capability %q subagents %v: %v", c.provider, c.capability, c.noSubagents, err)
		}
	}
}
