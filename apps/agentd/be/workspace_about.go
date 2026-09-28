package agentd

import (
	"encoding/json"
	"os"
	"time"

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
	// Which workspaces are open, and whether their orchestrators run here:
	// a stale one holding a QA directory is ended with workspace_end
	// workspace_id. And which agentd this is, when two are serving.
	open := []map[string]any{}
	for _, w := range ws.store.Snapshot().Workspaces {
		if w.State == "ended" {
			continue
		}
		open = append(open, map[string]any{"id": w.ID, "name": w.Name, "state": w.State, "project_root": w.Root, "orchestrator_running": workspaceHosted(workspaceLeadSession(w)) != nil})
	}
	result["open_workspaces"] = open
	bin, _ := os.Executable()
	result["agentd"] = map[string]any{"binary": bin, "pid": os.Getpid(), "started_at": agentdStarted.UTC().Format(time.RFC3339)}
	if caller["role"] == "member" {
		names := []string{}
		for _, t := range workspacemcp.MemberTools() {
			names = append(names, t.Name)
		}
		result["tools"] = names
	}
	result["permissions"] = map[string]any{
		"adapter_mode": mode, "adapter_settings": settings,
		"host_auto_approval": yolo, "host_policy_enabled": pol.Enabled,
		"approval_order":               "host policy rules, then session auto-approval, then human approval (or cancellation if unavailable/disabled)",
		"filesystem_enforcement":       "unknown: provider-specific; not verified by Wash workspace discovery",
		"reviewer_capability_profiles": map[string]any{"reviewer": map[string]any{"provider": "claude", "adapter": "@agentclientprotocol/claude-agent-acp", "verified_versions": reviewerVerifiedVersions, "tools": []string{"Read", "Glob", "Grep", "scoped Wash coordination"}, "enforcement": "provider tool allowlist plus host write/terminal denial; not an OS sandbox"}, "codex": "unsupported: read-only mode uses a writable sandbox", "gemini": "unsupported", "opencode": map[string]any{"adapter": "OpenCode", "verified_versions": reviewerVerifiedOpenCode, "tools": []string{"read", "glob", "grep", "todowrite", "scoped Wash coordination"}, "enforcement": "write, edit, patch, bash, task, webfetch and skill tools removed and denied by launch configuration; not an OS sandbox"}},
		"active_capability":            h.capability,
		"approval_profiles": map[string]any{
			"values":   []string{"ask", "auto"},
			"default":  "unset follows the launcher: the member is auto-approved while the session that launched it is, and follows the orchestrator's later toggles; reviewers never inherit",
			"ask":      "always ask the human, whatever the orchestrator does",
			"auto":     "host auto-approval from launch; host policy denies still win; every approval is narrated in the member's transcript",
			"granting": "only a session that is itself auto-approved can configure an auto member (children stay within the launcher's authority)",
			"lifetime": "kept on the member across a wash restart and restored on resume; ends with the workspace",
			"reviewer": "capability reviewer cannot be combined with auto",
		},
	}
	// Workspace-scoped approvals, reported because an agent reasoning about
	// what it may do should not have to infer it from which prompts it
	// stopped seeing. Rules only — they say what was granted; they are not
	// a claim about what the host would otherwise have asked.
	if id, name, wpol := workspaceApprovalPolicy(h.sessionID); id != "" {
		rules := make([]map[string]string, 0, len(wpol.Rules))
		for _, r := range wpol.Rules {
			rules = append(rules, map[string]string{"match": r.Match, "decision": r.Decision})
		}
		result["permissions"].(map[string]any)["workspace_approvals"] = map[string]any{
			"workspace": name,
			"rules":     rules,
			"scope":     "every member of this workspace, in any directory; consulted only where the host policy does not decide; ends with the workspace",
		}
	}
	return result
}

// agentdStarted is when this agentd started, for about.
var agentdStarted = time.Now()
