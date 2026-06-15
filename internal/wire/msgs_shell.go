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
	// Shell → router. App-private message from a mounted element.
	// Routing depends on the optional To field:
	//   - Own BE: To unset; InstanceID identifies the sender; router
	//     relays to that instance's BE half.
	//   - Cross-app: To set with {AppID (singleton) | InstanceID
	//     (direct)}; router resolves and forwards as EvtAppMsg.
	TShellAppMsgSend = "app_msg.send"
	TShellLog        = "log"
	// Shell → router, "launch this app id." Used for remote-apps
	// (docs/REMOTE.md §6.1): host B runs --no-session, so there is no
	// session BE to route a launcher click through — the shell asks B's
	// router to spawn directly, the same path its control socket uses.
	// Fire-and-forget: success surfaces as the usual app.declared +
	// window; the router logs failures. Refuses desktop-surface apps
	// (only the autoboot session owns the desktop), mirroring controlLaunch.
	TShellLaunch = "shell.launch"
	// Shell → router, remote-apps relay (docs/REMOTE.md). peer.attach asks
	// A's router to dial the socket the supervisor registered for this
	// origin (host B), allocate a channel, and splice it verbatim to that
	// socket — so B's wire rides this one connection. The router replies
	// with a channel.bind{kind:"peer", origin}. peer.detach tears it down.
	TShellPeerAttach = "peer.attach"
	TShellPeerDetach = "peer.detach"
	// Router → shell: a peer.attach could not be satisfied (no registration
	// for the origin, or the dial failed). Surfaces what was otherwise a
	// silent "host up but no apps". Carries the origin + a reason.
	TShellPeerError = "peer.error"

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

	// Shell ↔ router clipboard. The same router-held clipboard the
	// per-app event channel reaches via §9 clipboard.* — these tags
	// expose it on the shell channel so app FRONTENDS can use it
	// through window.wash without a BE round-trip. Text-only on this
	// surface (JSON channel; []byte would base64) — mime tags the
	// payload for future richness but Text carries it.
	//
	// Shell → router.
	TShellClipboardSet = "clipboard.set"
	TShellClipboardGet = "clipboard.get"
	// Router → shell, clipboard.get reply.
	TShellClipboardData = "clipboard.data"
	// Router → shell, broadcast on every clipboard change (including
	// changes made by app BEs via the §9 path). Carries the content
	// so consumers (sidebar widget) don't need a follow-up get.
	TShellClipboardChanged = "clipboard.changed"

	// Shell → router, settings-panel bundle pull. Read the cached
	// panel.js bytes (Entry.PanelBundle) for an app that declared a
	// SettingsPanel. Mirrors asset.read but keyed by app id and served
	// from the app registry rather than the router's own asset FS, so
	// the shell can blob-import + customElements.define the panel
	// element without spawning the owning app.
	TShellPanelRead = "panel.read"
	// Router → shell, panel pull accepted. ChannelID streams the panel
	// bytes via Kind="bundle"; ChannelUnbind marks the end.
	TShellPanelReadOK = "panel.read.ok"
	// Router → shell, panel pull rejected (ErrCodeNotFound when the app
	// is absent or declares no panel).
	TShellPanelReadErr = "panel.read.err"
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
	Accent     string `json:"accent,omitempty"`
	Surface    string `json:"surface"`
	Instancing string `json:"instancing"`
	Disabled   bool   `json:"disabled,omitempty"`
	Reason     string `json:"reason,omitempty"`

	// RootVariant mirrors the same manifest field. When non-nil the
	// shell renders an additional synthetic catalog row "<Name>
	// (root)" that routes through wash-priv on click.
	RootVariant *RootVariant `json:"root_variant,omitempty"`
}

// ShellPanelDesc is one app-supplied settings panel the host can mount.
// Carried in the catalog (alongside, not within, Apps) because panels
// come mostly from surface=background services that are deliberately
// kept out of the launchable Apps list. The settings FE renders a
// section per descriptor and loads the element on demand via
// window.wash.loadSettingsPanel (panel.read).
type ShellPanelDesc struct {
	AppID   string `json:"app_id"`
	Section string `json:"section"`
	Element string `json:"element"`
	Icon    string `json:"icon,omitempty"`
	Order   int    `json:"order,omitempty"`
}

