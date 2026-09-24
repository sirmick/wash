package agentd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
)

// This is a provider tool capability, not an OS sandbox. Pin the adapter contract:
// unknown versions must be reviewed before we claim their launch metadata works.
// Observed in claude-agent-acp 0.79.0 createSession: _meta.claudeCode.options is
// passed to the SDK on both new and load. Codex's read-only mode is workspaceWrite.
func reviewerMetadata(provider string, info acp.Implementation) (map[string]any, error) {
	if provider != "claude" || info.Name != "@agentclientprotocol/claude-agent-acp" || info.Version != "0.79.0" {
		return nil, fmt.Errorf("reviewer capability unsupported by %s %s: requires verified claude-agent-acp 0.79.0; mode names are not read-only guarantees", info.Name, info.Version)
	}
	return map[string]any{"claudeCode": map[string]any{"options": map[string]any{
		"tools":           []string{"Read", "Glob", "Grep"},
		"disallowedTools": []string{"Write", "Edit", "MultiEdit", "NotebookEdit", "Bash", "Agent", "Task", "Skill", "EnterWorktree", "ExitWorktree"},
		"settingSources":  []string{}, "strictMcpConfig": true,
		"allowDangerouslySkipPermissions": false,
		"settings":                        map[string]any{"disableAllHooks": true},
	}}}, nil
}
func reviewerWorkspaceTool(name string) bool {
	switch name {
	case "workspace_get", "inbox_read", "inbox_ack", "member_update", "message_send", "assignment_update", "decision_request", "flash_message":
		return true
	}
	return false
}
func (h *hosted) reviewerPermission(tc acp.ToolCall) bool {
	var meta struct {
		Claude struct {
			Tool   string `json:"toolName"`
			Server struct {
				Name   string `json:"name"`
				Source string `json:"source"`
			} `json:"mcpServer"`
		} `json:"claudeCode"`
	}
	if len(tc.Meta) > 0 && json.Unmarshal(tc.Meta, &meta) != nil {
		return false
	}
	// A foreign MCP tool cannot gain approval by claiming a read/search kind.
	if meta.Claude.Server.Name == "" && !strings.HasPrefix(meta.Claude.Tool, "mcp__") {
		return tc.Kind == acp.ToolKindRead || tc.Kind == acp.ToolKindSearch
	}
	const prefix = "mcp__wash_workspace__"
	return meta.Claude.Server.Name == "wash_workspace" && meta.Claude.Server.Source == "dynamic" && strings.HasPrefix(meta.Claude.Tool, prefix) && reviewerWorkspaceTool(strings.TrimPrefix(meta.Claude.Tool, prefix))
}
func savedWorkspaceCapability(session string) string {
	if workspaces != nil {
		for _, w := range workspaces.store.Snapshot().Workspaces {
			for _, m := range w.Members {
				if m.Session == session && m.LaunchSettings != nil {
					return m.LaunchSettings.Capability
				}
			}
		}
	}
	return ""
}

// workspaceApprovalPolicy is the decision path's one view of workspace-scoped
// rules. Nil-safe on purpose: the workspace service is optional (it logs and
// carries on if it cannot start), and an ordinary agent conversation has no
// workspace at all, so both must read as "nothing to say" rather than as a
// reason for the permission ladder to behave differently.
func workspaceApprovalPolicy(session string) (id, name string, policy agentpolicy.Policy) {
	if workspaces == nil || session == "" {
		return "", "", agentpolicy.Policy{}
	}
	return workspaces.store.ApprovalsFor(session)
}

// decideWithWorkspace runs the two rule tables in the order the permission
// ladder requires, and names which one answered so the log can say.
//
// The global table stays authoritative in BOTH directions. An allow or a deny
// in ~/.config/wash/agents.json is a decision the user made about this whole
// machine, and a per-workspace table — which an agent's own question can talk
// the user into writing — must be able to reopen neither. So the workspace
// table is reached only where global said "ask", which is exactly the case
// that would otherwise become one prompt per member per worktree.
func decideWithWorkspace(global, workspace agentpolicy.Policy, req agentpolicy.Request) (agentpolicy.Response, string) {
	res := agentpolicy.Evaluate(global, req)
	if res.Decision != agentpolicy.DecisionAsk || !workspace.Enabled {
		return res, "global"
	}
	if w := agentpolicy.Evaluate(workspace, req); w.Decision != agentpolicy.DecisionAsk {
		return w, "workspace"
	}
	return res, "global"
}
