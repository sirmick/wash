package wire

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// App event channel (channel 1) vocabulary, WIRE.md §9. Encoding:
// CBOR. Tag names match the spec exactly. Type names are prefixed
// Evt to disambiguate from shell-channel JSON types (§8).
const (
	// Router → app.
	TEvtWindowMapped         = "window.mapped"
	TEvtWindowFocus          = "window.focus"
	TEvtWindowUnfocus        = "window.unfocus"
	TEvtWindowResize         = "window.resize"
	TEvtWindowState          = "window.state"
	TEvtWindowCloseRequested = "window.close_requested"
	TEvtShutdown             = "shutdown"

	// App → router.
	TEvtWindowSetTitle     = "window.set_title"
	TEvtWindowConfirmClose = "window.confirm_close"
	TEvtSpawnRequest       = "spawn.request"
	TEvtSpawnOk            = "spawn.ok"
	TEvtSpawnErr           = "spawn.err"
	TEvtNotify             = "notify"

	// EvtPrepareSpawn / .ok / .err — external-spawner protocol used
	// by wash-priv. The app asks the router to mint a pending-attach
	// record (instance_id + attach_token) for a given app_id; the
	// app then fork+exec's the registered binary with sudo, wrapping
	// it in env vars that include the token. The dial-back is matched
	// by token, not pid (sudo's exec semantics make pid unreliable).
	TEvtPrepareSpawn    = "prepare_spawn"
	TEvtPrepareSpawnOk  = "prepare_spawn.ok"
	TEvtPrepareSpawnErr = "prepare_spawn.err"

	// Both directions.
	TEvtAppMsg = "app_msg"

	// App → router. Cross-app messaging: deliver data as an EvtAppMsg
	// to a *different* instance. Recipient is addressed by either
	// instance_id (any app) or app_id (singleton only — the router
	// resolves the sentinel to the running instance, spawning the
	// singleton on demand if none exists).
	TEvtAppMsgSendTo = "app_msg.send.to"

	// Clipboard — router holds a single in-memory entry. Apps set,
	// fetch, and observe changes.
	TEvtClipboardSet     = "clipboard.set"
	TEvtClipboardGet     = "clipboard.get"
	TEvtClipboardData    = "clipboard.data"
	TEvtClipboardChanged = "clipboard.changed"

	// App state persistence — apps can save an opaque per-instance
	// blob router-side. The shell delivers it as a wash:state event
	// on every (re)mount, restoring FE state across browser refresh
	// and multi-tab viewing.
	TEvtAppStateSet = "app_state.set"
)

// EvtWindowMapped: router → app, "the window is now visible".
type EvtWindowMapped struct {
	T   string `cbor:"t"`
	Win uint32 `cbor:"win"`
}

func NewEvtWindowMapped(win uint32) EvtWindowMapped {
	return EvtWindowMapped{T: TEvtWindowMapped, Win: win}
}

// EvtWindowFocus: router → app, focus gained.
type EvtWindowFocus struct {
	T   string `cbor:"t"`
	Win uint32 `cbor:"win"`
}

func NewEvtWindowFocus(win uint32) EvtWindowFocus {
	return EvtWindowFocus{T: TEvtWindowFocus, Win: win}
}

// EvtWindowUnfocus: router → app, focus lost.
type EvtWindowUnfocus struct {
	T   string `cbor:"t"`
	Win uint32 `cbor:"win"`
}

func NewEvtWindowUnfocus(win uint32) EvtWindowUnfocus {
	return EvtWindowUnfocus{T: TEvtWindowUnfocus, Win: win}
}

// EvtWindowResize: router → app, the window's pixel size changed
// (typically at drag-resize end). v0.1 emits this once per commit;
// live-resize is deferred.
type EvtWindowResize struct {
	T   string `cbor:"t"`
	Win uint32 `cbor:"win"`
	W   uint32 `cbor:"w"`
	H   uint32 `cbor:"h"`
}

