package agentproto

import "encoding/json"

// Workspace messages: the team sidebar in an orchestrator's Agent window,
// and the human's actions on it. The workspace's MCP tools, which agents
// call, are a separate surface (internal/workspacemcp).

// WorkspaceRefresh asks for the session's workspace frame again.
type WorkspaceRefresh struct {
	Key string `json:"key"`
}

// WorkspaceAction is a human's action in the workspace sidebar.
type WorkspaceAction struct {
	Key string `json:"key"`
	// Name is the operation: decision_response | member_open |
	// member_resume | member_message | member_inspect.
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func init() {
	register(Spec{Kind: "workspace_refresh", Dir: Request, Payload: WorkspaceRefresh{}, From: "the session's controller", Reply: "workspace_state",
		Doc: "Resend the workspace frame for the session."})
	register(Spec{Kind: "workspace_action", Dir: Request, Payload: WorkspaceAction{}, From: "the session's controller", Reply: "workspace_result",
		Doc: "A human's action in the workspace sidebar."})
}
