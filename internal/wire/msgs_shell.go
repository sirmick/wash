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
	TShellCatalog          = "catalog"
	TShellAppDeclared      = "app.declared"
	TShellSessionSnapshot  = "session.snapshot"
	TShellSessionPatch     = "session.patch"
	TShellAssetDeliver     = "asset.deliver"

	// Shell → router.
	TShellAssetFetch         = "asset.fetch"
	TShellWindowCloseClicked = "window.close_clicked"
	TShellWindowFocus        = "window.focus"
	TShellWindowMove         = "window.move"
	TShellWindowResize       = "window.resize"
	TShellWindowState        = "window.state"
	TShellAppMsgSend         = "app_msg.send"
	TShellLog                = "log"

	// Router → shell (BE → FE relay).
	TShellAppMsgDeliver = "app_msg.deliver"

	// Router → shell, relayed notification.
	TShellNotify = "notify"

	// Router → shell, raw channel bound / unbound. The shell uses
	// these to route incoming raw frames to the matching window's
	// element and to know when a channel id is dead.
	TShellChannelBind   = "channel.bind"
	TShellChannelUnbind = "channel.unbind"
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
	Title      string `json:"title"`
	X          int32  `json:"x"`
	Y          int32  `json:"y"`
	W          uint32 `json:"w"`
	H          uint32 `json:"h"`
	Z          uint32 `json:"z"`
	State      string `json:"state"` // normal | minimized | maximized
	Focused    bool   `json:"focused"`
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

// ShellAssetDeliver carries a bundle chunk back to the shell. Mirrors
// AssetData on the app side; the router glues the two streams.
type ShellAssetDeliver struct {
	T          string `json:"t"`
	InstanceID string `json:"instance_id"`
	Name       string `json:"name"`
	Bytes      string `json:"bytes"` // base64
	End        bool   `json:"end"`
	MIME       string `json:"mime,omitempty"`
}

func NewShellAssetDeliver(instanceID, name, b64Bytes string, end bool, mime string) ShellAssetDeliver {
	return ShellAssetDeliver{T: TShellAssetDeliver, InstanceID: instanceID, Name: name, Bytes: b64Bytes, End: end, MIME: mime}
}

// ShellAssetFetch is the shell asking for an instance's bundle file.
// The router translates this into a channel 0 AssetRead on the owning
// app's socket.
type ShellAssetFetch struct {
	T          string `json:"t"`
	InstanceID string `json:"instance_id"`
	Name       string `json:"name"`
}

func NewShellAssetFetch(instanceID, name string) ShellAssetFetch {
	return ShellAssetFetch{T: TShellAssetFetch, InstanceID: instanceID, Name: name}
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

// ShellChannelBind: the router tells the shell that a new raw channel
// id has been opened for one of its windows. The shell then knows
// where to route incoming raw frames on that channel.
type ShellChannelBind struct {
	T         string `json:"t"`
	ChannelID uint32 `json:"channel_id"`
	WindowID  uint32 `json:"window_id"`
}

func NewShellChannelBind(channelID, windowID uint32) ShellChannelBind {
	return ShellChannelBind{T: TShellChannelBind, ChannelID: channelID, WindowID: windowID}
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