func NewEvtWindowResize(win, w, h uint32) EvtWindowResize {
	return EvtWindowResize{T: TEvtWindowResize, Win: win, W: w, H: h}
}

// EvtWindowState: router → app, the user changed min/max/restore.
// State is one of WindowStateNormal / WindowStateMinimized /
// WindowStateMaximized (string-equal to shared constants on the wire).
type EvtWindowState struct {
	T     string `cbor:"t"`
	Win   uint32 `cbor:"win"`
	State string `cbor:"state"`
}

func NewEvtWindowState(win uint32, state string) EvtWindowState {
	return EvtWindowState{T: TEvtWindowState, Win: win, State: state}
}

// EvtWindowCloseRequested: router → app, X WM_DELETE analogue. App
// MUST reply with EvtWindowConfirmClose within grace (§10).
type EvtWindowCloseRequested struct {
	T   string `cbor:"t"`
	Win uint32 `cbor:"win"`
}

func NewEvtWindowCloseRequested(win uint32) EvtWindowCloseRequested {
	return EvtWindowCloseRequested{T: TEvtWindowCloseRequested, Win: win}
}

// EvtShutdown: router → app, "I'm going away, close cleanly".
type EvtShutdown struct {
	T string `cbor:"t"`
}

func NewEvtShutdown() EvtShutdown { return EvtShutdown{T: TEvtShutdown} }

// EvtWindowSetTitle: app → router, request titlebar text change.
type EvtWindowSetTitle struct {
	T     string `cbor:"t"`
	Win   uint32 `cbor:"win"`
	Title string `cbor:"title"`
}

func NewEvtWindowSetTitle(win uint32, title string) EvtWindowSetTitle {
	return EvtWindowSetTitle{T: TEvtWindowSetTitle, Win: win, Title: title}
}

// EvtWindowConfirmClose: app's answer to EvtWindowCloseRequested.
// Allow=true tears down the window; allow=false vetoes it.
type EvtWindowConfirmClose struct {
	T     string `cbor:"t"`
	Win   uint32 `cbor:"win"`
	Allow bool   `cbor:"allow"`
}

func NewEvtWindowConfirmClose(win uint32, allow bool) EvtWindowConfirmClose {
	return EvtWindowConfirmClose{T: TEvtWindowConfirmClose, Win: win, Allow: allow}
}

// EvtSpawnRequest: app → router, "launch app_id". Gated by the
// "spawn" capability on the requester's manifest.
type EvtSpawnRequest struct {
	T     string `cbor:"t"`
	AppID string `cbor:"app_id"`
}

func NewEvtSpawnRequest(appID string) EvtSpawnRequest {
	return EvtSpawnRequest{T: TEvtSpawnRequest, AppID: appID}
}

// EvtSpawnOk: router → app, spawn succeeded.
type EvtSpawnOk struct {
	T          string `cbor:"t"`
	AppID      string `cbor:"app_id"`
	InstanceID string `cbor:"instance_id"`
}

func NewEvtSpawnOk(appID, instanceID string) EvtSpawnOk {
	return EvtSpawnOk{T: TEvtSpawnOk, AppID: appID, InstanceID: instanceID}
}

// EvtSpawnErr: router → app, spawn refused. Capability denial uses
// code "forbidden"; unknown id "not_found"; ABI mismatch
// "incompatible_protocol".
type EvtSpawnErr struct {
	T     string `cbor:"t"`
	AppID string `cbor:"app_id"`
	Code  string `cbor:"code"`
	Msg   string `cbor:"msg"`
}

func NewEvtSpawnErr(appID, code, msg string) EvtSpawnErr {
	return EvtSpawnErr{T: TEvtSpawnErr, AppID: appID, Code: code, Msg: msg}
}

