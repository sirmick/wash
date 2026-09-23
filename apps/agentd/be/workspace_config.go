package agentd

import (
	"fmt"
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
		fallbacks := []string{"model"}
		if category == "thought_level" {
			fallbacks = []string{"thought_level", "reasoning_effort", "thinking"}
		}
		for _, id := range fallbacks {
			if find(id) != nil {
				return id, nil
			}
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
		if option := find(id); option != nil && (option.Category == "model" || option.ID == "model") {
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
