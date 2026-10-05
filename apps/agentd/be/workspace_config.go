package agentd

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/swarm"
)

// Order matters: the mode first, then the model, then the effort. Claude
// Code re-picks the model when its mode changes and narrows the effort
// choices when the model changes, so each is set only once the setting it
// depends on is in place. Every step uses the full, authoritative option
// list returned by ACP.
//
// The settings are advisory. A value the adapter does not offer (a model id
// not in its list, an effort level it has no setting for) is skipped and
// returned as a note; the session runs on what the adapter has, and the
// caller records the note on the member. Wash is agnostic to the provider,
// so a catalog written against one adapter's list must not fail a launch on
// another's — that cost the Redoubt Architect and every "opus-5-5" slot.
// What still fails: a contradiction in the request (a semantic model and a
// raw model config that disagree), and an adapter that errors on a value it
// said it offers.
func configureWorkspaceSession(settings swarm.AgentProfile, options []acp.ConfigOption, set func(string, string) ([]acp.ConfigOption, error)) (effective map[string]string, notes []string, err error) {
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
	skip := func(id, value, why string) {
		notes = append(notes, fmt.Sprintf("%s=%q not applied: %s", id, value, why))
		delete(pending, id)
	}
	apply := func(id, value string) error {
		option := find(id)
		if option == nil {
			skip(id, value, "the adapter has no such setting")
			return nil
		}
		if len(option.Options) > 0 && !slices.ContainsFunc(option.Options, func(v acp.ConfigOptionValue) bool { return v.Value == value }) {
			values := make([]string, 0, len(option.Options))
			for _, v := range option.Options {
				values = append(values, v.Value)
			}
			skip(id, value, fmt.Sprintf("not offered by the adapter; it runs %q (available: %s)", option.CurrentValue, strings.Join(values, ", ")))
			return nil
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
		return "", nil
	}
	semantic := func(category, value string) error {
		id, err := categoryID(category)
		if err != nil {
			return err
		}
		if id == "" {
			notes = append(notes, fmt.Sprintf("%s=%q not applied: the adapter exposes no %s setting", category, value, category))
			return nil
		}
		if raw, ok := pending[id]; ok && raw != value {
			return fmt.Errorf("conflicting %s and configs[%q] values", category, id)
		}
		return apply(id, value)
	}
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	// The mode first: Claude Code re-picks the model when its mode changes
	// (a member launched on haiku with configs {mode: plan} reported sonnet
	// a moment after this check had passed — the shakedown's finding 3),
	// while a model set after the mode stays. Verified live 2026-09-25.
	for _, id := range ids {
		if option := find(id); option != nil && option.Category == "mode" {
			if err := apply(id, pending[id]); err != nil {
				return nil, notes, err
			}
		}
	}
	if settings.Model != "" {
		if err := semantic("model", settings.Model); err != nil {
			return nil, notes, err
		}
	}
	// A caller may use the raw model option instead of the semantic shortcut.
	for _, id := range ids {
		if option := find(id); option != nil && option.Category == "model" {
			if err := apply(id, pending[id]); err != nil {
				return nil, notes, err
			}
		}
	}
	if settings.Effort != "" {
		if err := semantic("thought_level", settings.Effort); err != nil {
			return nil, notes, err
		}
	}
	for _, id := range ids {
		if value, ok := pending[id]; ok {
			if err := apply(id, value); err != nil {
				return nil, notes, err
			}
		}
	}
	effective = map[string]string{}
	for _, option := range options {
		effective[option.ID] = option.CurrentValue
	}
	// An adapter may coerce a value without reporting an RPC error. The
	// session is not stopped for it — the adapter chose, as it does for a
	// value it never offered — but the substitution is on the record, so
	// "it ran on the wrong model" has a line to read.
	for id, value := range requested {
		if effective[id] != value {
			notes = append(notes, fmt.Sprintf("%s=%q not kept by the adapter; it runs %q", id, value, effective[id]))
		}
	}
	slices.Sort(notes)
	return effective, notes, nil
}

// restoreWorkspaceSession reapplies launch settings to a session that was
// loaded rather than created. Unlike a launch it must not fail on a value
// the loaded session does not offer: session/load keeps the model, but the
// adapter may name it differently afterwards (observed: a session launched
// on "claude-fable-5-1[1m]" loads with 1M context as "claude-fable-5-1", from
// a list then reading only "default, opus"), while the effort level really
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
	if settings.Effort != "" && !offered("thought_level", settings.Effort) {
		skipped = append(skipped, "effort="+settings.Effort)
		settings.Effort = ""
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
	_, notes, err := configureWorkspaceSession(settings, options, set)
	skipped = append(skipped, notes...)
	slices.Sort(skipped)
	return skipped, err
}