// EvtPrepareSpawn: app → router. Request the router prepare for a
// dial-back from a process the *caller* will fork+exec. The router
// allocates an instance id and an opaque attach token; the caller
// passes both via env when spawning the binary (under sudo, in
// wash-priv's case). Token-matched attaches do not collide with
// pid-matched ones — sudo's fork/exec semantics make pid unreliable.
//
// Gated by the "prepare_spawn" capability — wash-priv has it.
type EvtPrepareSpawn struct {
	T     string `cbor:"t"`
	ReqID uint64 `cbor:"req_id"`
	AppID string `cbor:"app_id"`
}

func NewEvtPrepareSpawn(reqID uint64, appID string) EvtPrepareSpawn {
	return EvtPrepareSpawn{T: TEvtPrepareSpawn, ReqID: reqID, AppID: appID}
}

// EvtPrepareSpawnOk: router → app. Carries the minted instance id,
// the attach token, and the registered binary path. The caller
// should exec the binary (or a wrapper such as sudo that ultimately
// exec's it) with env including WASH_DISPLAY, WASH_PROTO,
// WASH_APP_ID, WASH_INSTANCE_ID, and WASH_ATTACH_TOKEN.
type EvtPrepareSpawnOk struct {
	T           string `cbor:"t"`
	ReqID       uint64 `cbor:"req_id"`
	InstanceID  string `cbor:"instance_id"`
	AttachToken string `cbor:"attach_token"`
	Binary      string `cbor:"binary"`
}

func NewEvtPrepareSpawnOk(reqID uint64, instanceID, token, binary string) EvtPrepareSpawnOk {
	return EvtPrepareSpawnOk{T: TEvtPrepareSpawnOk, ReqID: reqID, InstanceID: instanceID, AttachToken: token, Binary: binary}
}

// EvtPrepareSpawnErr: router → app. Codes: forbidden (cap missing),
// not_found (unknown app_id), bad_request (disabled / reserved-id
// refusal in registry), internal.
type EvtPrepareSpawnErr struct {
	T     string `cbor:"t"`
	ReqID uint64 `cbor:"req_id"`
	Code  string `cbor:"code"`
	Msg   string `cbor:"msg"`
}

func NewEvtPrepareSpawnErr(reqID uint64, code, msg string) EvtPrepareSpawnErr {
	return EvtPrepareSpawnErr{T: TEvtPrepareSpawnErr, ReqID: reqID, Code: code, Msg: msg}
}

// Notification levels.
const (
	NotifyLevelInfo  = "info"
	NotifyLevelWarn  = "warn"
	NotifyLevelError = "error"
)

// EvtNotify is app → router. The router relays as ShellNotify so
// the shell can render a toast. v0.1 has no capability gate; spamming
// the chrome with notifications is a future concern.
type EvtNotify struct {
	T     string `cbor:"t"`
	Title string `cbor:"title"`
	Body  string `cbor:"body,omitempty"`
	Level string `cbor:"level,omitempty"`
}

func NewEvtNotify(title, body, level string) EvtNotify {
	return EvtNotify{T: TEvtNotify, Title: title, Body: body, Level: level}
}

// EvtClipboardSet writes new content to the router-held clipboard.
// The router broadcasts EvtClipboardChanged to every connected app
// (except the sender) so they can react.
type EvtClipboardSet struct {
	T    string `cbor:"t"`
	Mime string `cbor:"mime"`
	Data []byte `cbor:"data"`
}

func NewEvtClipboardSet(mime string, data []byte) EvtClipboardSet {
	return EvtClipboardSet{T: TEvtClipboardSet, Mime: mime, Data: data}
}

// EvtClipboardGet requests the current clipboard contents. The router
// replies with EvtClipboardData. req_id correlates response with caller.
type EvtClipboardGet struct {
	T     string `cbor:"t"`
	ReqID uint64 `cbor:"req_id"`
}

func NewEvtClipboardGet(reqID uint64) EvtClipboardGet {
	return EvtClipboardGet{T: TEvtClipboardGet, ReqID: reqID}
}

