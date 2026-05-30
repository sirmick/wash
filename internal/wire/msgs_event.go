package wire

import (
	"encoding/json"
	"fmt"
)

// App event channel (channel 1) vocabulary, WIRE.md §9. Encoding:
// JSON (same discipline as channel 0). Type names are prefixed Evt
// to disambiguate from shell-channel JSON types (§8).
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
	// TEvtWindowGeometry: app → router, the app's own content changed
	// size (e.g. a wash-display guest surface resized). Distinct from
	// TEvtWindowResize, which is router → app (a command to resize). The
	// router updates the window's geometry so the shell frame tracks the
	// new content size. Only honored for windows the sender owns.
	TEvtWindowGeometry = "window.geometry"

	// Multi-window: one app instance owning more than one window
	// (wash-display maps each Wayland/X11 toplevel to a window). The
	// shape mirrors the spawn protocol — app → router window.create
	// with a req_id, router → app window.created{win} or
	// window.create.err. window.destroy is fire-and-forget. Gated by
	// the "windows" capability. See docs/DISPLAY.md §4. Single-window
	// apps never send these; the router still mints their one window
	// at identity.ack.
	TEvtWindowCreate    = "window.create"
	TEvtWindowCreated   = "window.created"
	TEvtWindowCreateErr = "window.create.err"
	TEvtWindowDestroy   = "window.destroy"

	// EvtEnvPublish: app → router. A privileged app (wash-display)
	// publishes WASH_*-namespaced env hints that the router then merges
	// into the environment of every app it subsequently spawns (see
	// Router.spawnEnv). Used to propagate the compositor's DISPLAY /
	// WAYLAND_DISPLAY socket names so wash-term's shell can launch X /
	// Wayland clients. Gated by the "env-publish" capability; the router
	// also allowlists keys to ^WASH_[A-Z0-9_]+$. See docs/DISPLAY_ENV.md.
	TEvtEnvPublish    = "env.publish"
	TEvtEnvPublishErr = "env.publish.err"
	// EvtSpawnRequest / Ok / Err — unified spawn protocol.
	//
	// Normal spawn: app requests "launch app_id" and the router does
	// fork+exec itself. Requires the "spawn" capability.
	//
	// Prepare spawn: the caller will fork+exec the binary itself
	// (e.g. wash-priv launching under sudo). The router replies with
	// an attach token + binary path; the caller passes both via env.
	// Requires the "prepare_spawn" capability and Prepare=true on
	// the request. The dial-back is matched by token, not pid, since
	// sudo's exec semantics make the dialing pid unreliable.
	//
	// ReqID correlates requests with replies — required for Prepare
	// (a single caller may have multiple in flight), optional for
	// normal spawn (which is fire-and-forget plus a single-shot ack).
	TEvtSpawnRequest = "spawn.request"
	TEvtSpawnOk      = "spawn.ok"
	TEvtSpawnErr     = "spawn.err"
	TEvtNotify       = "notify"

	// Both directions. Carries app-private bytes between an app's BE
	// and either its own FE half or another instance's BE.
	//
	// Routing is determined by the optional From / To fields:
	//   - own-FE → own-BE: neither set
	//   - cross-app source: To set by sender
	//   - cross-app delivery: From set by router (router-attested)
	TEvtAppMsg = "app_msg"

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

	// Runtime stats heartbeat — app → router. SDK emits one every
	// few seconds with Go-runtime self-report (NumGoroutine, MemStats
	// subset). Router collects per-instance and surfaces via the
	// com.wash.router synthetic-peer bus for the About panel.
	TEvtRuntimeStats = "runtime.stats"

	// Ingress — app → router RPC that publishes an HTTP/WS backend
	// (a unix socket or loopback addr the app's BE is serving) and
	// gets back an opaque public path under the shell origin. The
	// router reverse-proxies /app/<token>/* to that backend, with
	// WebSocket-upgrade passthrough, so an embedded web app
	// (code-server, jupyter, …) can render in an <iframe> without
	// the browser ever reaching the backend port directly. The
	// token is the capability: it is minted router-side, handed to
	// the FE over the trusted app_msg channel, and embedded in the
	// iframe src. req_id correlates publish with published/err.
	// Unpublish (idempotent) drops the route; the router also GCs
	// every token for an instance when that instance tears down.
	TEvtIngressPublish   = "ingress.publish"
	TEvtIngressPublished = "ingress.published"
	TEvtIngressUnpublish = "ingress.unpublish"
	TEvtIngressErr       = "ingress.err"
)

