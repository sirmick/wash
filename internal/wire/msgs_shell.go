package wire

import "encoding/json"

// WS shell channel 0 vocabulary (WIRE.md §8). Encoding: JSON.
//
// "Shell" here means the browser shell runtime — the compositor that
// hosts web components. The router speaks this vocabulary directly to
// it; the router translates between this and the per-app event
// channel (§9) as needed.
//
// Type names are prefixed Shell to disambiguate from event-channel
// types (§9) that use overlapping names like window.focus.
const (
	// Router → shell.
	TShellCatalog         = "catalog"
	TShellAppDeclared     = "app.declared"
	TShellSessionSnapshot = "session.snapshot"
	TShellSessionPatch    = "session.patch"

	// Shell → router.
	TShellWindowCloseClicked = "window.close_clicked"
	TShellWindowFocus        = "window.focus"
	TShellWindowMove         = "window.move"
	TShellWindowResize       = "window.resize"
	TShellWindowState        = "window.state"
	TShellAppMsgSend         = "app_msg.send"
	// Shell → router. Cross-app send: relay to a different instance
	// (peer to EvtAppMsgSendTo on the event channel). Same Recipient
	// addressing rules: app_id for singletons, instance_id for any
	// running app.
	TShellAppMsgSendTo = "app_msg.send.to"
	TShellLog          = "log"

	// Router → shell (BE → FE relay).
	TShellAppMsgDeliver = "app_msg.deliver"

	// Router → shell, relayed notification.
	TShellNotify = "notify"

	// Router → shell, raw channel bound / unbound. The shell uses
	// these to route incoming raw frames to the matching window's
	// element and to know when a channel id is dead.
	TShellChannelBind   = "channel.bind"
	TShellChannelUnbind = "channel.unbind"

	// Shell → router, per-channel credit grant. Lets the FE pace
	// individual raw streams ("I'm slow rendering wash-fm — pause
	// that channel") without affecting other channels on the same
	// connection. See docs/QOS.md §5.
	TShellChannelCredit = "channel.credit"

	// Router → shell, "reload your page now." Used by --dev mode
	// when a watched binary changes — the embedded shell/app
	// bundles are stale and customElements is one-shot per tab.
	// Shell handles this by calling window.location.reload().
	TShellReload = "shell.reload"

	// Router → shell, "the BE for this instance crashed." Carries
	// captured stdout/stderr (last few KB) + exit info. The shell
	// converts the matching window into a crash tombstone in place,
	// preserving geometry so the user can copy the trace.
	TShellAppCrashed = "app.crashed"

	// Shell → router, asset pull. Read a single file from the
	// router's embedded shell-asset FS (icons.svg, future docs, etc.).
	TShellAssetRead = "asset.read"
	// Router → shell, asset pull accepted. ChannelID will stream the
	// file bytes via Kind="asset". ChannelClose marks EOF.
	TShellAssetReadOK = "asset.read.ok"
	// Router → shell, asset pull rejected. Code mirrors §13.
	TShellAssetReadErr = "asset.read.err"
)

// ShellLog levels.
const (
	LogLevelError = "error"
	LogLevelWarn  = "warn"
	LogLevelInfo  = "info"
	LogLevelDebug = "debug"
)

// ShellLog is a browser-side log line forwarded to the router so
// stdout can show what the browser sees. The shell wires window.onerror
// and unhandledrejection to this; apps can opt in via window.wash.log.
//
// Intentionally untyped beyond level/msg/source/stack — anything
// fancier (structured fields, breadcrumbs) belongs in a real
// telemetry pipeline, not on the WS.
type ShellLog struct {
	T      string `json:"t"`
	Level  string `json:"level"`
	Msg    string `json:"msg"`
	Source string `json:"source,omitempty"`
	Stack  string `json:"stack,omitempty"`
}

func NewShellLog(level, source, msg, stack string) ShellLog {
	return ShellLog{T: TShellLog, Level: level, Msg: msg, Source: source, Stack: stack}
}

