// ACP v1 wire types (docs/AGENT_APP.md §5).
//
// Version note: v1 is the current stable protocol version; a v2 schema
// exists upstream and restructures several of these — the permission
// request in particular (v2 replaces toolCall+typed option kinds with
// title/description/subject and accept/decline/cancel outcomes). Version
// is negotiated in `initialize`, so this file is deliberately one version's
// worth of shapes rather than a union; the day v2 matters, it gets its own
// file and Client picks by the negotiated number.
//
// These shapes are transcribed from the v1 spec, not from observed
// traffic. The first real adapter run is what confirms them — see the
// conformance note in AGENT_APP.md §13.
package acp

import "encoding/json"

// ProtocolVersion is the version this client asks for.
const ProtocolVersion = 1

// ---- initialize ----

type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

type FsCapability struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

// ClientCapabilities tells the agent what it may ask us to do. Advertising
// `terminal` is what makes an agent hand its shell commands to wash rather
// than running them itself (§8) — so this flag is the difference between
// the transcript showing a link to a real tab and showing inline output.
type ClientCapabilities struct {
	Fs       FsCapability `json:"fs"`
	Terminal bool         `json:"terminal"`
}

type InitializeRequest struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         Implementation     `json:"clientInfo"`
}

type PromptCapabilities struct {
	Image           bool `json:"image,omitempty"`
	Audio           bool `json:"audio,omitempty"`
	EmbeddedContext bool `json:"embeddedContext,omitempty"`
}

type AgentCapabilities struct {
	LoadSession        bool               `json:"loadSession,omitempty"`
	PromptCapabilities PromptCapabilities `json:"promptCapabilities,omitempty"`
}

type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type InitializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AgentInfo         Implementation    `json:"agentInfo"`
	AuthMethods       []AuthMethod      `json:"authMethods,omitempty"`
}

// ---- sessions ----

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type McpServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Env     []EnvVar `json:"env,omitempty"`
}

// NewSessionRequest.Cwd MUST be absolute — the spec is explicit, and an
// adapter that gets a relative path fails in a way that reads like a
// missing directory rather than a protocol error.
type NewSessionRequest struct {
	Cwd        string      `json:"cwd"`
	McpServers []McpServer `json:"mcpServers"`
}

type NewSessionResponse struct {
	SessionID string `json:"sessionId"`
}

type LoadSessionRequest struct {
	SessionID             string      `json:"sessionId"`
	Cwd                   string      `json:"cwd"`
	McpServers            []McpServer `json:"mcpServers"`
	AdditionalDirectories []string    `json:"additionalDirectories,omitempty"`
}

// ContentBlock is MCP-shaped: {"type":"text","text":"…"}.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func Text(s string) ContentBlock { return ContentBlock{Type: "text", Text: s} }

type PromptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// Stop reasons a prompt turn can end with.
const (
	StopEndTurn   = "end_turn"
	StopMaxTokens = "max_tokens"
	StopRefusal   = "refusal"
	StopCancelled = "cancelled"
)

type PromptResponse struct {
	StopReason string `json:"stopReason"`
}

type CancelNotification struct {
	SessionID string `json:"sessionId"`
}

// ---- session/update ----

// Tool-call kinds and statuses, as the agent classifies its own work. The
// kind is what lets the transcript pick an icon and the policy reason
// about a call without parsing a shell string.
const (
	ToolKindRead    = "read"
	ToolKindEdit    = "edit"
	ToolKindDelete  = "delete"
	ToolKindMove    = "move"
	ToolKindSearch  = "search"
	ToolKindFetch   = "fetch"
	ToolKindExecute = "execute"
	ToolKindThink   = "think"
	ToolKindOther   = "other"

	ToolStatusPending    = "pending"
	ToolStatusInProgress = "in_progress"
	ToolStatusCompleted  = "completed"
	ToolStatusFailed     = "failed"
)

// sessionUpdate discriminator values.
const (
	UpdateAgentMessageChunk = "agent_message_chunk"
	UpdateAgentThoughtChunk = "agent_thought_chunk"
	UpdateUserMessageChunk  = "user_message_chunk"
	UpdateToolCall          = "tool_call"
	UpdateToolCallUpdate    = "tool_call_update"
	UpdatePlan              = "plan"
)