// EvtWindowMapped: router → app, "the window is now visible".
type EvtWindowMapped struct {
	T   string `json:"t"`
	Win uint32 `json:"win"`
}

func NewEvtWindowMapped(win uint32) EvtWindowMapped {
	return EvtWindowMapped{T: TEvtWindowMapped, Win: win}
}

// EvtWindowFocus: router → app, focus gained.
type EvtWindowFocus struct {
	T   string `json:"t"`
	Win uint32 `json:"win"`
}

func NewEvtWindowFocus(win uint32) EvtWindowFocus {
	return EvtWindowFocus{T: TEvtWindowFocus, Win: win}
}

// EvtWindowUnfocus: router → app, focus lost.
type EvtWindowUnfocus struct {
	T   string `json:"t"`
	Win uint32 `json:"win"`
}

func NewEvtWindowUnfocus(win uint32) EvtWindowUnfocus {
	return EvtWindowUnfocus{T: TEvtWindowUnfocus, Win: win}
}

// EvtWindowResize: router → app, the window's pixel size changed
// (typically at drag-resize end). v0.1 emits this once per commit;
// live-resize is deferred.
type EvtWindowResize struct {
	T   string `json:"t"`
	Win uint32 `json:"win"`
	W   uint32 `json:"w"`
	H   uint32 `json:"h"`
}

func NewEvtWindowResize(win, w, h uint32) EvtWindowResize {
	return EvtWindowResize{T: TEvtWindowResize, Win: win, W: w, H: h}
}

// EvtWindowState: router → app, the user changed min/max/restore.
// State is one of WindowStateNormal / WindowStateMinimized /
// WindowStateMaximized (string-equal to shared constants on the wire).
type EvtWindowState struct {
	T     string `json:"t"`
	Win   uint32 `json:"win"`
	State string `json:"state"`
}

func NewEvtWindowState(win uint32, state string) EvtWindowState {
	return EvtWindowState{T: TEvtWindowState, Win: win, State: state}
}

// EvtWindowCloseRequested: router → app, X WM_DELETE analogue. App
// MUST reply with EvtWindowConfirmClose within grace (§10).
type EvtWindowCloseRequested struct {
	T   string `json:"t"`
	Win uint32 `json:"win"`
}

func NewEvtWindowCloseRequested(win uint32) EvtWindowCloseRequested {
	return EvtWindowCloseRequested{T: TEvtWindowCloseRequested, Win: win}
}

// EvtShutdown: router → app, "I'm going away, close cleanly".
type EvtShutdown struct {
	T string `json:"t"`
}

func NewEvtShutdown() EvtShutdown { return EvtShutdown{T: TEvtShutdown} }

// EvtWindowSetTitle: app → router, request titlebar text change.
type EvtWindowSetTitle struct {
	T     string `json:"t"`
	Win   uint32 `json:"win"`
	Title string `json:"title"`
}

func NewEvtWindowSetTitle(win uint32, title string) EvtWindowSetTitle {
	return EvtWindowSetTitle{T: TEvtWindowSetTitle, Win: win, Title: title}
}

// EvtWindowGeometry: app → router, the app's own window content changed
// size. The router applies it via windowSession.resize so the shell
// frame tracks the content. Only honored for windows the sender owns.
type EvtWindowGeometry struct {
	T   string `json:"t"`
	Win uint32 `json:"win"`
	W   uint32 `json:"w"`
	H   uint32 `json:"h"`
}

func NewEvtWindowGeometry(win, w, h uint32) EvtWindowGeometry {
	return EvtWindowGeometry{T: TEvtWindowGeometry, Win: win, W: w, H: h}
}

// EvtWindowConfirmClose: app's answer to EvtWindowCloseRequested.
// Allow=true tears down the window; allow=false vetoes it.
type EvtWindowConfirmClose struct {
	T     string `json:"t"`
	Win   uint32 `json:"win"`
	Allow bool   `json:"allow"`
}

func NewEvtWindowConfirmClose(win uint32, allow bool) EvtWindowConfirmClose {
	return EvtWindowConfirmClose{T: TEvtWindowConfirmClose, Win: win, Allow: allow}
}

// Window roles for EvtWindowCreate. Empty == toplevel. A popup is a
// transient child (menu, tooltip, X11 override-redirect) positioned
// relative to ParentWin; the shell renders it borderless.
const (
	WindowRoleToplevel = "toplevel"
	WindowRolePopup    = "popup"
)

