package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"sort"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/pkg/sdk"
)

// Stacks: a named set of tiers, each tier the settings one session starts
// with. The launcher picks a stack and a tier instead of an adapter, and a
// workspace member says "tier":"coding" instead of carrying model strings.
//
// A tier is a swarm.AgentProfile, the same type a workspace's named
// profiles use: a stack is in effect a saved, global set of profiles. The
// defaults are data (stacks.json), because model names churn faster than
// code should; agents.json's `stacks` overrides them by key, tier by tier.
// Model values are the adapter's own option values, preferring names that
// follow new releases (Claude Code's `sonnet`, OpenRouter's
// `~vendor/family-latest`) where one points at the intended model.

// Tiers, in launcher order. A conversation starts on frontier by default.
var tierNames = []string{"frontier", "coding", "review", "small"}

const defaultTier = "frontier"

// Stack is one stack as stacks.json and agents.json write it.
type Stack struct {
	Name  string                        `json:"name"`
	Tiers map[string]swarm.AgentProfile `json:"tiers"`
}

// builtinStacks is stacks.json's `stacks`, decoded once. The file is in the
// binary, so a malformed one is a build defect, not a runtime condition.
var builtinStacks = func() map[string]Stack {
	var d struct {
		Stacks map[string]Stack `json:"stacks"`
	}
	if err := json.Unmarshal(launchDataJSON, &d); err != nil {
		panic("agentd: stacks.json: " + err.Error())
	}
	return d.Stacks
}()

// loadStacks merges agents.json's stacks over the built-in ones. A tier the
// user supplies replaces that tier; a name replaces the name. A stack that
// fails validation is returned with its error rather than dropped, so the
// launcher can grey it with the reason instead of it silently vanishing.
func loadStacks(pol agentpolicy.Policy) (map[string]Stack, map[string]error) {
	stacks := map[string]Stack{}
	for id, s := range builtinStacks {
		stacks[id] = Stack{Name: s.Name, Tiers: maps.Clone(s.Tiers)}
	}
	bad := map[string]error{}
	for id, raw := range pol.Stacks {
		var over Stack
		if err := decodeWorkspace(raw, &over); err != nil {
			bad[id] = fmt.Errorf("agents.json stack %q: %w", id, err)
			continue
		}
		s, ok := stacks[id]
		if !ok {
			s = Stack{Tiers: map[string]swarm.AgentProfile{}}
		}
		if over.Name != "" {
			s.Name = over.Name
		}
		maps.Copy(s.Tiers, over.Tiers)
		stacks[id] = s
	}
	for id, s := range stacks {
		if _, done := bad[id]; done {
			continue
		}
		if err := validateStack(pol, s); err != nil {
			bad[id] = fmt.Errorf("stack %q: %w", id, err)
		}
	}
	return stacks, bad
}

// validateStack applies the workspace profile rules to every tier, plus
// what only a stack needs: all four tiers, adapters and connections that
// exist, and no auto-approval. Auto-approval is granted per workspace by a
// session that has it; a global stack handing it to every launch would be a
// standing yes nobody gave.
func validateStack(pol agentpolicy.Policy, s Stack) error {
	if !swarm.ValidText(s.Name, 80) {
		return errors.New("needs a name")
	}
	for id := range s.Tiers {
		if !slices.Contains(tierNames, id) {
			return fmt.Errorf("unknown tier %q; tiers are %v", id, tierNames)
		}
	}
	conns := connections(pol)
	for _, id := range tierNames {
		t, ok := s.Tiers[id]
		if !ok {
			return fmt.Errorf("has no %s tier", id)
		}
		if err := swarm.ValidateProfile(t); err != nil {
			return fmt.Errorf("tier %s: %w", id, err)
		}
		if err := knownProvider(t.Provider); err != nil {
			return fmt.Errorf("tier %s: %w", id, err)
		}
		if t.Approval != "" {
			return fmt.Errorf("tier %s: a stack cannot set approval", id)
		}
		// Checked here so the stack is greyed with the reason, rather than
		// offered and then refused at launch (dialAdapterCapability).
		if t.Capability == "reviewer" && t.Provider != "claude" || t.Subagents == "deny" && t.Provider != "claude" {
			return fmt.Errorf("tier %s: capability and subagents are enforced on claude only", id)
		}
		if t.Connection != "" {
			c, ok := conns[t.Connection]
			if !ok {
				return fmt.Errorf("tier %s: unknown connection %q", id, t.Connection)
			}
			if c.Adapter != t.Provider {
				return fmt.Errorf("tier %s: connection %q is for %s, not %s", id, t.Connection, c.Adapter, t.Provider)
			}
		}
	}
	return nil
}

// resolveTier is the launch settings for one tier of one stack: a copy the
// caller may change.
func resolveTier(pol agentpolicy.Policy, stack, tier string) (swarm.AgentProfile, error) {
	stacks, bad := loadStacks(pol)
	if err := bad[stack]; err != nil {
		return swarm.AgentProfile{}, err
	}
	s, ok := stacks[stack]
	if !ok {
		return swarm.AgentProfile{}, fmt.Errorf("unknown stack %q", stack)
	}
	t, ok := s.Tiers[tier]
	if !ok {
		return swarm.AgentProfile{}, fmt.Errorf("unknown tier %q; tiers are %v", tier, tierNames)
	}
	t.Configs = maps.Clone(t.Configs)
	return t, nil
}

