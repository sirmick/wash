package agentd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
)

// This is a provider tool capability, not an OS sandbox. Pin the adapter contract:
// unknown versions must be reviewed before we claim their launch metadata works.
// Observed in claude-agent-acp 0.79.0 createSession: _meta.claudeCode.options is
// passed to the SDK on both new and load. Codex's read-only mode is workspaceWrite.
//
// 0.81.1 re-verified against its dist/acp-agent.js: tools comes from options;
// disallowedTools is merged with the adapter's own; settingSources and
// strictMcpConfig reach the SDK through the options spread, after the
// adapter's defaults; settings (disableAllHooks) survives the provider merge;
// allowDangerouslySkipPermissions:false turns allowBypass off; permission
// requests for MCP tools carry _meta.claudeCode.mcpServer {name, source}.
var reviewerVerifiedVersions = []string{"0.79.0", "0.81.1"}

func reviewerMetadata(provider string, info acp.Implementation) (map[string]any, error) {
	if provider != "claude" || info.Name != "@agentclientprotocol/claude-agent-acp" || !slices.Contains(reviewerVerifiedVersions, info.Version) {
		return nil, fmt.Errorf("reviewer capability unsupported by %s %s: requires verified claude-agent-acp %s; mode names are not read-only guarantees", info.Name, info.Version, strings.Join(reviewerVerifiedVersions, " or "))
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
	meta, ok := claudeToolMeta(tc)
	if !ok {
		return false
	}
	// A foreign MCP tool cannot gain approval by claiming a read/search kind.
	if meta.Server.Name == "" && !strings.HasPrefix(meta.Tool, "mcp__") {
		return tc.Kind == acp.ToolKindRead || tc.Kind == acp.ToolKindSearch
	}
	tool, ok := washWorkspaceTool(tc)
	return ok && reviewerWorkspaceTool(tool)
}

// claudeMeta is what claude-agent-acp says about a tool call beyond its
// ACP kind: the provider's own tool name and, for MCP tools, which server.
type claudeMeta struct {
	Tool   string `json:"toolName"`
	Server struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	} `json:"mcpServer"`
}

func claudeToolMeta(tc acp.ToolCall) (claudeMeta, bool) {
	var meta struct {
		Claude claudeMeta `json:"claudeCode"`
	}
	if len(tc.Meta) > 0 && json.Unmarshal(tc.Meta, &meta) != nil {
		return claudeMeta{}, false
	}
	return meta.Claude, true
}

// washWorkspaceTool names the wash_workspace tool a call invokes, when it is
// one: the server Wash injected itself ("dynamic"), not a same-named server
// from the user's own configuration.
func washWorkspaceTool(tc acp.ToolCall) (string, bool) {
	meta, ok := claudeToolMeta(tc)
	const prefix = "mcp__wash_workspace__"
	if !ok || meta.Server.Name != "wash_workspace" || meta.Server.Source != "dynamic" || !strings.HasPrefix(meta.Tool, prefix) {
		return "", false
	}
	return strings.TrimPrefix(meta.Tool, prefix), true
}

// coordinationPermission is the approval a workspace's own coordination
// calls get without a human. Asking gained nothing: the bridge derives the
// caller from its session credentials and enforces every role limit itself
// (only the orchestrator configures, a reviewer cannot spawn), so a click
// could only ever approve what the server was going to check anyway. What it
// cost was one prompt per inbox read, status report and message, per member —
// a question with no subject that trained the human to click without reading.
func coordinationPermission(tc acp.ToolCall) bool {
	_, ok := washWorkspaceTool(tc)
	return ok
}

// savedWorkspaceLaunch is what a reopened session was launched as: its
// capability, and whether it is a member (not the workspace's lead).
func savedWorkspaceLaunch(session string) (capability string, member bool) {
	if workspaces != nil {
		for _, w := range workspaces.store.Snapshot().Workspaces {
			for _, m := range w.Members {
				if m.Session == session {
					if m.LaunchSettings != nil {
						capability = m.LaunchSettings.Capability
					}
					return capability, m.ID != w.Lead && m.State != "ended"
				}
			}
		}
	}
	return "", false
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