// ShellCatalogApp is one row of the launchable-apps catalog sent from
// the router to the shell on connect. Mirrors the manifest fields the
// chrome needs to render menus and quick-launches; full manifest
// inspection still happens via app.declared when an instance is
// actually created.
type ShellCatalogApp struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Icon       string `json:"icon,omitempty"`
	Surface    string `json:"surface"`
	Instancing string `json:"instancing"`
	Disabled   bool   `json:"disabled,omitempty"`
	Reason     string `json:"reason,omitempty"`

	// RootVariant mirrors the same manifest field. When non-nil the
	// shell renders an additional synthetic catalog row "<Name>
	// (root)" that routes through wash-priv on click.
	RootVariant *RootVariant `json:"root_variant,omitempty"`
}

// ShellCatalog is the router's "here is what is launchable" snapshot.
// Sent once on shell connect; v0.1 will revisit live updates.
type ShellCatalog struct {
	T    string            `json:"t"`
	Apps []ShellCatalogApp `json:"apps"`
}

func NewShellCatalog(apps []ShellCatalogApp) ShellCatalog {
	return ShellCatalog{T: TShellCatalog, Apps: apps}
}

// ShellAppDeclared tells the shell a new app instance has been
// accepted by the router and is ready to be mounted. The shell should
// fetch the bundle and prepare to host the element.
type ShellAppDeclared struct {
	T          string          `json:"t"`
	InstanceID string          `json:"instance_id"`
	Element    string          `json:"element"`
	Surface    string          `json:"surface"` // "window" | "desktop"
	Manifest   json.RawMessage `json:"manifest"`
}

func NewShellAppDeclared(instanceID, element, surface string, manifest json.RawMessage) ShellAppDeclared {
	return ShellAppDeclared{T: TShellAppDeclared, InstanceID: instanceID, Element: element, Surface: surface, Manifest: manifest}
}

// SessionWindow is the router-canonical state for one window. The
// router owns geometry, z-order, focus, and lifecycle state; shells
// are observers that emit pointer events back as wire messages.
//
// X/Y are int32 so a window can sit partially off-screen (negative)
// without overflow. W/H/Z are uint32.
type SessionWindow struct {
	WindowID   uint32 `json:"window_id"`
	InstanceID string `json:"instance_id"`
	Element    string `json:"element"`
	Icon       string `json:"icon,omitempty"`
	Title      string `json:"title"`
	X          int32  `json:"x"`
	Y          int32  `json:"y"`
	W          uint32 `json:"w"`
	H          uint32 `json:"h"`
	Z          uint32 `json:"z"`
	State      string `json:"state"` // normal | minimized | maximized
	Focused    bool   `json:"focused"`
	// IsRoot is true when the owning app process runs as uid 0, or
	// when the app id is reserved as part of the privilege chain
	// (com.wash.priv). The shell's WM paints a red stripe and ROOT
	// label on the titlebar of such windows. The router fills this
	// from SO_PEERCRED at attach time and from the reserved-id
	// list — never from anything the app declares.
	IsRoot bool `json:"is_root,omitempty"`
	// Pre-min/max geometry; preserved so restoreWindow returns to the
	// user-set frame even after a chain of min → max → restore.
	RestoreX int32  `json:"restore_x,omitempty"`
	RestoreY int32  `json:"restore_y,omitempty"`
	RestoreW uint32 `json:"restore_w,omitempty"`
	RestoreH uint32 `json:"restore_h,omitempty"`
}

// ShellSessionSnapshot is the router's "here is everything you need
// to render" message, sent on shell connect immediately after the
// catalog. After this, all state changes arrive as ShellSessionPatch.
//
// AppState carries each instance's saved FE state blob (per
// app_state.save); the shell delivers it as wash:state to the
// matching mounted element on (re)mount.
type ShellSessionSnapshot struct {
	T        string                     `json:"t"`
	Windows  []SessionWindow            `json:"windows"`
	AppState map[string]json.RawMessage `json:"app_state,omitempty"`
}