// StackView is a stack as the launcher shows it.
type StackView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Available is every tier startable here; Note says why not.
	Available bool       `json:"available"`
	Note      string     `json:"note,omitempty"`
	Tiers     []TierView `json:"tiers,omitempty"`
}

// TierView is one tier as the launcher shows it.
type TierView struct {
	Tier       string `json:"tier"`
	Adapter    string `json:"adapter"`
	Connection string `json:"connection,omitempty"`
	Model      string `json:"model,omitempty"`
	Thinking   string `json:"thinking,omitempty"`
	Capability string `json:"capability,omitempty"`
	// ReadOnly is set on the review tier: "enforced" where the adapter's
	// tools are restricted (Claude Code's reviewer capability), otherwise
	// "instruction" — the reviewer is asked not to write, and could.
	ReadOnly  string `json:"read_only,omitempty"`
	Available bool   `json:"available"`
	Note      string `json:"note,omitempty"`
}

// publishStacks is every stack with what this box can start, sorted by key
// (anthropic, openai, openrouter, then the user's own).
func publishStacks(pol agentpolicy.Policy, keys map[string]string) []StackView {
	stacks, bad := loadStacks(pol)
	ids := make([]string, 0, len(stacks))
	for id := range stacks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]StackView, 0, len(ids))
	for _, id := range ids {
		s := stacks[id]
		v := StackView{ID: id, Name: s.Name, Available: true}
		if v.Name == "" {
			v.Name = id
		}
		if err := bad[id]; err != nil {
			v.Available, v.Note = false, err.Error()
			out = append(out, v)
			continue
		}
		for _, name := range tierNames {
			t := s.Tiers[name]
			tv := TierView{Tier: name, Adapter: t.Provider, Connection: t.Connection, Model: t.Model, Thinking: t.Thinking, Capability: t.Capability}
			if name == "review" {
				tv.ReadOnly = "instruction"
				if t.Capability == "reviewer" {
					tv.ReadOnly = "enforced"
				}
			}
			tv.Available, tv.Note = connectionStatus(pol, keys, t.Provider, t.Connection)
			if !tv.Available && v.Available {
				v.Available, v.Note = false, tv.Note
			}
			v.Tiers = append(v.Tiers, tv)
		}
		out = append(out, v)
	}
	return out
}

// startProfile is what a start request launches: the stack's tier, with the
// Advanced overrides on top. Choosing another adapter drops the tier's
// model, effort and connection, which are that tier's adapter's values and
// mean nothing to another; the new adapter starts on its own defaults.
func startProfile(pol agentpolicy.Policy, req startReq) (swarm.AgentProfile, sessionLaunch, error) {
	var p swarm.AgentProfile
	tier := ""
	if req.Stack != "" {
		tier = req.Tier
		if tier == "" {
			tier = defaultTier
		}
		var err error
		if p, err = resolveTier(pol, req.Stack, tier); err != nil {
			return p, sessionLaunch{}, err
		}
	}
	if req.Agent != "" && req.Agent != p.Provider {
		p = swarm.AgentProfile{Provider: req.Agent}
	}
	if req.Model != "" {
		p.Model = req.Model
	}
	if p.Provider == "" {
		return p, sessionLaunch{}, errors.New("choose a stack, or an agent under Advanced")
	}
	return p, sessionLaunch{connection: p.Connection, capability: p.Capability, noSubagents: p.Subagents == "deny", stack: req.Stack, tier: tier}, nil
}

// startSession is the one way a new session starts from the launcher: launch
// the adapter through its connection, then apply the settings with the same
// check workspace members get, so a model the adapter does not offer fails
// here, naming the ones it does, rather than running on its default.
func startSession(req startReq, svcConn *sdk.Conn) (*hosted, error) {
	p, launch, err := startProfile(hostedPolicy(), req)
	if err != nil {
		return nil, err
	}
	h, err := startHostedCapability(p.Provider, req.Cwd, svcConn, launch)
	if err != nil {
		return nil, err
	}
	hostedMu.Lock()
	options := append([]acp.ConfigOption(nil), h.configs...)
	hostedMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()
	effective, err := configureWorkspaceSession(p, options, func(id, value string) ([]acp.ConfigOption, error) {
		res, e := h.client.SetConfigOption(ctx, h.sessionID, id, value)
		if e == nil {
			h.applyConfigs(res.ConfigOptions)
		}
		return res.ConfigOptions, e
	})
	if err != nil {
		h.retire()
		return nil, fmt.Errorf("%s: %w", p.Provider, err)
	}
	// What the adapter reports now, not what was asked: the two are checked
	// equal above, and this is the line to read when a session "ran on the
	// wrong model".
	log.Printf("agentd: session settings key=%s stack=%s tier=%s connection=%s adapter=%s effective=%v",
		h.key, launch.stack, launch.tier, launch.connection, p.Provider, effective)
	return h, nil
}