// EvtWindowCreate: app → router, "give me another window". The router
// allocates a window id, registers a SessionWindow under the calling
// instance, and replies with EvtWindowCreated (or EvtWindowCreateErr).
// Gated by the "windows" capability. ReqID correlates the reply.
// W/H are the requested initial pixel size; Min*/Max* are hints the
// shell's WM may honour. ParentWin is required for role "popup".
type EvtWindowCreate struct {
	T         string `json:"t"`
	ReqID     uint64 `json:"req_id"`
	Role      string `json:"role,omitempty"`
	ParentWin uint32 `json:"parent_win,omitempty"`
	Title     string `json:"title,omitempty"`
	W         uint32 `json:"w,omitempty"`
	H         uint32 `json:"h,omitempty"`
	MinW      uint32 `json:"min_w,omitempty"`
	MinH      uint32 `json:"min_h,omitempty"`
	MaxW      uint32 `json:"max_w,omitempty"`
	MaxH      uint32 `json:"max_h,omitempty"`
	// Element optionally overrides the custom-element tag the shell
	// mounts for this window. Empty inherits the requesting instance's
	// manifest element (the common case). A multi-window app can name a
	// per-window decoder/view here (e.g. the built-in "wash-app-display").
	Element string `json:"element,omitempty"`
}

func NewEvtWindowCreate(reqID uint64, role string, parentWin uint32, title string, w, h uint32, element string) EvtWindowCreate {
	return EvtWindowCreate{T: TEvtWindowCreate, ReqID: reqID, Role: role, ParentWin: parentWin, Title: title, W: w, H: h, Element: element}
}

// EvtWindowCreated: router → app, the window exists and is in the
// session. Win is the freshly allocated id; subsequent window.* events
// (resize/focus/…) and OpenChannel calls key on it.
type EvtWindowCreated struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	Win   uint32 `json:"win"`
}

func NewEvtWindowCreated(reqID uint64, win uint32) EvtWindowCreated {
	return EvtWindowCreated{T: TEvtWindowCreated, ReqID: reqID, Win: win}
}

// EvtWindowCreateErr: router → app, the create was refused. Capability
// denial uses code "forbidden"; a per-instance window-count cap uses
// "forbidden" too with an explanatory msg.
type EvtWindowCreateErr struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	Code  string `json:"code"`
	Msg   string `json:"msg"`
}

func NewEvtWindowCreateErr(reqID uint64, code, msg string) EvtWindowCreateErr {
	return EvtWindowCreateErr{T: TEvtWindowCreateErr, ReqID: reqID, Code: code, Msg: msg}
}

// EvtWindowDestroy: app → router, tear down one of my windows (the
// app's own toplevel went away). Fire-and-forget; the router emits the
// session window.delete. Instance teardown GCs all its windows anyway.
type EvtWindowDestroy struct {
	T   string `json:"t"`
	Win uint32 `json:"win"`
}

func NewEvtWindowDestroy(win uint32) EvtWindowDestroy {
	return EvtWindowDestroy{T: TEvtWindowDestroy, Win: win}
}

// EvtEnvPublish: app → router. Env is a set of WASH_*-namespaced
// environment hints the router merges into every subsequently-spawned
// app's environment. See docs/DISPLAY_ENV.md and Router.spawnEnv.
type EvtEnvPublish struct {
	T   string            `json:"t"`
	Env map[string]string `json:"env"`
}

func NewEvtEnvPublish(env map[string]string) EvtEnvPublish {
	return EvtEnvPublish{T: TEvtEnvPublish, Env: env}
}

