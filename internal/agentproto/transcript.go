package agentproto

// Transcripts: a session's conversation, as events. A frontend subscribes
// per session (transcript_subscribe), gets the history as snapshot frames,
// then each change as it happens.

// Event kinds. Deliberately fewer than ACP's update variants: the
// transcript renders messages and tool calls, and everything else
// (usage, available commands, session info) is roster or nothing.
const (
	EventMessage = "message"
	EventThought = "thought"
	// EventDecision is wash's own approval verdict on a tool call: Status is
	// allow or cancelled, Title the tool, Detail its (shortened) subject,
	// Reason why. Distinct from a message so a transcript can
	// show a guard coming off (or holding) at a glance, and so these lines
	// stay out of the conversation preview.
	EventDecision = "decision"
	EventTool     = "tool"
	// EventUser is what the human typed. ACP has a user_message_chunk
	// variant, but an agent does not echo the prompt its client just sent
	// it — so a transcript built purely from notifications shows the
	// answers with none of the questions. wash records its own side.
	EventUser = "user"
	// EventTerminal is a command the agent handed to wash to run
	// (acpterm.go). Channel carries the raw channel its pty writes to, so
	// a transcript can mount a live terminal on it — the difference
	// between watching the command and reading about it afterwards.
	EventTerminal = "terminal"
	// EventImage is one image the agent showed. Its own event rather than
	// a field on a message, so it renders in the order it arrived without
	// restructuring everything else.
	EventImage = "image"
	// EventCollaboration is a message delivered to a workspace member by
	// its team (another member, the orchestrator or the human), with its
	// provenance at the head of Text.
	EventCollaboration = "collaboration"
)

// Event is one line in a transcript.
//
// Flat and string-typed on purpose: this crosses the router to the FE, and
// structured byte fields get base64'd on the way (the CBOR pitfall).
type Event struct {
	Seq uint64 `json:"seq"`
	// Kind is one of the Event* kinds above: message | thought | tool |
	// decision | user | terminal | image | collaboration.
	Kind string `json:"kind"`
	// Text is the message body, accumulated across streamed chunks.
	Text string `json:"text,omitempty"`
	// Tool fields, set when Kind == EventTool.
	ToolID   string `json:"tool_id,omitempty"`
	ToolKind string `json:"tool_kind,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   string `json:"status,omitempty"`
	// Path is the file a tool call touched (its first ACP location, or
	// the diff's), so a host can open it. Diff is the unified diff of what
	// the call changed, rendered once here from the agent's before/after
	// pair (diff.go). Both on EventTool only.
	Path string `json:"path,omitempty"`
	Diff string `json:"diff,omitempty"`
	// Mime is set on EventImage; Text then holds the base64 bytes.
	Mime string `json:"mime,omitempty"`
	// Reason and Detail are set on EventDecision.
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Channel is set on EventTerminal: the raw channel id to render.
	Channel uint32 `json:"channel,omitempty"`
	// AtMS is wall-clock at first append, for the FE's own clock anchoring.
	AtMS int64 `json:"at_ms"`
	// Append marks a wire-only delta: Text is what was ADDED to the event
	// with this Seq since the last emit, not the whole message. Never set
	// on a stored or snapshotted event (transcript_emit.go).
	Append bool `json:"append,omitempty"`
	// TextLen is the message's byte length after this event applies, on
	// message/thought events. A consumer applying a delta checks its own
	// length + the delta against it, and asks for a replay on mismatch.
	TextLen int `json:"text_len,omitempty"`
}

// TranscriptSubscribe watches a session's transcript, and re-affirms the
// watch: a watcher agentd has not heard from for its TTL is dropped, so a
// frontend repeats this every agentclient.WatcherRefresh.
type TranscriptSubscribe struct {
	Key string `json:"key"`
	// Replay asks for the whole history again even from a watcher agentd
	// already knows, after a frontend lost its copy (a reload, a missed
	// delta).
	Replay bool `json:"replay,omitempty"`
}

// TranscriptSnapshot is a session's history, sent in bounded frames: the
// first has Reset (replace what you hold), the rest append.
type TranscriptSnapshot struct {
	Key    string  `json:"key"`
	Reset  bool    `json:"reset"`
	Events []Event `json:"events"`
}

// TranscriptEvent is one new or changed event. An event with Append set is
// a delta to the one with its Seq.
type TranscriptEvent struct {
	Key   string `json:"key"`
	Event Event  `json:"event"`
}

func init() {
	register(Spec{Kind: "transcript_subscribe", Dir: Request, Payload: TranscriptSubscribe{}, From: "any frontend showing the session",
		Reply: "transcript_snapshot, when the watcher is new or asks for replay",
		Doc:   "Watch a session's transcript, or re-affirm the watch."})
	register(Spec{Kind: "transcript_snapshot", Dir: Push, Payload: TranscriptSnapshot{}, From: "the subscribing instance", Class: Bulk, Keyed: true,
		Doc: "A session's history, in bounded frames."})
	register(Spec{Kind: "transcript_event", Dir: Push, Payload: TranscriptEvent{}, From: "every watcher of the session", Class: Bulk, Keyed: true,
		Doc: "One transcript event, new or changed; streamed text arrives as appended deltas."})
}
