package agentd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/swarm"
)

func stacksPolicy(t *testing.T, stacks map[string]string) agentpolicy.Policy {
	t.Helper()
	p := agentpolicy.Policy{Stacks: map[string]json.RawMessage{}}
	for id, raw := range stacks {
		p.Stacks[id] = json.RawMessage(raw)
	}
	return p
}

// The shipped stacks are complete and valid; a defect in stacks.json would
// otherwise only show as a greyed row on someone's desktop.
func TestBuiltinStacksAreValid(t *testing.T) {
	stacks, bad := loadStacks(agentpolicy.Policy{})
	if len(bad) > 0 {
		t.Fatalf("invalid built-in stacks: %v", bad)
	}
	for _, id := range []string{"anthropic", "openai", "openrouter"} {
		s, ok := stacks[id]
		if !ok || len(s.Tiers) != len(tierNames) {
			t.Errorf("stack %s = %+v", id, s)
		}
	}
	if s := stacks["anthropic"].Tiers["review"]; s.Capability != "reviewer" {
		t.Errorf("anthropic review tier is not an enforced reviewer: %+v", s)
	}
	for tier, p := range stacks["openrouter"].Tiers {
		if p.Provider != "opencode" || p.Connection != "opencode@openrouter" || !strings.HasPrefix(p.Model, "openrouter/") {
			t.Errorf("openrouter %s = %+v", tier, p)
		}
	}
	// Haiku offers no effort option (claude-agent-acp 0.81.2): setting one
	// would fail every small-tier launch.
	if p := stacks["anthropic"].Tiers["small"]; p.Thinking != "" {
		t.Errorf("anthropic small sets thinking %q", p.Thinking)
	}
}

// agents.json overrides a stack tier by tier, and may rename it; the tiers
// it does not mention stay.
func TestAgentsJSONOverridesAStackTierByTier(t *testing.T) {
	pol := stacksPolicy(t, map[string]string{"anthropic": `{"name":"Mine","tiers":{"frontier":{"provider":"claude","model":"opus[1m]","thinking":"high"}}}`})
	stacks, bad := loadStacks(pol)
	if len(bad) > 0 {
		t.Fatal(bad)
	}
	s := stacks["anthropic"]
	if s.Name != "Mine" || s.Tiers["frontier"].Model != "opus[1m]" || s.Tiers["coding"].Model != "sonnet" {
		t.Errorf("merged = %+v", s)
	}
	// The built-in table is not changed by a merge.
	if builtinStacks["anthropic"].Tiers["frontier"].Model == "opus[1m]" {
		t.Error("an override leaked into the built-in stacks")
	}
}

// A stack that cannot work is kept, with the reason, so the launcher can
// say why instead of the row disappearing.
func TestInvalidStacksAreKeptWithTheirReason(t *testing.T) {
	full := func(tier string) string {
		tiers := []string{}
		for _, n := range tierNames {
			v := `{"provider":"claude"}`
			if n == "coding" {
				v = tier
			}
			tiers = append(tiers, `"`+n+`":`+v)
		}
		return `{"name":"X","tiers":{` + strings.Join(tiers, ",") + `}}`
	}
	cases := map[string]string{
		"missing tiers":       `{"name":"X","tiers":{"frontier":{"provider":"claude"}}}`,
		"no name":             `{"tiers":{"frontier":{"provider":"claude"},"coding":{"provider":"claude"},"review":{"provider":"claude"},"small":{"provider":"claude"}}}`,
		"auto approval":       full(`{"provider":"claude","approval":"auto"}`),
		"unknown connection":  full(`{"provider":"claude","connection":"claude@nowhere"}`),
		"connection mismatch": full(`{"provider":"codex","connection":"opencode@openrouter"}`),
		"unknown adapter":     full(`{"provider":"nope"}`),
		"reviewer off claude": full(`{"provider":"codex","capability":"reviewer"}`),
		"unknown field":       `{"name":"X","colour":"red"}`,
	}
	for name, raw := range cases {
		stacks, bad := loadStacks(stacksPolicy(t, map[string]string{"mine": raw}))
		if bad["mine"] == nil {
			t.Errorf("%s: accepted %+v", name, stacks["mine"])
		}
		if len(bad) != 1 {
			t.Errorf("%s: one bad stack broke others: %v", name, bad)
		}
	}
	if _, err := resolveTier(stacksPolicy(t, map[string]string{"mine": cases["auto approval"]}), "mine", "frontier"); err == nil {
		t.Error("an invalid stack's tier resolved")
	}
	if _, err := resolveTier(agentpolicy.Policy{}, "anthropic", "huge"); err == nil {
		t.Error("an unknown tier resolved")
	}
}