// EvtEnvPublishErr: router → app, the publish was rejected (capability
// not declared). Fire-and-forget; the app isn't expected to retry.
type EvtEnvPublishErr struct {
	T    string `json:"t"`
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

func NewEvtEnvPublishErr(code, msg string) EvtEnvPublishErr {
	return EvtEnvPublishErr{T: TEvtEnvPublishErr, Code: code, Msg: msg}
}

// EvtSpawnRequest is the unified spawn request: app → router.
//
//   - Normal spawn (router does fork+exec): Prepare=false. Gated by
//     the "spawn" capability. ReqID is optional.
//   - Prepare spawn (caller will fork+exec, e.g. wash-priv under
//     sudo): Prepare=true. Gated by the "prepare_spawn" capability.
//     ReqID required for correlation with EvtSpawnOk{AttachToken}.
type EvtSpawnRequest struct {
	T       string `json:"t"`
	ReqID   uint64 `json:"req_id,omitempty"`
	AppID   string `json:"app_id"`
	Prepare bool   `json:"prepare,omitempty"`
}

func NewEvtSpawnRequest(appID string) EvtSpawnRequest {
	return EvtSpawnRequest{T: TEvtSpawnRequest, AppID: appID}
}

// NewEvtPrepareSpawnRequest is the prepare-spawn variant. Same wire
// type; Prepare=true; ReqID required for the caller's correlation.
func NewEvtPrepareSpawnRequest(reqID uint64, appID string) EvtSpawnRequest {
	return EvtSpawnRequest{T: TEvtSpawnRequest, ReqID: reqID, AppID: appID, Prepare: true}
}

// EvtSpawnOk: router → app, spawn succeeded.
//
// AttachToken + Binary are populated only for prepare-spawn replies.
// The caller should exec the binary (or a wrapper such as sudo) with
// env including WASH_DISPLAY, WASH_PROTO, WASH_APP_ID,
// WASH_INSTANCE_ID, and WASH_ATTACH_TOKEN.
type EvtSpawnOk struct {
	T           string `json:"t"`
	ReqID       uint64 `json:"req_id,omitempty"`
	AppID       string `json:"app_id"`
	InstanceID  string `json:"instance_id"`
	AttachToken string `json:"attach_token,omitempty"`
	Binary      string `json:"binary,omitempty"`
}

func NewEvtSpawnOk(appID, instanceID string) EvtSpawnOk {
	return EvtSpawnOk{T: TEvtSpawnOk, AppID: appID, InstanceID: instanceID}
}

// NewEvtSpawnOkPrepared is the prepare-spawn reply variant. Carries
// the minted instance id, attach token, and registered binary path.
func NewEvtSpawnOkPrepared(reqID uint64, appID, instanceID, token, binary string) EvtSpawnOk {
	return EvtSpawnOk{T: TEvtSpawnOk, ReqID: reqID, AppID: appID, InstanceID: instanceID, AttachToken: token, Binary: binary}
}

// EvtSpawnErr: router → app, spawn refused. Capability denial uses
// code "forbidden"; unknown id "not_found"; ABI mismatch
// "incompatible_protocol".
type EvtSpawnErr struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id,omitempty"`
	AppID string `json:"app_id,omitempty"`
	Code  string `json:"code"`
	Msg   string `json:"msg"`
}

func NewEvtSpawnErr(appID, code, msg string) EvtSpawnErr {
	return EvtSpawnErr{T: TEvtSpawnErr, AppID: appID, Code: code, Msg: msg}
}

