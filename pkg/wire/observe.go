package wire

// Observation shapes (docs/COMMANDER.md §4). One struct is the answer to
// every form of the verb — the shell's control-channel request, an app's
// attested request about another instance, and what the commander service
// hands to inference — so a consumer reads one shape whoever asked.

// Observation source values: which of the router's holdings answered.
const (
	// ObserveSourceExport: the app answered observe.request itself (§4.5).
	ObserveSourceExport = "export"
	// ObserveSourcePtyTail: the tail of a pty channel's scrollback ring,
	// stripped of terminal control sequences and redacted (§4.2).
	ObserveSourcePtyTail = "pty-tail"
	// ObserveSourceAppState: the instance's persisted app_state blob (§4.3).
	ObserveSourceAppState = "app-state"
	// ObserveSourceNone: nothing to observe, or the app is not eligible.
	ObserveSourceNone = "none"
)

// Observation is one look at one instance.
type Observation struct {
	Source string `json:"source"`
	// Revision changes whenever the content would: the pty ring's bytes-
	// seen counter, the state blob's version, an export's own stamp. Cheap
	// change detection for a scheduler; opaque otherwise.
	Revision    string `json:"revision,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Content     string `json:"content,omitempty"`
	// Truncated says Content was cut at the front (a tail) or the end (a
	// blob or export longer than the cap).
	Truncated  bool            `json:"truncated,omitempty"`
	CapturedAt int64           `json:"captured_at"`
	Window     *ObservedWindow `json:"window,omitempty"`
}

// ObservedWindow is the window metadata a consumer shows beside the
// content: what the router knows without reading anything.
type ObservedWindow struct {
	App        string `json:"app"`
	InstanceID string `json:"instance_id"`
	WindowID   uint32 `json:"window_id,omitempty"`
	Title      string `json:"title,omitempty"`
	State      string `json:"state,omitempty"`
	Focused    bool   `json:"focused,omitempty"`
}

// ObserveMaxBytes caps what any observation carries; a request asking for
// more gets this. Exports are cut here too (§4.5).
const ObserveMaxBytes = 128 << 10

// --- router → app: ask the app for its own export (§4.5) ---

// TEvtObserveRequest asks an app whose manifest says observation=export
// for its view of itself. The app answers TEvtObserveReply within the
// router's deadline or the router falls back to the automatic sources.
const TEvtObserveRequest = "observe.request"

type EvtObserveRequest struct {
	T        string `json:"t"`
	ReqID    uint64 `json:"req_id"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

func NewEvtObserveRequest(reqID uint64, maxBytes int) EvtObserveRequest {
	return EvtObserveRequest{T: TEvtObserveRequest, ReqID: reqID, MaxBytes: maxBytes}
}

// TEvtObserveReply is the app's export. An empty Content means "nothing
// to say" and the router falls back, the same as no answer.
const TEvtObserveReply = "observe.reply"

type EvtObserveReply struct {
	T           string `json:"t"`
	ReqID       uint64 `json:"req_id"`
	ContentType string `json:"content_type,omitempty"`
	Content     string `json:"content,omitempty"`
	Revision    string `json:"revision,omitempty"`
}

func NewEvtObserveReply(reqID uint64, contentType, content, revision string) EvtObserveReply {
	return EvtObserveReply{T: TEvtObserveReply, ReqID: reqID, ContentType: contentType, Content: content, Revision: revision}
}

// --- app → router: observe another instance (attested; CapObserve) ---

// TEvtObserveGet asks the router to observe an instance. The requester is
// the attested sender; it needs CapObserve. The answer is
// TEvtObserveResult or TEvtObserveGetErr with the same req_id.
const (
	TEvtObserveGet    = "observe.get"
	TEvtObserveResult = "observe.result"
	TEvtObserveGetErr = "observe.get.err"
)

type EvtObserveGet struct {
	T          string `json:"t"`
	ReqID      uint64 `json:"req_id"`
	InstanceID string `json:"instance_id"`
	MaxBytes   int    `json:"max_bytes,omitempty"`
}

func NewEvtObserveGet(reqID uint64, instanceID string, maxBytes int) EvtObserveGet {
	return EvtObserveGet{T: TEvtObserveGet, ReqID: reqID, InstanceID: instanceID, MaxBytes: maxBytes}
}

type EvtObserveResult struct {
	T           string      `json:"t"`
	ReqID       uint64      `json:"req_id"`
	Observation Observation `json:"observation"`
}

func NewEvtObserveResult(reqID uint64, o Observation) EvtObserveResult {
	return EvtObserveResult{T: TEvtObserveResult, ReqID: reqID, Observation: o}
}

type EvtObserveGetErr struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	Code  string `json:"code"`
	Msg   string `json:"msg,omitempty"`
}

func NewEvtObserveGetErr(reqID uint64, code, msg string) EvtObserveGetErr {
	return EvtObserveGetErr{T: TEvtObserveGetErr, ReqID: reqID, Code: code, Msg: msg}
}

// --- shell ↔ router ---

const (
	// Shell → router: observe an instance of this router's.
	TShellObserve = "observe"
	// Router → shell: the observation.
	TShellObserveOK = "observe.ok"
	// Router → shell: no such instance.
	TShellObserveErr = "observe.err"
)

type ShellObserve struct {
	T          string `json:"t"`
	ReqID      uint64 `json:"req_id"`
	InstanceID string `json:"instance_id"`
	MaxBytes   int    `json:"max_bytes,omitempty"`
}

func NewShellObserve(reqID uint64, instanceID string, maxBytes int) ShellObserve {
	return ShellObserve{T: TShellObserve, ReqID: reqID, InstanceID: instanceID, MaxBytes: maxBytes}
}

type ShellObserveOK struct {
	T           string      `json:"t"`
	ReqID       uint64      `json:"req_id"`
	Observation Observation `json:"observation"`
}

func NewShellObserveOK(reqID uint64, o Observation) ShellObserveOK {
	return ShellObserveOK{T: TShellObserveOK, ReqID: reqID, Observation: o}
}

type ShellObserveErr struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	Code  string `json:"code"`
	Msg   string `json:"msg,omitempty"`
}

func NewShellObserveErr(reqID uint64, code, msg string) ShellObserveErr {
	return ShellObserveErr{T: TShellObserveErr, ReqID: reqID, Code: code, Msg: msg}
}