func NewShellSessionSnapshot(wins []SessionWindow, appState map[string]json.RawMessage) ShellSessionSnapshot {
	if wins == nil {
		wins = []SessionWindow{}
	}
	return ShellSessionSnapshot{T: TShellSessionSnapshot, Windows: wins, AppState: appState}
}

// SessionPatchOp values.
const (
	SessionPatchWindowUpsert = "window.upsert"
	SessionPatchWindowDelete = "window.delete"
	SessionPatchAppState     = "app_state"
)

// SessionPatch is one change to the session state. The active fields
// depend on Op:
//
//	window.upsert  → Window
//	window.delete  → WindowID
//	app_state      → InstanceID, State
type SessionPatch struct {
	Op         string          `json:"op"`
	Window     *SessionWindow  `json:"window,omitempty"`
	WindowID   uint32          `json:"window_id,omitempty"`
	InstanceID string          `json:"instance_id,omitempty"`
	State      json.RawMessage `json:"state,omitempty"`
}

// ShellSessionPatch is a batched set of state mutations the shell
// applies in order. Batching matters for atomic-feeling updates (e.g.
// focus change clears Focused on the old window AND sets it on the
// new one — both upserts in one patch).
type ShellSessionPatch struct {
	T       string         `json:"t"`
	Patches []SessionPatch `json:"patches"`
}

func NewShellSessionPatch(patches ...SessionPatch) ShellSessionPatch {
	return ShellSessionPatch{T: TShellSessionPatch, Patches: patches}
}

// ShellWindowCloseClicked is the user clicking a titlebar close.
type ShellWindowCloseClicked struct {
	T        string `json:"t"`
	WindowID uint32 `json:"window_id"`
}

func NewShellWindowCloseClicked(windowID uint32) ShellWindowCloseClicked {
	return ShellWindowCloseClicked{T: TShellWindowCloseClicked, WindowID: windowID}
}

// ShellWindowFocus is the user clicking/focusing a window; the shell
// also raises it. The router relays a corresponding EvtWindowFocus to
// the owning app on its event channel.
type ShellWindowFocus struct {
	T        string `json:"t"`
	WindowID uint32 `json:"window_id"`
}

func NewShellWindowFocus(windowID uint32) ShellWindowFocus {
	return ShellWindowFocus{T: TShellWindowFocus, WindowID: windowID}
}

// ShellWindowMove is the user committing a new position (drag-move
// end). v0.1 only emits this on drag completion; live position
// streaming would be a future opt-in (multi-cursor coordination etc).
type ShellWindowMove struct {
	T        string `json:"t"`
	WindowID uint32 `json:"window_id"`
	X        int32  `json:"x"`
	Y        int32  `json:"y"`
}

func NewShellWindowMove(windowID uint32, x, y int32) ShellWindowMove {
	return ShellWindowMove{T: TShellWindowMove, WindowID: windowID, X: x, Y: y}
}

// ShellWindowResize is the user committing a new size (drag-resize
// end). The router relays EvtWindowResize to the owning app. v0.1
// only emits this on resize completion; live-resize for apps that
// need it (e.g. a terminal) is a deferred opt-in.
type ShellWindowResize struct {
	T        string `json:"t"`
	WindowID uint32 `json:"window_id"`
	W        uint32 `json:"w"`
	H        uint32 `json:"h"`
}

func NewShellWindowResize(windowID, w, h uint32) ShellWindowResize {
	return ShellWindowResize{T: TShellWindowResize, WindowID: windowID, W: w, H: h}
}

// Window state values for ShellWindowState / EvtWindowState.
const (
	WindowStateNormal    = "normal"
	WindowStateMinimized = "minimized"
	WindowStateMaximized = "maximized"
)

// ShellWindowState is the user changing min/max/restore. The router
// relays EvtWindowState to the owning app so apps can react (e.g. a
// terminal pausing redraws when minimized).
type ShellWindowState struct {
	T        string `json:"t"`
	WindowID uint32 `json:"window_id"`
	State    string `json:"state"`
}

func NewShellWindowState(windowID uint32, state string) ShellWindowState {
	return ShellWindowState{T: TShellWindowState, WindowID: windowID, State: state}
}

