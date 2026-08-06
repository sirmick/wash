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

import (
	"bytes"
	"encoding/json"
	"strings"
)

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

// SessionMode is one approval/sandbox preset the agent offers. Codex ships
// read-only / agent / agent-full-access; the last is the honest place for
// "stop asking me", because it is the AGENT's own setting rather than a
// bypass bolted onto wash's rules.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SessionModes struct {
	AvailableModes []SessionMode `json:"availableModes,omitempty"`
	CurrentModeID  string        `json:"currentModeId,omitempty"`
}

type SessionModel struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SessionModels struct {
	AvailableModels []SessionModel `json:"availableModels,omitempty"`
	CurrentModelID  string         `json:"currentModelId,omitempty"`
}

type NewSessionResponse struct {
	SessionID string `json:"sessionId"`
	// ConfigOptions is the generic settings block (model, reasoning
	// effort, plan mode, …). Observed on codex-acp 1.1.9.
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
	// Modes and Models are what the agent will let you change mid-session.
	// Observed on codex-acp 1.1.9; absent from adapters that offer neither,
	// which is why nothing here is required.
	Modes  SessionModes  `json:"modes,omitempty"`
	Models SessionModels `json:"models,omitempty"`
}

// ConfigOption is a per-session setting the agent exposes generically:
// model, reasoning effort, collaboration/plan mode, fast mode. One shape
// covers all of them and whatever an adapter adds later, which is why
// this is rendered by a single control rather than four bespoke ones.
type ConfigOption struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Description  string              `json:"description,omitempty"`
	Category     string              `json:"category,omitempty"`
	Type         string              `json:"type,omitempty"`
	CurrentValue string              `json:"currentValue,omitempty"`
	Options      []ConfigOptionValue `json:"options,omitempty"`
}

type ConfigOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SetConfigOptionRequest struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

type SetConfigOptionResponse struct {
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
}

// AvailableCommand is one slash command the agent offers.
type AvailableCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Input       *struct {
		Hint string `json:"hint,omitempty"`
	} `json:"input,omitempty"`
}

// ---- elicitation ----
//
// The agent asking YOU a structured question, distinct from a permission
// request: "which of these?", not "may I?". Answering needs a form, so
// wash's minimum honest behaviour is to show the question and let the
// human decline — never to answer on their behalf.

type ElicitRequest struct {
	SessionID string          `json:"sessionId,omitempty"`
	Message   string          `json:"message"`
	Mode      string          `json:"mode,omitempty"`
	Schema    json.RawMessage `json:"requestedSchema,omitempty"`
}

const (
	ElicitAccept  = "accept"
	ElicitDecline = "decline"
	ElicitCancel  = "cancel"
)

type ElicitResponse struct {
	Action  string          `json:"action"`
	Content json.RawMessage `json:"content,omitempty"`
}

type SetModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

type LoadSessionRequest struct {
	SessionID             string      `json:"sessionId"`
	Cwd                   string      `json:"cwd"`
	McpServers            []McpServer `json:"mcpServers"`
	AdditionalDirectories []string    `json:"additionalDirectories,omitempty"`
}

// ContentBlock is MCP-shaped: {"type":"text","text":"…"}, or an image
// block carrying base64 bytes and their mime type. Both adapters
// advertise promptCapabilities.image, so images travel in BOTH
// directions — an agent can show you one, and you can paste a screenshot
// into the composer.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Data is base64 for type=="image"; MimeType names it.
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	// Nested is the wrapper form a tool_call uses: its content items are
	// ToolCallContent, which carry the real block under `content` rather
	// than being one themselves. Accepting both shapes is why an image
	// produced by a TOOL (a screenshot, a chart) is not silently lost.
	Nested *ContentBlock `json:"content,omitempty"`
}

// Image builds an image block for a prompt.
func Image(mime, b64 string) ContentBlock {
	return ContentBlock{Type: "image", Data: b64, MimeType: mime}
}

// Images returns the image blocks in this content, unwrapping the
// tool_call nesting on the way.
func (c Content) Images() []ContentBlock {
	var out []ContentBlock
	for _, b := range c {
		if b.Type == "image" && b.Data != "" {
			out = append(out, b)
		}
		if b.Nested != nil && b.Nested.Type == "image" && b.Nested.Data != "" {
			out = append(out, *b.Nested)
		}
	}
	return out
}

