package swarm

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ConfigurePatch changes a workspace's settings. Omitted fields are
// preserved.
type ConfigurePatch struct {
	Name       *string `json:"name"`
	MaxActive  *int    `json:"max_active"`
	MaxMembers *int    `json:"max_members"`
	// Legend says what the orchestrator's emojis and states mean.
	Legend *string `json:"legend"`
	// Roles are instruction templates by role; a null role removes one.
	Roles map[string]*RoleTemplate `json:"roles"`
	// ContextWarn is the share (0–1) of a member's context window at which
	// the orchestrator hears about it.
	ContextWarn *float64 `json:"context_warn"`
	// Catalog names the catalog members' models come from. agentd checks
	// that it exists; the store only keeps the name.
	Catalog *string `json:"catalog"`
	// Supervisor tunes the watchdog. It replaces the whole setting; {}
	// restores the defaults.
	Supervisor *Supervisor `json:"supervisor"`
	Expected   *int64      `json:"expected_revision"`
}

// Supervisor tunes the watchdog that tells the orchestrator when work has
// stalled (agentd workspace_supervisor.go). Empty fields take its defaults.
type Supervisor struct {
	Off bool `json:"off,omitempty"`
	// Quiet is how long a turn may go without a word, a tool or a busy
	// process before its member counts as wedged.
	Quiet string `json:"quiet,omitempty"`
	// Idle is how long a member may sit idle with open work, or the whole
	// team idle with the plan unfinished, before the orchestrator hears.
	Idle string `json:"idle,omitempty"`
	// Repeat is the wait before the same finding is sent again; it
	// doubles each time.
	Repeat string `json:"repeat,omitempty"`
	// MaxPrompts is how many times the same finding is sent before the
	// owner is told instead.
	MaxPrompts int `json:"max_prompts,omitempty"`
}

// Validate checks the durations and limits.
func (s Supervisor) Validate() error {
	for name, d := range map[string]string{"quiet": s.Quiet, "idle": s.Idle, "repeat": s.Repeat} {
		if d == "" {
			continue
		}
		v, err := time.ParseDuration(d)
		if err != nil || v < 10*time.Second || v > 24*time.Hour {
			return fmt.Errorf("supervisor.%s is a duration from 10s to 24h, e.g. \"2m\"", name)
		}
	}
	if s.MaxPrompts < 0 || s.MaxPrompts > 20 {
		return errors.New("supervisor.max_prompts is 0–20")
	}
	return nil
}

// RoleTemplate is a role's instructions, put before every new member's own.
type RoleTemplate struct {
	Instructions string `json:"instructions"`
}

// WithRole is a member's instructions with its role's template first.
func WithRole(w *Workspace, role, instructions string) string {
	if t := w.Roles[role]; t != "" && role != "" {
		return t + "\n\n" + instructions
	}
	return instructions
}