// ShellAppMsgSend is the FE half of an app sending an APP_MSG to its
// BE half. Data is intentionally opaque to the router; it carries
// whatever the app uses domain-side.
type ShellAppMsgSend struct {
	T          string          `json:"t"`
	InstanceID string          `json:"instance_id"`
	Data       json.RawMessage `json:"data"`
}

func NewShellAppMsgSend(instanceID string, data json.RawMessage) ShellAppMsgSend {
	return ShellAppMsgSend{T: TShellAppMsgSend, InstanceID: instanceID, Data: data}
}

// ShellAppMsgSendTo is the FE → router cross-app send. Mirrors
// EvtAppMsgSendTo on the event channel; lets a FE (e.g. fm)
// dispatch a job to a singleton service (e.g. wash-bulk) without
// going through its own BE.
type ShellAppMsgSendTo struct {
	T         string          `json:"t"`
	Recipient Recipient       `json:"recipient"`
	Data      json.RawMessage `json:"data"`
}

func NewShellAppMsgSendTo(r Recipient, data json.RawMessage) ShellAppMsgSendTo {
	return ShellAppMsgSendTo{T: TShellAppMsgSendTo, Recipient: r, Data: data}
}

// ShellAppMsgDeliver is the reverse: a BE → FE message, relayed to
// the shell. The shell forwards it to the matching mounted element
// as a CustomEvent.
type ShellAppMsgDeliver struct {
	T          string          `json:"t"`
	InstanceID string          `json:"instance_id"`
	Data       json.RawMessage `json:"data"`
}

func NewShellAppMsgDeliver(instanceID string, data json.RawMessage) ShellAppMsgDeliver {
	return ShellAppMsgDeliver{T: TShellAppMsgDeliver, InstanceID: instanceID, Data: data}
}

// ShellNotify is a notification the shell should display. Originates
// in an app's EvtNotify; the router stamps the source instance id
// so the chrome can attribute / route per-app (e.g. "do not disturb
// from app X").
type ShellNotify struct {
	T          string `json:"t"`
	InstanceID string `json:"instance_id"`
	Title      string `json:"title"`
	Body       string `json:"body,omitempty"`
	Level      string `json:"level,omitempty"`
}

func NewShellNotify(instanceID, title, body, level string) ShellNotify {
	return ShellNotify{T: TShellNotify, InstanceID: instanceID, Title: title, Body: body, Level: level}
}

// ShellReload is the router → shell "reload your page" signal,
// used by --dev mode after a binary changes. The Reason field is
// informational only; the shell calls window.location.reload()
// regardless.
type ShellReload struct {
	T      string `json:"t"`
	Reason string `json:"reason,omitempty"`
}

func NewShellReload(reason string) ShellReload {
	return ShellReload{T: TShellReload, Reason: reason}
}

