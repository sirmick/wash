package agentproto

// Session messages: starting a session, talking to it, changing its
// settings, answering its questions, and ending it. A session is named by
// its roster key ("acp:<n>").

// AgentStart starts a session.
type AgentStart struct {
	// Catalog and Model choose the settings (catalogs.go): Model is a slot
	// of a curated catalog (frontier, coding, small; frontier when empty)
	// or a model id the catalog's adapter offers (empty is its default on
	// an auto catalog). Agent alone, with no catalog, is how `wash ai
	// --agent` starts: that adapter on its defaults.
	Catalog string `json:"catalog,omitempty"`
	Model   string `json:"model,omitempty"`
	Agent   string `json:"agent,omitempty"`
	// Configs are the launcher's Advanced settings, by the adapter's own
	// option ids (effort, fast mode, …), applied over the catalog's.
	Configs map[string]string `json:"configs,omitempty"`
	// Mode is the adapter session mode to start in (State.Launch's default
	// unless the launcher's Permissions row was changed for this start);
	// empty is the adapter's default. Yolo starts with host-side
	// auto-approval on.
	Mode string `json:"mode,omitempty"`
	Yolo bool   `json:"yolo,omitempty"`
	// Cwd is the folder the session works in; empty is the home folder.
	Cwd string `json:"cwd"`
	// Prompt is sent as the first turn, after the stored default prompt.
	Prompt string `json:"prompt,omitempty"`
	// Open asks agentd to open (or focus) an Agent window on the new
	// session, for a starter that is not itself that window (the manager).
	Open bool `json:"open,omitempty"`
	// Claim makes the starter the session's controller (see Roles): an
	// Agent window starting the session it will show.
	Claim bool `json:"claim,omitempty"`
	// ReqID is opaque to agentd and echoed back on agent_started, success
	// or failure. A host with ONE session per process (wash-ai) never needs
	// it — the reply can only be about the one thing it asked for. A host
	// with several (wash-edit's agent tabs) cannot tell two concurrent
	// starts apart without it, and a FAILED start carries no key at all, so
	// there would be nothing to attribute the error to.
	ReqID string `json:"req_id,omitempty"`
}