// EvtClipboardData is the router → app response containing the
// clipboard's current mime and bytes.
type EvtClipboardData struct {
	T     string `cbor:"t"`
	ReqID uint64 `cbor:"req_id"`
	Mime  string `cbor:"mime"`
	Data  []byte `cbor:"data"`
}

func NewEvtClipboardData(reqID uint64, mime string, data []byte) EvtClipboardData {
	return EvtClipboardData{T: TEvtClipboardData, ReqID: reqID, Mime: mime, Data: data}
}

// EvtClipboardChanged is broadcast by the router to every app
// (excluding the setter) when the clipboard changes. Apps re-get
// content if interested.
type EvtClipboardChanged struct {
	T    string `cbor:"t"`
	Mime string `cbor:"mime"`
}

func NewEvtClipboardChanged(mime string) EvtClipboardChanged {
	return EvtClipboardChanged{T: TEvtClipboardChanged, Mime: mime}
}

// Sender identifies the originating app instance of a relayed
// app_msg. The router populates it when delivering an EvtAppMsg
// that arrived via app_msg.send.to (cross-app BE→BE). For ordinary
// FE→BE deliveries (a window's own FE messaging its own BE) the
// field is nil — there is no other app to name.
//
// Receivers MUST treat From as authoritative; the value comes from
// the router's view of the sending socket, not from anything the
// sender wrote into the payload. This is what makes the field
// unforgeable and the basis for capability prompts in apps like
// wash-priv.
type Sender struct {
	AppID      string `cbor:"app_id,omitempty"`
	InstanceID string `cbor:"instance_id,omitempty"`
}

// EvtAppMsg is the FE↔BE app-private pipe. Data is intentionally
// opaque; the router never inspects it. From is set only on
// cross-app deliveries — see Sender.
type EvtAppMsg struct {
	T    string  `cbor:"t"`
	Win  uint32  `cbor:"win"`
	Data any     `cbor:"data"`
	From *Sender `cbor:"from,omitempty"`
}

func NewEvtAppMsg(win uint32, data any) EvtAppMsg {
	return EvtAppMsg{T: TEvtAppMsg, Win: win, Data: data}
}

// NewEvtAppMsgFrom builds an EvtAppMsg whose From field identifies
// the originating instance. Only the router constructs these — it
// is the relay path for cross-app app_msg.send.to.
func NewEvtAppMsgFrom(win uint32, data any, from Sender) EvtAppMsg {
	return EvtAppMsg{T: TEvtAppMsg, Win: win, Data: data, From: &from}
}

// Recipient is the address used by EvtAppMsgSendTo and the shell's
// ShellAppMsgSendTo. Exactly one of AppID / InstanceID should be
// set: AppID resolves via the singleton table (failing if the
// named app isn't singleton-instancing); InstanceID is a direct
// reference to an already-running app.
type Recipient struct {
	AppID      string `cbor:"app_id,omitempty" json:"app_id,omitempty"`
	InstanceID string `cbor:"instance_id,omitempty" json:"instance_id,omitempty"`
}

// EvtAppMsgSendTo: an app asking the router to relay data as an
// EvtAppMsg to another instance. The router is a message broker for
// this one path — no app capability gate yet (v0.1 single-user
// trust model).
type EvtAppMsgSendTo struct {
	T         string    `cbor:"t"`
	Recipient Recipient `cbor:"recipient"`
	Data      any       `cbor:"data"`
}

func NewEvtAppMsgSendTo(r Recipient, data any) EvtAppMsgSendTo {
	return EvtAppMsgSendTo{T: TEvtAppMsgSendTo, Recipient: r, Data: data}
}

// EvtAppStateSet — app → router — persist `State` as this app's
// FE-state blob (router stores keyed by instance_id). The blob is
// JSON bytes; the router never inspects them. Used by SDK.SaveState
// for apps whose state lives BE-side or is computed in Go. FE-only
// apps can use the shell's window.wash.saveState instead.
type EvtAppStateSet struct {
	T     string `cbor:"t"`
	State []byte `cbor:"state"`
}