// One start path. A stack alone starts its frontier tier; Advanced overrides
// sit on top; another adapter drops the tier's adapter-specific values; and
// an agent alone (wash ai --agent) starts that adapter on its defaults.
func TestStartProfile(t *testing.T) {
	pol := agentpolicy.Policy{}
	p, l, err := startProfile(pol, startReq{Stack: "openrouter"})
	if err != nil || p.Model != builtinStacks["openrouter"].Tiers["frontier"].Model || l.connection != "opencode@openrouter" || l.tier != "frontier" || l.stack != "openrouter" {
		t.Fatalf("stack alone: %+v %+v %v", p, l, err)
	}
	p, l, _ = startProfile(pol, startReq{Stack: "openrouter", Tier: "coding", Model: "openrouter/z-ai/glm-5.3"})
	if p.Model != "openrouter/z-ai/glm-5.3" || p.Thinking != "high" || l.connection != "opencode@openrouter" {
		t.Errorf("model override: %+v %+v", p, l)
	}
	p, l, _ = startProfile(pol, startReq{Stack: "openrouter", Tier: "coding", Agent: "codex"})
	if p.Provider != "codex" || p.Model != "" || p.Thinking != "" || l.connection != "" || l.stack != "openrouter" {
		t.Errorf("adapter override kept another adapter's settings: %+v %+v", p, l)
	}
	p, l, _ = startProfile(pol, startReq{Stack: "anthropic", Tier: "review"})
	if l.capability != "reviewer" || p.Model != "sonnet" {
		t.Errorf("review tier: %+v %+v", p, l)
	}
	p, l, err = startProfile(pol, startReq{Agent: "gemini"})
	if err != nil || p.Provider != "gemini" || l.stack != "" || l.tier != "" {
		t.Errorf("agent alone: %+v %+v %v", p, l, err)
	}
	if _, _, err := startProfile(pol, startReq{}); err == nil {
		t.Error("an empty request resolved")
	}
	if _, _, err := startProfile(pol, startReq{Stack: "nope"}); err == nil {
		t.Error("an unknown stack resolved")
	}
}

// The launcher's view: a stack is greyed with the reason its tiers cannot
// start, and the review tier says whether read-only is enforced.
func TestPublishStacksGreysWhatCannotStart(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"claude-agent-acp", "opencode"} {
		if err := os.WriteFile(filepath.Join(dir, bin), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	views := map[string]StackView{}
	for _, v := range publishStacks(agentpolicy.Policy{}, nil) {
		views[v.ID] = v
	}
	if v := views["anthropic"]; !v.Available || v.Tiers[2].ReadOnly != "enforced" || v.Tiers[0].Tier != "frontier" {
		t.Errorf("anthropic = %+v", v)
	}
	if v := views["openai"]; v.Available || !strings.Contains(v.Note, "codex-acp") || v.Tiers[2].ReadOnly != "instruction" {
		t.Errorf("openai without codex = %+v", v)
	}
	if v := views["openrouter"]; v.Available || v.Note != "no openrouter key set" {
		t.Errorf("openrouter without a key = %+v", v)
	}
	for _, v := range publishStacks(agentpolicy.Policy{}, map[string]string{"openrouter": "k"}) {
		if v.ID == "openrouter" && !v.Available {
			t.Errorf("openrouter with a key = %+v", v)
		}
	}
	for _, v := range publishStacks(stacksPolicy(t, map[string]string{"mine": `{"name":"Mine"}`}), nil) {
		if v.ID == "mine" && (v.Available || !strings.Contains(v.Note, "tier")) {
			t.Errorf("invalid user stack = %+v", v)
		}
	}
}

// A new key or an edited agents.json reaches the launcher on the next sweep,
// and an unchanged box pushes nothing.
func TestRefreshLaunchersPushesOnlyChanges(t *testing.T) {
	withPolicy(t, agentpolicy.Policy{})
	keys := map[string]string{}
	old := keyStore
	keyStore = func() map[string]string { return keys }
	t.Cleanup(func() { keyStore = old })
	var s State
	if !refreshLaunchers(&s) || len(s.Stacks) != 3 {
		t.Fatalf("first refresh: %+v", s.Stacks)
	}
	if refreshLaunchers(&s) {
		t.Error("an unchanged refresh reported a change")
	}
	keys["openrouter"] = "k"
	if !refreshLaunchers(&s) {
		t.Error("setting a key changed nothing")
	}
}

// A workspace tier and a launcher tier are the same AgentProfile.
func TestTierIsAnAgentProfile(t *testing.T) {
	p, err := resolveTier(agentpolicy.Policy{}, "anthropic", "coding")
	if err != nil {
		t.Fatal(err)
	}
	if err := swarm.ValidateProfile(p); err != nil {
		t.Fatal(err)
	}
}
