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
	TShellCatalog       = "catalog"
	TShellAppDeclared   = "app.declared"
	TShellWindowCreate  = "window.create"
	TShellWindowDestroy = "window.destroy"
	TShellWindowTitle   = "window.title"
	TShellAssetDeliver  = "asset.deliver"

	// Shell → router.
	TShellAssetFetch         = "asset.fetch"
	TShellWindowCloseClicked = "window.close_clicked"
	TShellWindowFocus        = "window.focus"
	TShellWindowResize       = "window.resize"
	TShellAppMsgSend         = "app_msg.send"
	TShellLog                = "log"

	// Router → shell (BE → FE relay).
	TShellAppMsgDeliver = "app_msg.deliver"
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

// ShellWindowCreate asks the shell to create a floating window and
// mount the instance's element inside it. Not emitted for
// surface:"desktop"; the shell mounts at the root surface instead.
type ShellWindowCreate struct {
	T          string `json:"t"`
	WindowID   uint32 `json:"window_id"`
	InstanceID string `json:"instance_id"`
	Title      string `json:"title"`
	W          uint32 `json:"w"`
	H          uint32 `json:"h"`
}

func NewShellWindowCreate(windowID uint32, instanceID, title string, w, h uint32) ShellWindowCreate {
	return ShellWindowCreate{T: TShellWindowCreate, WindowID: windowID, InstanceID: instanceID, Title: title, W: w, H: h}
}

// ShellWindowDestroy tears down a window in the shell.
type ShellWindowDestroy struct {
	T        string `json:"t"`
	WindowID uint32 `json:"window_id"`
}

func NewShellWindowDestroy(windowID uint32) ShellWindowDestroy {
	return ShellWindowDestroy{T: TShellWindowDestroy, WindowID: windowID}
}

// ShellWindowTitle updates a window's titlebar text.
type ShellWindowTitle struct {
	T        string `json:"t"`
	WindowID uint32 `json:"window_id"`
	Title    string `json:"title"`
}

func NewShellWindowTitle(windowID uint32, title string) ShellWindowTitle {
	return ShellWindowTitle{T: TShellWindowTitle, WindowID: windowID, Title: title}
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