// ShellCatalog is the router's "here is what is launchable" snapshot.
// Sent once on shell connect; v0.1 will revisit live updates. Panels
// is the parallel list of app-supplied settings panels (see
// ShellPanelDesc).
type ShellCatalog struct {
	T      string            `json:"t"`
	Apps   []ShellCatalogApp `json:"apps"`
	Panels []ShellPanelDesc  `json:"panels,omitempty"`
}

func NewShellCatalog(apps []ShellCatalogApp, panels []ShellPanelDesc) ShellCatalog {
	return ShellCatalog{T: TShellCatalog, Apps: apps, Panels: panels}
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
	// Accent mirrors the source app's manifest accent so the shell
	// can tint the titlebar icon in the same brand color the
	// launcher uses. Empty for apps that didn't declare one (the
	// shell will derive a hash-based hue at render time).
	Accent  string `json:"accent,omitempty"`
	Title   string `json:"title"`
	X       int32  `json:"x"`
	Y       int32  `json:"y"`
	W       uint32 `json:"w"`
	H       uint32 `json:"h"`
	Z       uint32 `json:"z"`
	State   string `json:"state"` // normal | minimized | maximized
	Focused bool   `json:"focused"`
	// IsRoot is true when the owning app process runs as uid 0, or
	// when the app id is reserved as part of the privilege chain
	// (com.wash.priv). The shell's WM paints a red stripe and ROOT
	// label on the titlebar of such windows. The router fills this
	// from SO_PEERCRED at attach time and from the reserved-id
	// list — never from anything the app declares.
	IsRoot bool `json:"is_root,omitempty"`
	// Chromeless mirrors the app manifest's WindowHints.Chromeless: the
	// shell renders this window with no titlebar/border, letting the
	// guest surface (e.g. Webamp's Winamp UI) own its full chrome. The
	// app drives move/close via the window.wash API.
	Chromeless bool `json:"chromeless,omitempty"`
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

// ShellAppMsgSend is the unified FE → router app-private message.
// Data is intentionally opaque to the router; it carries whatever
// the app uses domain-side. Routing:
//
//   - Own BE: InstanceID identifies the sender (the mounted app);
//     To is nil; router relays to that instance's BE half.
//   - Cross-app: To is set; router resolves the recipient and
//     forwards as EvtAppMsg on the receiver's event channel. The FE
//     cannot attribute the sender — no router-side identity for
//     window-of-origin on the multiplexed shell channel — so the
//     receiver sees a bare EvtAppMsg without From.
type ShellAppMsgSend struct {
	T          string          `json:"t"`
	InstanceID string          `json:"instance_id,omitempty"`
	Data       json.RawMessage `json:"data"`
	To         *Recipient      `json:"to,omitempty"`
}

func NewShellAppMsgSend(instanceID string, data json.RawMessage) ShellAppMsgSend {
	return ShellAppMsgSend{T: TShellAppMsgSend, InstanceID: instanceID, Data: data}
}

// NewShellAppMsgSendTo is the cross-app variant — same wire type,
// To set, InstanceID unused.
func NewShellAppMsgSendTo(r Recipient, data json.RawMessage) ShellAppMsgSend {
	return ShellAppMsgSend{T: TShellAppMsgSend, Data: data, To: &r}
}

// ShellLaunch is the FE asking this shell's router to launch an app by
// id (docs/REMOTE.md §6.1). Sent over a remote host's RouterClient so
// wash-connect can launch on B without a session BE there.
type ShellLaunch struct {
	T     string `json:"t"`
	AppID string `json:"app_id"`
}

func NewShellLaunch(appID string) ShellLaunch {
	return ShellLaunch{T: TShellLaunch, AppID: appID}
}

// ShellPeerAttach / ShellPeerDetach drive the remote-apps relay
// (docs/REMOTE.md). Origin names the remote host (== the supervisor's
// registered origin). attach makes A dial the registered socket + splice a
// fresh "peer" channel to it; detach tears that channel + socket down.
type ShellPeerAttach struct {
	T      string `json:"t"`
	Origin string `json:"origin"`
}

func NewShellPeerAttach(origin string) ShellPeerAttach {
	return ShellPeerAttach{T: TShellPeerAttach, Origin: origin}
}

type ShellPeerDetach struct {
	T      string `json:"t"`
	Origin string `json:"origin"`
}

func NewShellPeerDetach(origin string) ShellPeerDetach {
	return ShellPeerDetach{T: TShellPeerDetach, Origin: origin}
}

// ShellPeerError — router → shell — a peer.attach failed (no registration /
// dial error). Lets the FE log it instead of silently showing a host with
// no apps.
type ShellPeerError struct {
	T      string `json:"t"`
	Origin string `json:"origin"`
	Msg    string `json:"msg"`
}

func NewShellPeerError(origin, msg string) ShellPeerError {
	return ShellPeerError{T: TShellPeerError, Origin: origin, Msg: msg}
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
	// Origin names the remote host whose wire a Kind="peer" channel
	// carries (docs/REMOTE.md). The shell routes this channel's bytes to
	// the matching origin's RouterClient. Empty for all other kinds.
	Origin string `json:"origin,omitempty"`
}

// NewShellChannelBind binds an app-opened raw channel to a window. The
// kind hint (e.g. ChannelKindVideo) lets the shell mount a decoder
// component for the channel instead of treating its bytes as opaque;
// pass ChannelKindGeneric ("") for a plain byte stream.
func NewShellChannelBind(channelID, windowID uint32, kind string) ShellChannelBind {
	return ShellChannelBind{T: TShellChannelBind, ChannelID: channelID, WindowID: windowID, Kind: kind}
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

// ShellClipboardSet writes text into the router-held clipboard on
// behalf of the browser shell (window.wash.clipboardSetText). The
// router broadcasts the change to every app BE and every OTHER shell.
type ShellClipboardSet struct {
	T    string `json:"t"`
	Mime string `json:"mime"`
	Text string `json:"text"`
}

func NewShellClipboardSet(mime, text string) ShellClipboardSet {
	return ShellClipboardSet{T: TShellClipboardSet, Mime: mime, Text: text}
}

// ShellClipboardGet requests the clipboard's current content; the
// router replies with ShellClipboardData carrying the same ReqID.
type ShellClipboardGet struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
}

func NewShellClipboardGet(reqID uint64) ShellClipboardGet {
	return ShellClipboardGet{T: TShellClipboardGet, ReqID: reqID}
}

// ShellClipboardData is the clipboard.get reply. Non-text content
// (future mimes) is surfaced as empty Text with the mime preserved.
type ShellClipboardData struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	Mime  string `json:"mime"`
	Text  string `json:"text"`
}

