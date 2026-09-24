package agentd

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/swarm"
)

// Apply model first: adapters can change thinking choices when the model changes.
// Every subsequent step uses the full, authoritative option list returned by ACP.
func configureWorkspaceSession(settings swarm.AgentProfile, options []acp.ConfigOption, set func(string, string) ([]acp.ConfigOption, error)) (map[string]string, error) {
	pending := map[string]string{}
	for id, value := range settings.Configs {
		pending[id] = value
	}
	requested := map[string]string{}
	find := func(id string) *acp.ConfigOption {
		for i := range options {
			if options[i].ID == id {
				return &options[i]
			}
		}
		return nil
	}
	apply := func(id, value string) error {
		option := find(id)
		if option == nil {
			return fmt.Errorf("unsupported adapter setting %q", id)
		}
		if len(option.Options) > 0 && !slices.ContainsFunc(option.Options, func(v acp.ConfigOptionValue) bool { return v.Value == value }) {
			values := make([]string, 0, len(option.Options))
			for _, v := range option.Options {
				values = append(values, v.Value)
			}
			return fmt.Errorf("unsupported value %q for %s; available values: %s", value, id, strings.Join(values, ", "))
		}
		next, err := set(id, value)
		if err != nil {
			return fmt.Errorf("setting %s: %w", id, err)
		}
		options = next
		requested[id] = value
		delete(pending, id)
		return nil
	}
	categoryID := func(category string) (string, error) {
		ids := []string{}
		for _, option := range options {
			if option.Category == category {
				ids = append(ids, option.ID)
			}
		}
		if len(ids) == 1 {
			return ids[0], nil
		}
		if len(ids) > 1 {
			return "", fmt.Errorf("ambiguous %s options; use explicit configs instead", category)
		}
		return "", fmt.Errorf("adapter does not expose %s; inspect workspace_get config_options", category)
	}
	semantic := func(category, value string) error {
		id, err := categoryID(category)
		if err != nil {
			return err
		}
		if raw, ok := pending[id]; ok && raw != value {
			return fmt.Errorf("conflicting %s and configs[%q] values", category, id)
		}
		return apply(id, value)
	}
	if settings.Model != "" {
		if err := semantic("model", settings.Model); err != nil {
			return nil, err
		}
	}
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	// A caller may use the raw model option instead of the semantic shortcut.
	for _, id := range ids {
		if option := find(id); option != nil && option.Category == "model" {
			if err := apply(id, pending[id]); err != nil {
				return nil, err
			}
		}
	}
	if settings.Thinking != "" {
		if err := semantic("thought_level", settings.Thinking); err != nil {
			return nil, err
		}
	}
	for _, id := range ids {
		if value, ok := pending[id]; ok {
			if err := apply(id, value); err != nil {
				return nil, err
			}
		}
	}
	effective := map[string]string{}
	for _, option := range options {
		effective[option.ID] = option.CurrentValue
	}
	// An adapter may reject/coerce a value without reporting an RPC error. Never
	// start work under a silently substituted model or thinking level.
	for id, value := range requested {
		if effective[id] != value {
			return nil, fmt.Errorf("adapter did not retain %s=%q (reported %q)", id, value, effective[id])
		}
	}
	return effective, nil
}

// restoreWorkspaceSession reapplies launch settings to a session that was
// loaded rather than created. Unlike a launch it must not fail on a value
// the loaded session does not offer: session/load keeps the model, but the
// adapter may name it differently afterwards (observed: a session launched
// on "claude-fable-5-1[1m]" loads with 1M context as "claude-fable-5-1", from
// a list then reading only "default, opus"), while the thinking level really
// is lost. So a setting the session does not offer is skipped and reported,
// and everything it does offer is still applied — dropping effort because
// the model's label moved is the bug this exists to fix.
func restoreWorkspaceSession(settings swarm.AgentProfile, options []acp.ConfigOption, set func(string, string) ([]acp.ConfigOption, error)) (skipped []string, err error) {
	offered := func(category, value string) bool {
		for _, o := range options {
			if o.Category != category {
				continue
			}
			return len(o.Options) == 0 || slices.ContainsFunc(o.Options, func(v acp.ConfigOptionValue) bool { return v.Value == value })
		}
		return false
	}
	if settings.Model != "" && !offered("model", settings.Model) {
		skipped = append(skipped, "model="+settings.Model)
		settings.Model = ""
	}
	if settings.Thinking != "" && !offered("thought_level", settings.Thinking) {
		skipped = append(skipped, "thinking="+settings.Thinking)
		settings.Thinking = ""
	}
	settings.Configs = maps.Clone(settings.Configs)
	for id, value := range settings.Configs {
		ok := false
		for _, o := range options {
			if o.ID == id {
				ok = len(o.Options) == 0 || slices.ContainsFunc(o.Options, func(v acp.ConfigOptionValue) bool { return v.Value == value })
			}
		}
		if !ok {
			skipped = append(skipped, id+"="+value)
			delete(settings.Configs, id)
		}
	}
	slices.Sort(skipped)
	_, err = configureWorkspaceSession(settings, options, set)
	return skipped, err
}