// ToolCall is both the `tool_call` update and (partially populated) the
// subject of a permission request.
type ToolCall struct {
	ToolCallID string          `json:"toolCallId,omitempty"`
	Title      string          `json:"title,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Status     string          `json:"status,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
}

// SessionUpdate is decoded leniently: the discriminator plus the fields
// wash actually renders, with Raw kept so a variant this build does not
// know about can still be logged rather than lost. A protocol under active
// development will grow variants faster than we consume them.
type SessionUpdate struct {
	SessionUpdate string       `json:"sessionUpdate"`
	Content       ContentBlock `json:"content,omitempty"`
	ToolCall
	Raw json.RawMessage `json:"-"`
}

type SessionNotification struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

// ---- session/request_permission ----

// Permission option kinds. `allow_always` / `reject_always` are what make
// "Always allow <rule>" a protocol-level affordance rather than something
// wash infers — the agent proposes the durable option, we decide whether
// to offer it.
const (
	OptionAllowOnce    = "allow_once"
	OptionAllowAlways  = "allow_always"
	OptionRejectOnce   = "reject_once"
	OptionRejectAlways = "reject_always"
)

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type RequestPermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCall           `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// Outcome discriminator values.
const (
	OutcomeSelected  = "selected"
	OutcomeCancelled = "cancelled"
)

type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type RequestPermissionResponse struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// Selected is the answer for "the human picked this option".
func Selected(optionID string) RequestPermissionResponse {
	return RequestPermissionResponse{Outcome: PermissionOutcome{Outcome: OutcomeSelected, OptionID: optionID}}
}

// Cancelled is the answer for "nobody is going to pick" — no desktop
// attached, the question expired, the turn was cancelled. It is the ACP
// spelling of the queue's `defer`: it hands the decision back to the agent
// rather than inventing one.
func Cancelled() RequestPermissionResponse {
	return RequestPermissionResponse{Outcome: PermissionOutcome{Outcome: OutcomeCancelled}}
}

// ---- fs ----

type ReadTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      *int   `json:"line,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

type ReadTextFileResponse struct {
	Content string `json:"content"`
}

type WriteTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

// ---- terminal ----

type CreateTerminalRequest struct {
	SessionID       string   `json:"sessionId"`
	Command         string   `json:"command"`
	Args            []string `json:"args,omitempty"`
	Env             []EnvVar `json:"env,omitempty"`
	Cwd             string   `json:"cwd,omitempty"`
	OutputByteLimit *int     `json:"outputByteLimit,omitempty"`
}

type CreateTerminalResponse struct {
	TerminalID string `json:"terminalId"`
}

type TerminalRef struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

type ExitStatus struct {
	ExitCode *int   `json:"exitCode,omitempty"`
	Signal   string `json:"signal,omitempty"`
}

type TerminalOutputResponse struct {
	Output     string      `json:"output"`
	Truncated  bool        `json:"truncated,omitempty"`
	ExitStatus *ExitStatus `json:"exitStatus,omitempty"`
}

// Method names, one place, so a typo is a compile error at the call site
// rather than a -32601 at runtime.
const (
	MethodInitialize        = "initialize"
	MethodAuthenticate      = "authenticate"
	MethodSessionNew        = "session/new"
	MethodSessionLoad       = "session/load"
	MethodSessionPrompt     = "session/prompt"
	MethodSessionCancel     = "session/cancel"
	MethodSessionUpdate     = "session/update"
	MethodRequestPermission = "session/request_permission"
	MethodReadTextFile      = "fs/read_text_file"
	MethodWriteTextFile     = "fs/write_text_file"
	MethodTerminalCreate    = "terminal/create"
	MethodTerminalOutput    = "terminal/output"
	MethodTerminalWait      = "terminal/wait_for_exit"
	MethodTerminalKill      = "terminal/kill"
	MethodTerminalRelease   = "terminal/release"
)