// ShellChannelBind: the router tells the shell that a new raw channel
// id has been opened for one of its windows. The shell then knows
// where to route incoming raw frames on that channel.
//
// Kind="bundle" signals a one-shot bundle delivery for InstanceID:
// the shell accumulates bytes until ChannelUnbind, then blob-URL-
// imports the result to instantiate the app's custom element.
type ShellChannelBind struct {
	T          string `json:"t"`
	ChannelID  uint32 `json:"channel_id"`
	WindowID   uint32 `json:"window_id"`
	Kind       string `json:"kind,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
}

func NewShellChannelBind(channelID, windowID uint32) ShellChannelBind {
	return ShellChannelBind{T: TShellChannelBind, ChannelID: channelID, WindowID: windowID}
}

func NewShellChannelBindBundle(channelID uint32, instanceID string) ShellChannelBind {
	return ShellChannelBind{
		T:          TShellChannelBind,
		ChannelID:  channelID,
		Kind:       ChannelKindBundle,
		InstanceID: instanceID,
	}
}

// ShellChannelCredit is the per-channel flow-control grant sent from
// the shell (browser FE) to the router. N is *additive*: each credit
// frame extends the channel's send budget by N bytes. The router
// tracks sent_bytes vs. granted_bytes and pauses dequeuing for a
// channel when the two meet; this credit frame replenishes that
// budget.
//
// Channels open with an implicit initial window — 64 KiB for raw,
// unlimited for JSON control. The FE replenishes as its renderer
// absorbs bytes; see docs/QOS.md §5.
type ShellChannelCredit struct {
	T         string `json:"t"`
	ChannelID uint32 `json:"ch"`
	N         uint32 `json:"n"`
}

func NewShellChannelCredit(channelID, n uint32) ShellChannelCredit {
	return ShellChannelCredit{T: TShellChannelCredit, ChannelID: channelID, N: n}
}

// ShellAppCrashed reports that an app BE process exited abnormally.
// "Abnormal" means non-zero exit code, or killed by a signal. The
// router emits this immediately before the window-delete patch that
// tearDown produces; the shell intercepts it, marks the window
// crashed in place (so the user keeps the geometry context), and
// suppresses the subsequent delete.
//
// Log is the tail of the BE's stdout+stderr — typically the panic
// trace from Go's default crash handler. Truncated at the router's
// per-instance ring-buffer cap.
type ShellAppCrashed struct {
	T          string `json:"t"`
	InstanceID string `json:"instance_id"`
	WindowID   uint32 `json:"window_id,omitempty"`
	AppID      string `json:"app_id"`
	ExitCode   int    `json:"exit_code"`
	Signal     string `json:"signal,omitempty"`
	Uptime     string `json:"uptime"`
	Log        string `json:"log"`
}

func NewShellAppCrashed(instanceID, appID string, windowID uint32, exitCode int, signal, uptime, log string) ShellAppCrashed {
	return ShellAppCrashed{
		T:          TShellAppCrashed,
		InstanceID: instanceID,
		WindowID:   windowID,
		AppID:      appID,
		ExitCode:   exitCode,
		Signal:     signal,
		Uptime:     uptime,
		Log:        log,
	}
}

// ShellChannelUnbind: the channel is gone.
type ShellChannelUnbind struct {
	T         string `json:"t"`
	ChannelID uint32 `json:"channel_id"`
	Reason    string `json:"reason,omitempty"`
}

func NewShellChannelUnbind(channelID uint32, reason string) ShellChannelUnbind {
	return ShellChannelUnbind{T: TShellChannelUnbind, ChannelID: channelID, Reason: reason}
}

// ShellAssetRead: shell asks router for a single asset from the
// router's embedded asset FS. ReqID correlates the response. Path is
// the request path as the shell would have written `fetch(path)` —
// resolved against the asset FS root, leading slash optional.
type ShellAssetRead struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	Path  string `json:"path"`
}

func NewShellAssetRead(reqID uint64, path string) ShellAssetRead {
	return ShellAssetRead{T: TShellAssetRead, ReqID: reqID, Path: path}
}

// ShellAssetReadOK: router accepts the pull. Bytes will stream on
// ChannelID (Kind=ChannelKindAsset) until a ChannelClose marks EOF.
// Size/Mime are advisory; shell can pre-allocate and dispatch.
type ShellAssetReadOK struct {
	T         string `json:"t"`
	ReqID     uint64 `json:"req_id"`
	ChannelID uint32 `json:"channel_id"`
	Size      int64  `json:"size"`
	Mime      string `json:"mime,omitempty"`
}

func NewShellAssetReadOK(reqID uint64, channelID uint32, size int64, mime string) ShellAssetReadOK {
	return ShellAssetReadOK{T: TShellAssetReadOK, ReqID: reqID, ChannelID: channelID, Size: size, Mime: mime}
}

// ShellAssetReadErr: router rejects the pull (typically ErrCodeNotFound
// or ErrCodeForbidden). No channel is opened.
type ShellAssetReadErr struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	Code  string `json:"code"`
	Msg   string `json:"msg"`
}

func NewShellAssetReadErr(reqID uint64, code, msg string) ShellAssetReadErr {
	return ShellAssetReadErr{T: TShellAssetReadErr, ReqID: reqID, Code: code, Msg: msg}
}