func NewEvtAppStateSet(state []byte) EvtAppStateSet {
	return EvtAppStateSet{T: TEvtAppStateSet, State: state}
}

// PeekEvtType returns the t field of a CBOR map without otherwise
// decoding it.
func PeekEvtType(data []byte) (string, error) {
	var probe struct {
		T string `cbor:"t"`
	}
	if err := cbor.Unmarshal(data, &probe); err != nil {
		return "", err
	}
	return probe.T, nil
}

// DecodeEvt parses a single CBOR event-channel message into the
// concrete type matching its t field.
func DecodeEvt(data []byte) (any, error) {
	t, err := PeekEvtType(data)
	if err != nil {
		return nil, fmt.Errorf("evt decode: %w", err)
	}
	switch t {
	case TEvtWindowMapped:
		var m EvtWindowMapped
		return m, cbor.Unmarshal(data, &m)
	case TEvtWindowFocus:
		var m EvtWindowFocus
		return m, cbor.Unmarshal(data, &m)
	case TEvtWindowUnfocus:
		var m EvtWindowUnfocus
		return m, cbor.Unmarshal(data, &m)
	case TEvtWindowResize:
		var m EvtWindowResize
		return m, cbor.Unmarshal(data, &m)
	case TEvtWindowState:
		var m EvtWindowState
		return m, cbor.Unmarshal(data, &m)
	case TEvtWindowCloseRequested:
		var m EvtWindowCloseRequested
		return m, cbor.Unmarshal(data, &m)
	case TEvtShutdown:
		var m EvtShutdown
		return m, cbor.Unmarshal(data, &m)
	case TEvtWindowSetTitle:
		var m EvtWindowSetTitle
		return m, cbor.Unmarshal(data, &m)
	case TEvtWindowConfirmClose:
		var m EvtWindowConfirmClose
		return m, cbor.Unmarshal(data, &m)
	case TEvtSpawnRequest:
		var m EvtSpawnRequest
		return m, cbor.Unmarshal(data, &m)
	case TEvtSpawnOk:
		var m EvtSpawnOk
		return m, cbor.Unmarshal(data, &m)
	case TEvtSpawnErr:
		var m EvtSpawnErr
		return m, cbor.Unmarshal(data, &m)
	case TEvtAppMsg:
		var m EvtAppMsg
		return m, cbor.Unmarshal(data, &m)
	case TEvtAppMsgSendTo:
		var m EvtAppMsgSendTo
		return m, cbor.Unmarshal(data, &m)
	case TEvtNotify:
		var m EvtNotify
		return m, cbor.Unmarshal(data, &m)
	case TEvtClipboardSet:
		var m EvtClipboardSet
		return m, cbor.Unmarshal(data, &m)
	case TEvtClipboardGet:
		var m EvtClipboardGet
		return m, cbor.Unmarshal(data, &m)
	case TEvtClipboardData:
		var m EvtClipboardData
		return m, cbor.Unmarshal(data, &m)
	case TEvtClipboardChanged:
		var m EvtClipboardChanged
		return m, cbor.Unmarshal(data, &m)
	case TEvtAppStateSet:
		var m EvtAppStateSet
		return m, cbor.Unmarshal(data, &m)
	case TEvtPrepareSpawn:
		var m EvtPrepareSpawn
		return m, cbor.Unmarshal(data, &m)
	case TEvtPrepareSpawnOk:
		var m EvtPrepareSpawnOk
		return m, cbor.Unmarshal(data, &m)
	case TEvtPrepareSpawnErr:
		var m EvtPrepareSpawnErr
		return m, cbor.Unmarshal(data, &m)
	}
	return nil, fmt.Errorf("evt decode: unknown t %q", t)
}

// EncodeEvt marshals a CBOR event-channel message. Equivalent to
// cbor.Marshal; provided for symmetry with EncodeCtrl.
func EncodeEvt(v any) ([]byte, error) {
	return cbor.Marshal(v)
}
