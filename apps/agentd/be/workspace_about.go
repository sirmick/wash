package agentd

import (
	"encoding/json"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/workspacemcp"
)

// Discovery is read-only and available before setup. Report limits honestly:
// adapter mode names and role instructions do not establish a sandbox guarantee.
func (ws *workspaceService) about(h *hosted) map[string]any {
	result := workspacemcp.About()
	caller := map[string]any{"provider": h.agent, "session_id": h.sessionID, "role": "unattached"}
	if w := ws.store.View(h.sessionID); w != nil {
		caller["workspace_id"] = w.ID
		for _, m := range w.Members {
			if m.Session == h.sessionID {
				caller["member_id"], caller["can_spawn"] = m.ID, m.CanSpawn
				caller["role"] = "member"
				if m.ID == w.Lead {
					caller["role"] = "orchestrator"
				}
				break
			}
		}
	}
	hostedMu.Lock()
	mode, yolo := h.mode, h.yolo
	optionsJSON, _ := json.Marshal(h.configs)
	var options []acp.ConfigOption
	_ = json.Unmarshal(optionsJSON, &options)
	settings := map[string]string{}
	for _, c := range h.configs {
		settings[c.ID] = c.CurrentValue
	}
	hostedMu.Unlock()
	pol := hostedPolicy()
	caller["config_options"] = options
	result["caller"] = caller
	result["permissions"] = map[string]any{
		"adapter_mode": mode, "adapter_settings": settings,
		"host_auto_approval": yolo, "host_policy_enabled": pol.Enabled,
		"approval_order":               "host policy rules, then session auto-approval, then human approval (or cancellation if unavailable/disabled)",
		"filesystem_enforcement":       "unknown: provider-specific; not verified by Wash workspace discovery",
		"reviewer_capability_profiles": map[string]any{"reviewer": map[string]any{"provider": "claude", "adapter": "@agentclientprotocol/claude-agent-acp", "verified_versions": []string{"0.79.0"}, "tools": []string{"Read", "Glob", "Grep", "scoped Wash coordination"}, "enforcement": "provider tool allowlist plus host write/terminal denial; not an OS sandbox"}, "codex": "unsupported: read-only mode uses a writable sandbox", "gemini": "unsupported"},
		"active_capability":            h.capability,
	}
	return result
}