// NewEvtSpawnErrPrepared is the prepare-spawn error variant: echoes
// ReqID so the caller can match the rejection to its in-flight
// request.
func NewEvtSpawnErrPrepared(reqID uint64, code, msg string) EvtSpawnErr {
	return EvtSpawnErr{T: TEvtSpawnErr, ReqID: reqID, Code: code, Msg: msg}
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
	T     string `json:"t"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Level string `json:"level,omitempty"`
}

func NewEvtNotify(title, body, level string) EvtNotify {
	return EvtNotify{T: TEvtNotify, Title: title, Body: body, Level: level}
}

// EvtClipboardSet writes new content to the router-held clipboard.
// The router broadcasts EvtClipboardChanged to every connected app
// (except the sender) so they can react. Data is base64 on the JSON
// wire — encoding/json's default for []byte; the SDK handles the
// round-trip transparently.
type EvtClipboardSet struct {
	T    string `json:"t"`
	Mime string `json:"mime"`
	Data []byte `json:"data"`
}

func NewEvtClipboardSet(mime string, data []byte) EvtClipboardSet {
	return EvtClipboardSet{T: TEvtClipboardSet, Mime: mime, Data: data}
}

// EvtClipboardGet requests the current clipboard contents. The router
// replies with EvtClipboardData. req_id correlates response with caller.
type EvtClipboardGet struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
}

func NewEvtClipboardGet(reqID uint64) EvtClipboardGet {
	return EvtClipboardGet{T: TEvtClipboardGet, ReqID: reqID}
}

// EvtClipboardData is the router → app response containing the
// clipboard's current mime and bytes.
type EvtClipboardData struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	Mime  string `json:"mime"`
	Data  []byte `json:"data"`
}

func NewEvtClipboardData(reqID uint64, mime string, data []byte) EvtClipboardData {
	return EvtClipboardData{T: TEvtClipboardData, ReqID: reqID, Mime: mime, Data: data}
}

// EvtClipboardChanged is broadcast by the router to every app
// (excluding the setter) when the clipboard changes. Apps re-get
// content if interested.
type EvtClipboardChanged struct {
	T    string `json:"t"`
	Mime string `json:"mime"`
}

func NewEvtClipboardChanged(mime string) EvtClipboardChanged {
	return EvtClipboardChanged{T: TEvtClipboardChanged, Mime: mime}
}

// EvtIngressPublish — app → router — register an HTTP/WS backend the
// app's BE is serving and ask for a public ingress path. Network is
// "unix" (Addr is a socket path) or "tcp" (Addr is host:port, which
// the router will dial on loopback). BasePath, if set, is the path
// prefix the backend expects to be served under — the router echoes
// the minted path in an X-Wash-Ingress-Path header regardless, but
// some backends want it confirmed up front. req_id correlates with
// EvtIngressPublished / EvtIngressErr.
type EvtIngressPublish struct {
	T       string `json:"t"`
	ReqID   uint64 `json:"req_id"`
	Network string `json:"network"`
	Addr    string `json:"addr"`
}

func NewEvtIngressPublish(reqID uint64, network, addr string) EvtIngressPublish {
	return EvtIngressPublish{T: TEvtIngressPublish, ReqID: reqID, Network: network, Addr: addr}
}

// EvtIngressPublished — router → app — the public base path (e.g.
// "/app/<token>/") the FE should load in its iframe. Token is the
// raw capability token, also embedded in Path; apps generally only
// need Path.
type EvtIngressPublished struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	Path  string `json:"path"`
	Token string `json:"token"`
}

func NewEvtIngressPublished(reqID uint64, path, token string) EvtIngressPublished {
	return EvtIngressPublished{T: TEvtIngressPublished, ReqID: reqID, Path: path, Token: token}
}

// EvtIngressUnpublish — app → router — drop a previously published
// route by its path. Idempotent: unpublishing an unknown/already-gone
// path is a no-op. Fire-and-forget (no reply).
type EvtIngressUnpublish struct {
	T    string `json:"t"`
	Path string `json:"path"`
}

func NewEvtIngressUnpublish(path string) EvtIngressUnpublish {
	return EvtIngressUnpublish{T: TEvtIngressUnpublish, Path: path}
}

// EvtIngressErr — router → app — publish failed (bad request, no
// shell, internal). req_id correlates with the originating publish.
type EvtIngressErr struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	Code  string `json:"code"`
	Msg   string `json:"msg"`
}

func NewEvtIngressErr(reqID uint64, code, msg string) EvtIngressErr {
	return EvtIngressErr{T: TEvtIngressErr, ReqID: reqID, Code: code, Msg: msg}
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
	AppID      string `json:"app_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
}

// EvtAppMsg is the unified app-private message envelope on the
// event channel. Data is intentionally opaque; the router never
// inspects it. The optional From / To fields direct routing:
//
//   - own-FE ↔ own-BE: neither set (router relays to/from the
//     instance's other half by socket).
//   - cross-app outbound: To set by the sender; router resolves the
//     recipient and forwards as a new EvtAppMsg with From set on
//     the receiver's side.
//   - cross-app inbound: From set by router; never trust this field
//     on the sender side — it is router-attested.
//
// Data is json.RawMessage so the router can relay bytes verbatim —
// no decode + re-encode hop.
type EvtAppMsg struct {
	T    string          `json:"t"`
	Win  uint32          `json:"win,omitempty"`
	Data json.RawMessage `json:"data"`
	From *Sender         `json:"from,omitempty"`
	To   *Recipient      `json:"to,omitempty"`
}

// NewEvtAppMsg builds an EvtAppMsg with data marshaled to JSON. data
// may be any json-marshalable value (struct, map, slice, primitive).
// Pre-encoded json.RawMessage is shipped verbatim — useful for relays
// that want to avoid re-encode cost.
func NewEvtAppMsg(win uint32, data any) EvtAppMsg {
	return EvtAppMsg{T: TEvtAppMsg, Win: win, Data: mustJSON(data)}
}