func ValidProfileName(s string) bool {
	if !ValidText(s, 80) {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func ValidateProfile(p AgentProfile) error {
	if p.Capability != "" && p.Capability != "reviewer" {
		return errors.New("unknown capability profile")
	}
	if p.Subagents != "" && p.Subagents != "allow" && p.Subagents != "deny" {
		return errors.New(`subagents must be "allow" or "deny"`)
	}
	if p.Approval != "" && p.Approval != "ask" && p.Approval != "auto" {
		return errors.New(`approval must be "ask" or "auto"`)
	}
	if p.Capability == "reviewer" && p.Approval == "auto" {
		return errors.New("reviewer capability cannot be auto-approved")
	}
	if p.Capability == "reviewer" {
		for id := range p.Configs {
			if id == "mode" || id == "permission_mode" || id == "sandbox" {
				return errors.New("reviewer capability cannot override permission mode")
			}
		}
	}
	if !ValidText(p.Provider, 80) || strings.TrimSpace(p.Provider) != p.Provider {
		return errors.New("profile requires a provider")
	}
	if p.Connection != "" && (!ValidText(p.Connection, 80) || strings.TrimSpace(p.Connection) != p.Connection) {
		return errors.New("invalid connection")
	}
	if p.Model != "" && !ValidText(p.Model, 256) || p.Effort != "" && !ValidText(p.Effort, 80) {
		return errors.New("invalid model or effort value")
	}
	if len(p.Configs) > 32 {
		return errors.New("maximum 32 adapter settings")
	}
	for id, value := range p.Configs {
		if !ValidText(id, 120) || !ValidText(value, 1024) {
			return errors.New("invalid adapter setting")
		}
	}
	return nil
}

func (s *Store) Configure(session string, p ConfigurePatch) (int64, error) {
	var revision int64
	err := s.Mutate(session, true, func(w *Workspace, _ *Member) error {
		if p.Expected != nil && *p.Expected != w.Revision {
			return fmt.Errorf("workspace revision conflict: expected %d, current %d", *p.Expected, w.Revision)
		}
		if p.Name != nil {
			if !ValidText(*p.Name, 160) {
				return errors.New("name must contain 1–160 bytes")
			}
			w.Name = *p.Name
		}
		if p.MaxActive != nil {
			w.MaxActive = *p.MaxActive
		}
		if p.MaxMembers != nil {
			w.MaxMembers = *p.MaxMembers
		}
		if w.MaxActive < 1 || w.MaxActive > 16 || w.MaxMembers < 1 || w.MaxMembers > 64 || w.MaxActive > w.MaxMembers {
			return errors.New("invalid limits: max_active 1–16, max_members 1–64, active <= members")
		}
		live := 0
		for _, m := range w.Members {
			if m.State != "ended" {
				live++
			}
		}
		if w.MaxMembers < live {
			return errors.New("max_members cannot be below current membership")
		}
		for role, t := range p.Roles {
			if !ValidProfileName(role) {
				return fmt.Errorf("role %q: letters, digits, - or _", role)
			}
			if t == nil {
				delete(w.Roles, role)
				continue
			}
			if !ValidText(t.Instructions, 8000) {
				return fmt.Errorf("role %s: instructions are 1–8000 bytes", role)
			}
			if w.Roles == nil {
				w.Roles = map[string]string{}
			}
			w.Roles[role] = t.Instructions
		}
		if p.ContextWarn != nil {
			if *p.ContextWarn <= 0 || *p.ContextWarn >= 1 {
				return errors.New("context_warn is a share of the window, between 0 and 1")
			}
			w.ContextWarn = *p.ContextWarn
		}
		if p.Legend != nil {
			if len(*p.Legend) > ReportLimit {
				return fmt.Errorf("legend is at most %d bytes", ReportLimit)
			}
			w.Legend = *p.Legend
		}
		if p.Catalog != nil {
			w.Catalog = *p.Catalog
		}
		if p.Supervisor != nil {
			if err := p.Supervisor.Validate(); err != nil {
				return err
			}
			w.Supervisor = *p.Supervisor
		}
		// The revision counts configuration changes only: bumped by every
		// mutation, an expected_revision read moments earlier went stale
		// whenever any member's turn delivered mail.
		w.Revision++
		revision = w.Revision
		return nil
	})
	return revision, err
}

// Overlay puts a member's explicit settings over its base, the catalog
// slot or model it resolved to. Explicit settings win field by field; configs
// merge by option ID. A provider override must match the base's: raw adapter option IDs and
// a connection must never cross provider boundaries.
func Overlay(base, explicit AgentProfile) (AgentProfile, error) {
	result := clone(base)
	if explicit.Provider != "" && result.Provider != "" && explicit.Provider != result.Provider {
		return result, errors.New("provider conflicts with the catalog's adapter")
	}
	if explicit.Provider != "" {
		result.Provider = explicit.Provider
	}
	if explicit.Capability != "" {
		result.Capability = explicit.Capability
	}
	if explicit.Approval != "" {
		result.Approval = explicit.Approval
	}
	if explicit.Model != "" {
		result.Model = explicit.Model
	}
	if explicit.Effort != "" {
		result.Effort = explicit.Effort
	}
	if explicit.Subagents != "" {
		result.Subagents = explicit.Subagents
	}
	if result.Configs == nil {
		result.Configs = map[string]string{}
	}
	for id, value := range explicit.Configs {
		result.Configs[id] = value
	}
	return result, ValidateProfile(result)
}