// Kinds lists the block types present, for the debug log — "an image
// never appeared" and "no image was ever sent" look identical without it.
func (c Content) Kinds() []string {
	out := make([]string, 0, len(c))
	for _, b := range c {
		k := b.Type
		if b.Nested != nil {
			k += "/" + b.Nested.Type
		}
		out = append(out, k)
	}
	return out
}

func Text(s string) ContentBlock { return ContentBlock{Type: "text", Text: s} }

// Content is one-or-many content blocks.
//
// The same `content` key is a single block on an agent_message_chunk and an
// ARRAY on a tool_call — observed 2026-08-04 against claude-agent-acp
// 0.64.2, where decoding it as a single block dropped every tool_call
// notification on the floor. Accepting both is not defensive coding; it is
// the shape.
type Content []ContentBlock

func (c *Content) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '[' {
		var many []ContentBlock
		if err := json.Unmarshal(b, &many); err != nil {
			return err
		}
		*c = many
		return nil
	}
	var one ContentBlock
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*c = Content{one}
	return nil
}

// String joins the blocks' text, which is what a transcript line wants.
func (c Content) String() string {
	var sb strings.Builder
	for _, b := range c {
		sb.WriteString(b.Text)
		if b.Nested != nil {
			sb.WriteString(b.Nested.Text)
		}
	}
	return sb.String()
}

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

	// Observed on the wire but deliberately not consumed (2026-08-04,
	// claude-agent-acp 0.64.2 and codex-acp 1.1.9). Named so that seeing
	// one is a decision rather than a surprise.
	UpdateUsage             = "usage_update"
	UpdateSessionInfo       = "session_info_update"
	UpdateCurrentMode       = "current_mode_update"
	UpdateConfigOption      = "config_option_update"
	UpdateAvailableCommands = "available_commands_update"
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
	SessionUpdate string  `json:"sessionUpdate"`
	Content       Content `json:"content,omitempty"`
	// Used / Size ride usage_update: context tokens consumed out of the
	// window. Observed 2026-08-05 on codex-acp as
	// {"sessionUpdate":"usage_update","used":14689,"size":258400}.
	Used int64 `json:"used,omitempty"`
	Size int64 `json:"size,omitempty"`
	// AvailableCommands rides available_commands_update; ConfigOptions
	// rides config_option_update.
	AvailableCommands []AvailableCommand `json:"availableCommands,omitempty"`
	ConfigOptions     []ConfigOption     `json:"configOptions,omitempty"`
	// ModeID rides current_mode_update when the agent changes mode on its
	// own (a slash command, its own policy) — so the UI follows the wire
	// rather than assuming its last set_mode stuck.
	ModeID string `json:"currentModeId,omitempty"`
	// Title is the agent's own name for the session on
	// session_info_update — and the tool's label on tool_call, since both
	// arrive under the same key. Which one it means is the discriminator's
	// business, not this struct's.
	ToolCall
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes what it can and never fails.
//
// The strict version dropped a whole notification when one field had an
// unexpected shape, which is data loss in a transcript and — worse —
// invisible, because the only evidence was a log line. On a protocol under
// active development the right failure is a partly-populated update with
// its Raw intact, not silence.
func (u *SessionUpdate) UnmarshalJSON(b []byte) error {
	type plain SessionUpdate
	var p plain
	if err := json.Unmarshal(b, &p); err == nil {
		*u = SessionUpdate(p)
		u.Raw = append([]byte(nil), b...)
		return nil
	}
	// Field-by-field, so one bad shape costs one field.
	var loose map[string]json.RawMessage
	if err := json.Unmarshal(b, &loose); err != nil {
		return err
	}
	*u = SessionUpdate{Raw: append([]byte(nil), b...)}
	_ = json.Unmarshal(loose["sessionUpdate"], &u.SessionUpdate)
	_ = json.Unmarshal(loose["content"], &u.Content)
	_ = json.Unmarshal(b, &u.ToolCall)
	return nil
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
	MethodSessionSetMode    = "session/set_mode"
	MethodSessionSetConfig  = "session/set_config_option"
	MethodElicitationCreate = "elicitation/create"
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