func NewShellClipboardData(reqID uint64, mime, text string) ShellClipboardData {
	return ShellClipboardData{T: TShellClipboardData, ReqID: reqID, Mime: mime, Text: text}
}

// ShellClipboardChanged announces a clipboard change to shells.
// Unlike the §9 event-channel notice (mime only), this carries the
// content: shells fan out to FE consumers (sidebar preview, paste
// handlers) that would otherwise each issue a get.
type ShellClipboardChanged struct {
	T    string `json:"t"`
	Mime string `json:"mime"`
	Text string `json:"text"`
}

func NewShellClipboardChanged(mime, text string) ShellClipboardChanged {
	return ShellClipboardChanged{T: TShellClipboardChanged, Mime: mime, Text: text}
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

// ShellPanelRead: shell asks the router for an app's settings-panel
// bundle (Entry.PanelBundle). ReqID correlates the response; AppID
// names the app whose panel.js to stream.
type ShellPanelRead struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	AppID string `json:"app_id"`
}

func NewShellPanelRead(reqID uint64, appID string) ShellPanelRead {
	return ShellPanelRead{T: TShellPanelRead, ReqID: reqID, AppID: appID}
}

// ShellPanelReadOK: router accepts the pull. Bytes stream on ChannelID
// (Kind=ChannelKindBundle) until a ChannelUnbind marks the end. Size
// is advisory.
type ShellPanelReadOK struct {
	T         string `json:"t"`
	ReqID     uint64 `json:"req_id"`
	ChannelID uint32 `json:"channel_id"`
	Size      int64  `json:"size"`
}

func NewShellPanelReadOK(reqID uint64, channelID uint32, size int64) ShellPanelReadOK {
	return ShellPanelReadOK{T: TShellPanelReadOK, ReqID: reqID, ChannelID: channelID, Size: size}
}

// ShellPanelReadErr: router rejects the pull (ErrCodeNotFound when the
// app is absent or declares no panel). No channel is opened.
type ShellPanelReadErr struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	Code  string `json:"code"`
	Msg   string `json:"msg"`
}

func NewShellPanelReadErr(reqID uint64, code, msg string) ShellPanelReadErr {
	return ShellPanelReadErr{T: TShellPanelReadErr, ReqID: reqID, Code: code, Msg: msg}
}
