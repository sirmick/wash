package agentproto

import (
	"encoding/json"

	"github.com/sirmick/wash/internal/swarm"
)

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
	// Name is the operation: member_open | member_resume |
	// member_message | member_inspect.
	Name      string              `json:"name"`
	Arguments WorkspaceActionArgs `json:"arguments"`
}

// WorkspaceActionArgs are an action's arguments; each operation reads its
// own.
type WorkspaceActionArgs struct {
	// MemberID names the member for member_open, member_resume and
	// member_inspect.
	MemberID string `json:"member_id,omitempty"`
	// Recipient is the member a member_message goes to.
	Recipient string `json:"recipient,omitempty"`
	// Body is a member_message's text.
	Body string `json:"body,omitempty"`
}

// WorkspaceState is the whole sidebar frame for an orchestrator's (or a
// member's) Agent window. Sent in full on refresh and when a patch cannot
// apply; otherwise changes arrive as WorkspacePatch.
type WorkspaceState struct {
	Key string `json:"key"`
	// Sequence numbers this frame; a patch names the sequence it applies to.
	Sequence int64 `json:"sequence"`
	// Workspace is the workspace this session belongs to, or null when it
	// belongs to none (the sidebar is then not shown).
	Workspace *swarm.Workspace `json:"workspace"`
	// Preview is the transcript of the member the window has selected.
	Preview *WorkspaceTranscript `json:"preview,omitempty"`
	// Activity and ActivityDetail are each member's live activity (by
	// member id): thinking, tool use, responding, waiting, …
	Activity       map[string]string `json:"activity,omitempty"`
	ActivityDetail map[string]string `json:"activity_detail,omitempty"`
	// Usage is each member's context accounting, by member id.
	Usage map[string]swarm.Usage `json:"usage,omitempty"`
	// Approvals are the members' questions waiting for the human.
	Approvals []WorkspaceApproval `json:"approvals,omitempty"`
	// Questions are the members' question sets waiting for the human.
	Questions []PendingQuestion `json:"questions,omitempty"`
	// QAMarkdown is the QA document as it is written to disk.
	QAMarkdown       string            `json:"qa_markdown,omitempty"`
	QADocumentStatus *QADocumentStatus `json:"qa_document_status,omitempty"`
	// PlanFileStatus is where the plan file stands on disk.
	PlanFileStatus *QADocumentStatus `json:"plan_file_status,omitempty"`
}

// WorkspaceTranscript is a member's recent transcript: the selected
// member's preview, or member_inspect's answer.
type WorkspaceTranscript struct {
	MemberID string  `json:"member_id"`
	Events   []Event `json:"events"`
	// Asks are the member's questions waiting for the human, when it runs.
	Asks []Ask `json:"asks,omitempty"`
	// Questions are the member's question sets waiting for the human.
	Questions []PendingQuestion `json:"questions,omitempty"`
	// Note says what the transcript is when it is not live: an ended
	// member's archive.
	Note string `json:"note,omitempty"`
}

// WorkspaceApproval is a member's question waiting for the human.
type WorkspaceApproval struct {
	ID       string `json:"id"`
	MemberID string `json:"member_id"`
	Tool     string `json:"tool"`
	Subject  string `json:"subject"`
}

// QADocumentStatus is where a file (or directory) Wash writes for the
// workspace stands on disk: the QA thread files, the plan file.
type QADocumentStatus struct {
	Path string `json:"path"`
	// State is unconfigured | pending | saved | error.
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
	Updated int64  `json:"updated_at,omitempty"`
}

// WorkspacePatch changes a WorkspaceState: the frame fields and workspace
// fields that changed (null removes one), and the plan's nodes by id. It
// applies to the frame with sequence Base; a frontend holding another asks
// for the whole frame again (workspace_refresh).
type WorkspacePatch struct {
	Key      string `json:"key"`
	Base     int64  `json:"base"`
	Sequence int64  `json:"sequence"`
	// Frame holds changed WorkspaceState fields by their JSON name.
	Frame map[string]json.RawMessage `json:"frame"`
	// Workspace holds changed swarm.Workspace fields, except the plan.
	Workspace map[string]json.RawMessage `json:"workspace"`
	Plan      *WorkspacePlanPatch        `json:"plan,omitempty"`
}

// WorkspacePlanPatch changes the plan: nodes replaced or added, ids
// removed, and the new order when it changed.
type WorkspacePlanPatch struct {
	Upsert []swarm.Node `json:"upsert"`
	Remove []string     `json:"remove"`
	Order  []string     `json:"order,omitempty"`
}

// WorkspaceResult answers a WorkspaceAction.
type WorkspaceResult struct {
	Key       string `json:"key"`
	Operation string `json:"operation"`
	Error     string `json:"error,omitempty"`
	// Transcript answers member_inspect.
	Transcript *WorkspaceTranscript `json:"transcript,omitempty"`
}

func init() {
	register(Spec{Kind: "workspace_state", Dir: Push, Payload: WorkspaceState{}, From: "the session's controller", Class: Bulk, Keyed: true,
		Doc: "The whole workspace sidebar frame."})
	register(Spec{Kind: "workspace_patch", Dir: Push, Payload: WorkspacePatch{}, From: "the session's controller", Class: Bulk, Keyed: true,
		Doc: "What changed in the frame since the frame with sequence base."})
	register(Spec{Kind: "workspace_result", Dir: Push, Payload: WorkspaceResult{}, From: "the acting controller", Keyed: true,
		Doc: "The outcome of a workspace_action."})
	register(Spec{Kind: "workspace_refresh", Dir: Request, Payload: WorkspaceRefresh{}, From: "the session's controller", Reply: "workspace_state",
		Doc: "Resend the workspace frame for the session."})
	register(Spec{Kind: "workspace_action", Dir: Request, Payload: WorkspaceAction{}, From: "the session's controller", Reply: "workspace_result",
		Doc: "A human's action in the workspace sidebar."})
}
