package swarm

import (
	"errors"
	"fmt"
	"strings"
)

// ConfigurePatch changes a workspace's settings. Omitted fields are
// preserved; a null package deletes it.
type ConfigurePatch struct {
	Name       *string             `json:"name"`
	MaxActive  *int                `json:"max_active"`
	MaxMembers *int                `json:"max_members"`
	Packages   map[string]*Package `json:"packages"`
	// Catalog names the catalog members' models come from. agentd checks
	// that it exists; the store only keeps the name.
	Catalog  *string `json:"catalog"`
	Expected *int64  `json:"expected_revision"`
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
		for code, pkg := range p.Packages {
			if !ValidProfileName(code) {
				return errors.New("package code must be 1–80 ASCII letters, digits, underscores or hyphens")
			}
			if pkg == nil {
				delete(w.Packages, code)
				continue
			}
			if !ValidText(pkg.Title, 120) {
				return fmt.Errorf("package %q: title must contain 1–120 bytes", code)
			}
			if w.Packages == nil {
				w.Packages = map[string]Package{}
			}
			w.Packages[code] = *pkg
		}
		if len(w.Packages) > 64 {
			return errors.New("maximum 64 packages")
		}
		if p.Catalog != nil {
			w.Catalog = *p.Catalog
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
