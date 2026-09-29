package agentd

import (
	"os"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/workspacemcp"
)

// Discovery is read-only and available before setup. Report limits honestly:
// adapter mode names and role instructions do not establish a sandbox
// guarantee. A member gets its guide and what it can act on; an
// orchestrator, or a session with no workspace yet, gets the reference for
// setting one up.
func (ws *workspaceService) about(h *hosted) workspacemcp.Discovery {
	caller := map[string]any{"provider": h.agent, "session_id": h.sessionID, "role": "unattached"}
	if w := ws.store.View(h.sessionID); w != nil {
		caller["workspace_id"] = w.ID
		for _, m := range w.Members {
			if m.Session == h.sessionID {
				caller["member_id"], caller["key"], caller["can_spawn"] = m.ID, memberRef(m), m.CanSpawn
				caller["role"] = "member"
				if m.ID == w.Lead {
					caller["role"] = "orchestrator"
				}
				break
			}
		}
	}
	member := caller["role"] == "member"
	result := workspacemcp.About(member)
	result.Caller = caller
	h.mu.Lock()
	mode, yolo := h.mode, h.yolo
	options := append([]acp.ConfigOption(nil), h.configs...)
	settings := map[string]string{}
	for _, c := range h.configs {
		settings[c.ID] = c.CurrentValue
	}
	h.mu.Unlock()
	permissions := map[string]any{
		"adapter_mode": mode, "adapter_settings": settings,
		"host_auto_approval": yolo,
		"active_capability":  h.capability,
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
		permissions["workspace_approvals"] = map[string]any{
			"workspace": name,
			"rules":     rules,
			"scope":     "every member of this workspace, in any directory; consulted only where the host policy does not decide; ends with the workspace",
		}
	}
	result.Permissions = permissions
	if member {
		return result
	}

	caller["config_options"] = options
	permissions["host_policy_enabled"] = hostedPolicy().Enabled
	permissions["approval_order"] = "host policy rules, then session auto-approval, then human approval (or cancellation if unavailable/disabled)"
	permissions["filesystem_enforcement"] = "unknown: provider-specific; not verified by Wash workspace discovery"
	permissions["reviewer_capability_profiles"] = map[string]any{"reviewer": map[string]any{"provider": "claude", "adapter": "@agentclientprotocol/claude-agent-acp", "verified_versions": reviewerVerifiedVersions, "tools": []string{"Read", "Glob", "Grep", "scoped Wash coordination"}, "enforcement": "provider tool allowlist plus host write/terminal denial; not an OS sandbox"}, "codex": "unsupported: read-only mode uses a writable sandbox", "gemini": "unsupported", "opencode": map[string]any{"adapter": "OpenCode", "verified_versions": reviewerVerifiedOpenCode, "tools": []string{"read", "glob", "grep", "todowrite", "scoped Wash coordination"}, "enforcement": "write, edit, patch, bash, task, webfetch and skill tools removed and denied by launch configuration; not an OS sandbox"}}
	permissions["launch_setting_support"] = map[string]any{
		"reviewer":  providerCapability["reviewer"],
		"subagents": providerCapability["subagents"],
		"scope":     `which providers can enforce capability:"reviewer" and subagents:"deny"; configuring either on another provider is rejected before the member is committed`,
		"instructing_a_member_instead": "a member told not to spawn agents is not the same as one that cannot: can_spawn:false removes its Wash spawning authority only",
	}
	permissions["approval_profiles"] = map[string]any{
		"values":   []string{"ask", "auto"},
		"default":  "unset follows the launcher: the member is auto-approved while the session that launched it is, and follows the orchestrator's later toggles; reviewers never inherit",
		"ask":      "always ask the human, whatever the orchestrator does",
		"auto":     "host auto-approval from launch; host policy denies still win; every approval is narrated in the member's transcript",
		"granting": "only a session that is itself auto-approved can configure an auto member (children stay within the launcher's authority)",
		"lifetime": "kept on the member across a wash restart and restored on resume; ends with the workspace",
		"reviewer": "capability reviewer cannot be combined with auto",
	}
	// Which workspaces are open, and whether their orchestrators run here:
	// a stale one holding a QA directory is ended with workspace_end
	// workspace_id. And which agentd this is, when two are serving.
	for _, w := range ws.store.Snapshot().Workspaces {
		if w.State == "ended" {
			continue
		}
		result.OpenWorkspaces = append(result.OpenWorkspaces, map[string]any{"id": w.ID, "name": w.Name, "state": w.State, "project_root": w.Root, "orchestrator_running": hostedBySession(workspaceLeadSession(w)) != nil})
	}
	bin, _ := os.Executable()
	result.Agentd = map[string]any{"binary": bin, "pid": os.Getpid(), "started_at": agentdStarted.UTC().Format(time.RFC3339)}
	return result
}

// agentdStarted is when this agentd started, for about.
var agentdStarted = time.Now()