// NewEvtAppMsgFrom builds an EvtAppMsg whose From field identifies
// the originating instance. Only the router constructs these — it
// is the relay path for cross-app sends.
func NewEvtAppMsgFrom(win uint32, data any, from Sender) EvtAppMsg {
	return EvtAppMsg{T: TEvtAppMsg, Win: win, Data: mustJSON(data), From: &from}
}

// NewEvtAppMsgTo builds an EvtAppMsg addressed to a cross-app
// recipient. The router resolves the target (instance_id direct, or
// app_id sentinel for singletons) and forwards as a new EvtAppMsg
// on the receiver's socket with From set to the sender.
func NewEvtAppMsgTo(to Recipient, data any) EvtAppMsg {
	return EvtAppMsg{T: TEvtAppMsg, Data: mustJSON(data), To: &to}
}

// Recipient is the address used by cross-app EvtAppMsg and the
// shell's ShellAppMsgSend. Exactly one of AppID / InstanceID should
// be set: AppID resolves via the singleton table (failing if the
// named app isn't singleton-instancing); InstanceID is a direct
// reference to an already-running app.
type Recipient struct {
	AppID      string `json:"app_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
}

// EvtAppStateSet — app → router — persist `State` as this app's
// FE-state blob (router stores keyed by instance_id). The blob is
// JSON; the router never inspects it. Used by SDK.SaveState for
// apps whose state lives BE-side or is computed in Go. FE-only apps
// can use the shell's window.wash.saveState instead.
type EvtAppStateSet struct {
	T     string          `json:"t"`
	State json.RawMessage `json:"state"`
}

func NewEvtAppStateSet(state []byte) EvtAppStateSet {
	return EvtAppStateSet{T: TEvtAppStateSet, State: json.RawMessage(state)}
}

// EvtRuntimeStats is a periodic heartbeat from each app's SDK to the
// router. Two purposes in one envelope:
//
//   - Liveness: arrival proves the BE is responsive. Universal —
//     every wash app, in every implementation language, is expected
//     to ship one of these every few seconds.
//   - Go self-report: optional bag of Go-runtime metrics (heap,
//     goroutines, GC) the Go SDK fills in. A future Rust / Python
//     SDK would leave these zero and set RuntimeKind to its own
//     label; the router treats the absence the same as a Go app
//     that hasn't run a GC yet.
//
// RuntimeKind is the discriminator. Empty defaults to "go" for
// backwards compat with binaries that predate this field. Non-go
// kinds tell the About table to render heap/goroutine/GC columns
// as "—" rather than misleading zeros.
//
// /proc-derived RSS / VSize aren't shipped here — the router reads
// /proc/<pid>/statm at query time. RSS is a kernel fact, not an
// app self-report; no SDK should be expected to know it.
//
// Cadence: ~5s (SDK constant). All memory fields are bytes.
type EvtRuntimeStats struct {
	T string `json:"t"`
	// RuntimeKind is "go", "rust", "node", etc. Empty == "go" for
	// pre-field binaries. Lets the consumer skip language-specific
	// columns rather than display zeros.
	RuntimeKind string `json:"runtime_kind,omitempty"`
	// UptimeSec since the BE began. Universal across languages.
	UptimeSec int64 `json:"uptime_sec"`

	// ----- Go-specific (optional for non-Go BEs) -----
	GoVersion    string `json:"go_version,omitempty"`
	NumGoroutine int    `json:"num_goroutine,omitempty"`
	NumCgoCall   int64  `json:"num_cgo_call,omitempty"`
	HeapAlloc    uint64 `json:"heap_alloc,omitempty"`
	HeapInuse    uint64 `json:"heap_inuse,omitempty"`
	HeapObjects  uint64 `json:"heap_objects,omitempty"`
	StackInuse   uint64 `json:"stack_inuse,omitempty"`
	Sys          uint64 `json:"sys,omitempty"`
	NumGC        uint32 `json:"num_gc,omitempty"`
	GCPauseTotal uint64 `json:"gc_pause_total_ns,omitempty"`
}

// mustJSON marshals v with encoding/json. Internal use — callers
// (constructors above) take already-validated Go values whose JSON
// shapes are stable. On marshal failure we ship a JSON null rather
// than a malformed payload; downstream decoders treat null as the
// zero value, which is the same semantic as a missing field.
func mustJSON(v any) json.RawMessage {
	if rm, ok := v.(json.RawMessage); ok {
		return rm
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// PeekEvtType returns the t field of a JSON object without otherwise
// decoding it.
func PeekEvtType(data []byte) (string, error) {
	var probe struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return "", err
	}
	return probe.T, nil
}

// DecodeEvt parses a single JSON event-channel message into the
// concrete type matching its t field.
func DecodeEvt(data []byte) (any, error) {
	t, err := PeekEvtType(data)
	if err != nil {
		return nil, fmt.Errorf("evt decode: %w", err)
	}
	switch t {
	case TEvtWindowMapped:
		var m EvtWindowMapped
		return m, json.Unmarshal(data, &m)
	case TEvtWindowFocus:
		var m EvtWindowFocus
		return m, json.Unmarshal(data, &m)
	case TEvtWindowUnfocus:
		var m EvtWindowUnfocus
		return m, json.Unmarshal(data, &m)
	case TEvtWindowResize:
		var m EvtWindowResize
		return m, json.Unmarshal(data, &m)
	case TEvtWindowState:
		var m EvtWindowState
		return m, json.Unmarshal(data, &m)
	case TEvtWindowCloseRequested:
		var m EvtWindowCloseRequested
		return m, json.Unmarshal(data, &m)
	case TEvtShutdown:
		var m EvtShutdown
		return m, json.Unmarshal(data, &m)
	case TEvtWindowSetTitle:
		var m EvtWindowSetTitle
		return m, json.Unmarshal(data, &m)
	case TEvtWindowGeometry:
		var m EvtWindowGeometry
		return m, json.Unmarshal(data, &m)
	case TEvtWindowConfirmClose:
		var m EvtWindowConfirmClose
		return m, json.Unmarshal(data, &m)
	case TEvtWindowCreate:
		var m EvtWindowCreate
		return m, json.Unmarshal(data, &m)
	case TEvtWindowCreated:
		var m EvtWindowCreated
		return m, json.Unmarshal(data, &m)
	case TEvtWindowCreateErr:
		var m EvtWindowCreateErr
		return m, json.Unmarshal(data, &m)
	case TEvtWindowDestroy:
		var m EvtWindowDestroy
		return m, json.Unmarshal(data, &m)
	case TEvtEnvPublish:
		var m EvtEnvPublish
		return m, json.Unmarshal(data, &m)
	case TEvtEnvPublishErr:
		var m EvtEnvPublishErr
		return m, json.Unmarshal(data, &m)
	case TEvtSpawnRequest:
		var m EvtSpawnRequest
		return m, json.Unmarshal(data, &m)
	case TEvtSpawnOk:
		var m EvtSpawnOk
		return m, json.Unmarshal(data, &m)
	case TEvtSpawnErr:
		var m EvtSpawnErr
		return m, json.Unmarshal(data, &m)
	case TEvtAppMsg:
		var m EvtAppMsg
		return m, json.Unmarshal(data, &m)
	case TEvtNotify:
		var m EvtNotify
		return m, json.Unmarshal(data, &m)
	case TEvtClipboardSet:
		var m EvtClipboardSet
		return m, json.Unmarshal(data, &m)
	case TEvtClipboardGet:
		var m EvtClipboardGet
		return m, json.Unmarshal(data, &m)
	case TEvtClipboardData:
		var m EvtClipboardData
		return m, json.Unmarshal(data, &m)
	case TEvtClipboardChanged:
		var m EvtClipboardChanged
		return m, json.Unmarshal(data, &m)
	case TEvtAppStateSet:
		var m EvtAppStateSet
		return m, json.Unmarshal(data, &m)
	case TEvtRuntimeStats:
		var m EvtRuntimeStats
		return m, json.Unmarshal(data, &m)
	case TEvtIngressPublish:
		var m EvtIngressPublish
		return m, json.Unmarshal(data, &m)
	case TEvtIngressPublished:
		var m EvtIngressPublished
		return m, json.Unmarshal(data, &m)
	case TEvtIngressUnpublish:
		var m EvtIngressUnpublish
		return m, json.Unmarshal(data, &m)
	case TEvtIngressErr:
		var m EvtIngressErr
		return m, json.Unmarshal(data, &m)
	}
	return nil, fmt.Errorf("evt decode: unknown t %q", t)
}

// EncodeEvt marshals a JSON event-channel message. Equivalent to
// json.Marshal; provided for symmetry with EncodeCtrl.
func EncodeEvt(v any) ([]byte, error) {
	return json.Marshal(v)
}