// AgentStarted answers AgentStart: the new session's key, or why it failed.
type AgentStarted struct {
	Key       string `json:"key,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	ReqID     string `json:"req_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// AgentPrompt is another turn on a live session. Sent while a turn runs,
// it is queued and sent when the turn ends.
type AgentPrompt struct {
	Key  string `json:"key"`
	Text string `json:"text,omitempty"`
	// Blocks are attachments sent with the text: a pasted image, a file
	// the composer's Attach button picked. Kept as a wash-shaped struct
	// rather than acp.ContentBlock so the app→service wire is ours to
	// validate — the router carries this from a window, and a window is
	// not trusted to name a mime type or a path.
	Blocks []PromptAttachment `json:"blocks,omitempty"`
}

// PromptAttachment is one attachment on its way to an ACP content block.
// Type is "image" or "file"; anything else is dropped.
type PromptAttachment struct {
	Type string `json:"type"`
	// Image: base64 bytes and their mime type.
	Mime string `json:"mime,omitempty"`
	Data string `json:"data,omitempty"`
	// File: an absolute path, confined against the session cwd before it
	// becomes a resource_link.
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

// AgentCancel ends the running turn; the agent answers with a cancelled
// stop.
type AgentCancel struct {
	Key string `json:"key"`
}

// AgentDetach leaves a session running with no window: its roster row
// stays and offers Reattach, and its controller window is told to close.
type AgentDetach struct {
	Key string `json:"key"`
}

// AgentReattach opens a window onto a running session.
type AgentReattach struct {
	Key string `json:"key"`
}

// AgentStop ends a session and its adapter.
type AgentStop struct {
	Key string `json:"key"`
}

// Detach tells a session's controller window that the session was
// detached elsewhere (the rail, the manager), so it closes.
type Detach struct {
	Key string `json:"key"`
}

// AgentSetYolo turns host-side auto-approval on or off for one session.
type AgentSetYolo struct {
	Key string `json:"key"`
	On  bool   `json:"on"`
}

// AgentSetMode switches the agent's approval preset (Row.Modes).
type AgentSetMode struct {
	Key  string `json:"key"`
	Mode string `json:"mode"`
}

// AgentSetConfig changes one of the agent's own settings (Row.Configs):
// model, reasoning effort, plan mode, …
type AgentSetConfig struct {
	Key   string `json:"key"`
	ID    string `json:"id"`
	Value string `json:"value"`
}

// AgentAddRoot widens which folders a session may reach beyond its cwd.
type AgentAddRoot struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

// AgentRemoveRoot narrows it again.
type AgentRemoveRoot struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

// AgentAnswer answers a question (Ask) by its id.
type AgentAnswer struct {
	ID string `json:"id"`
	// Decision is allow | deny.
	Decision string `json:"decision"`
	// Remember writes Rule (or the ask's suggested rule) to the policy, so
	// the question is not asked again.
	Remember bool   `json:"remember"`
	Rule     string `json:"rule"`
	// Scope picks the table a remembered answer is written to: "" (or
	// anything unrecognised) is the global one, "workspace" is the asking
	// member's workspace's. An unknown value must not silently widen
	// anything, so it falls back to the narrower, global behaviour.
	Scope string `json:"scope,omitempty"`
}

// AgentDefaultPrompt asks for the stored default prompt's text.
type AgentDefaultPrompt struct{}

// AgentSetDefaultPrompt stores the default prompt. Empty text is a
// deletion, not a validation failure.
type AgentSetDefaultPrompt struct {
	Text string `json:"text"`
}

// DefaultPrompt is the stored default prompt's text, answering either.
type DefaultPrompt struct {
	Text string `json:"text"`
}

func init() {
	register(Spec{Kind: "agent_start", Dir: Request, Payload: AgentStart{}, From: "a launcher (the Agents manager, an Agent window, wash-edit, wash ai --agent)", Reply: "agent_started",
		Doc: "Start a session from a catalog and model, or an adapter on its defaults."})
	register(Spec{Kind: "agent_prompt", Dir: Request, Payload: AgentPrompt{}, From: "a frontend showing the session",
		Doc: "Send a prompt, with attachments; queued while a turn runs."})
	register(Spec{Kind: "agent_cancel", Dir: Request, Payload: AgentCancel{}, From: "a frontend showing the session",
		Doc: "Stop the running turn."})
	register(Spec{Kind: "agent_detach", Dir: Request, Payload: AgentDetach{}, From: "a frontend showing the session", Reply: "detach, to the session's controller",
		Doc: "Keep the session running with no window."})
	register(Spec{Kind: "agent_reattach", Dir: Request, Payload: AgentReattach{}, From: "any frontend",
		Doc: "Open a window onto a running session."})
	register(Spec{Kind: "agent_stop", Dir: Request, Payload: AgentStop{}, From: "any frontend",
		Doc: "End a session and its adapter."})
	register(Spec{Kind: "agent_set_yolo", Dir: Request, Payload: AgentSetYolo{}, From: "a frontend showing the session",
		Doc: "Turn host-side auto-approval on or off for the session."})
	register(Spec{Kind: "agent_set_mode", Dir: Request, Payload: AgentSetMode{}, From: "a frontend showing the session",
		Doc: "Switch the agent's approval preset. Refused for a reviewer."})
	register(Spec{Kind: "agent_set_config", Dir: Request, Payload: AgentSetConfig{}, From: "a frontend showing the session",
		Doc: "Change an agent setting (model, effort, …). A reviewer's mode cannot change."})
	register(Spec{Kind: "agent_add_root", Dir: Request, Payload: AgentAddRoot{}, From: "any frontend",
		Doc: "Let the session reach another folder."})
	register(Spec{Kind: "agent_remove_root", Dir: Request, Payload: AgentRemoveRoot{}, From: "any frontend",
		Doc: "Take a folder back."})
	register(Spec{Kind: "agent_answer", Dir: Request, Payload: AgentAnswer{}, From: "any frontend (answering anywhere resolves everywhere)",
		Doc: "Answer a question, optionally remembering the rule."})
	register(Spec{Kind: "agent_default_prompt", Dir: Request, Payload: AgentDefaultPrompt{}, From: "any frontend", Reply: "default_prompt",
		Doc: "Read the stored default prompt."})
	register(Spec{Kind: "agent_set_default_prompt", Dir: Request, Payload: AgentSetDefaultPrompt{}, From: "any frontend", Reply: "default_prompt",
		Doc: "Store (or, with empty text, delete) the default prompt."})

	register(Spec{Kind: "agent_started", Dir: Push, Payload: AgentStarted{}, From: "the starter",
		Doc: "A started session's key, or the error that stopped it."})
	register(Spec{Kind: "detach", Dir: Push, Payload: Detach{}, From: "the session's controller", Keyed: true,
		Doc: "The session was detached elsewhere; its window closes."})
	register(Spec{Kind: "default_prompt", Dir: Push, Payload: DefaultPrompt{}, From: "the asker",
		Doc: "The stored default prompt."})
}
