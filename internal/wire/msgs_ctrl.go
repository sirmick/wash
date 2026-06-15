package wire

import (
	"encoding/json"
	"fmt"
)

// JSON message tags shared between app↔router channel 0 (WIRE.md §6, §7)
// and the WS shell channel 0 (WIRE.md §8). Tags are kept exact-string per
// the spec; do not rename without bumping protocol_version.
const (
	// Handshake (§6).
	TIdentity    = "identity"
	TIdentityAck = "identity.ack"

	// Errors (§13). Either side may send before closing.
	TError = "error"

	// Dynamic raw channels (app socket channel 0 + WS channel 0 share
	// these tags). An app initiates the open by sending ChannelOpen on
	// its channel 0; the router allocates a channel id, replies with
	// ChannelOpened, and tells the shell about the binding via
	// ShellChannelBind. Bytes then flow on the allocated channel id
	// (≥ 2 on app socket, ≥ 1 on WS) as bare-byte frames.
	TChannelOpen    = "channel.open"
	TChannelOpened  = "channel.opened"
	TChannelOpenErr = "channel.open.err"
	TChannelClose   = "channel.close"
	TChannelClosed  = "channel.closed"
)

// Error codes carried in Error.Code (§13).
const (
	ErrCodeProtoMismatch        = "proto_mismatch"
	ErrCodeBadIdentity          = "bad_identity"
	ErrCodeBadFrame             = "bad_frame"
	ErrCodeOversizeFrame        = "oversize_frame"
	ErrCodeBadManifest          = "bad_manifest"
	ErrCodeForbidden            = "forbidden"
	ErrCodeUnknownApp           = "unknown_app"
	ErrCodeInternal             = "internal"
	ErrCodeNotFound             = "not_found"
	ErrCodeIncompatibleProtocol = "incompatible_protocol"
	// Credit / flow-control error codes (docs/QOS.md §5).
	ErrCodeUnknownChannel = "unknown_channel"
	ErrCodeCreditOverflow = "credit_overflow"
	ErrCodeBadRequest     = "bad_request"
)

// Identity is the app's first frame on channel 0 after the wash
// socket opens (§6 step 1). PID lets the router check that the
// connecting process is actually the registered binary for AppID
// — auth at the protocol level rather than relying on inherited
// fds. It's intended to be os.Getpid() at the SDK; the router
// resolves /proc/<pid>/exe and compares against the registered
// binary path. Omitted on transports where the check doesn't
// apply (in-process test loopback).
//
// AttachToken is set when the process was forked by an external
// spawner (e.g. wash-priv launching an app under sudo) that called
// EvtSpawnRequest (with Prepare=true) ahead of time. The router matches by token in
// that case, not pid — sudo's fork/exec semantics make the
// dialing pid unreliable. Token-matched attaches still go through
// the /proc/<pid>/exe binary check before being accepted.
type Identity struct {
	T           string `json:"t"`
	AppID       string `json:"app_id"`
	Proto       int    `json:"proto"`
	Version     string `json:"version"`
	PID         int    `json:"pid,omitempty"`
	AttachToken string `json:"attach_token,omitempty"`
}

func NewIdentity(appID string, proto int, version string) Identity {
	return Identity{T: TIdentity, AppID: appID, Proto: proto, Version: version}
}

// NewIdentityWithPID is the variant the SDK uses to identify
// itself when dialing the control socket from outside the
// router's spawn loop. The router validates pid → registered
// binary.
func NewIdentityWithPID(appID string, proto int, version string, pid int) Identity {
	return Identity{T: TIdentity, AppID: appID, Proto: proto, Version: version, PID: pid}
}

// NewIdentityWithToken identifies the SDK to the router for an
// externally-spawned attach. Token must match a live pending-attach
// record minted via EvtSpawnRequest (with Prepare=true) on the spawner's connection.
func NewIdentityWithToken(appID string, proto int, version string, pid int, token string) Identity {
	return Identity{T: TIdentity, AppID: appID, Proto: proto, Version: version, PID: pid, AttachToken: token}
}

// Session is the bag of session-scoped facts the router ships once
// at handshake time. Fields are omitempty so old SDKs (or apps that
// don't care) see no JSON noise. Future additions (Locale, Timezone,
// Theme, …) drop in here as new optional fields; no protocol bump
// needed as long as we only add — never rename or remove.
type Session struct {
	// Root is the fs sandbox; "" means unconfined. Apps that
	// touch the filesystem (wash-fm, wash-fs) MUST treat every
	// path as relative to this prefix when it's non-empty.
	Root string `json:"root,omitempty"`

	// CloseGraceMs is how long an app has to reply to
	// window.close_requested before the router force-kills, in
	// milliseconds. Zero means "the router didn't tell you" —
	// apps that need to surface a deadline (an unsaved-changes
	// dialog with a countdown, say) should treat the absence as
	// "use the conservative default" rather than guessing.
	CloseGraceMs uint32 `json:"close_grace_ms,omitempty"`
}

// IdentityAck is the router's reply (§6 step 3). WindowID is omitted
// for surface:"desktop" apps — the element mounts as the root surface.
// Session carries router-supplied session facts; pointer + omitempty
// so old IdentityAck producers (tests, future minimal mocks) don't
// emit a noisy "session": {} on the wire.
type IdentityAck struct {
	T          string   `json:"t"`
	InstanceID string   `json:"instance_id"`
	WindowID   uint32   `json:"window_id,omitempty"`
	Session    *Session `json:"session,omitempty"`
}

func NewIdentityAck(instanceID string, windowID uint32) IdentityAck {
	return IdentityAck{T: TIdentityAck, InstanceID: instanceID, WindowID: windowID}
}

// Error is the generic protocol-level error on channel 0 (§13).
type Error struct {
	T    string `json:"t"`
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

func NewError(code, msg string) Error {
	return Error{T: TError, Code: code, Msg: msg}
}

// Channel "kinds" — extra hint on ChannelOpen so the router and
// shell can give certain channels special semantics. Empty is the
// default (a normal bidirectional byte pipe forwarded to the bound
// shell). "bundle" is an upload-only pipe: bytes are cached
// router-side and replayed to every shell that attaches.
const (
	ChannelKindGeneric = ""
	ChannelKindBundle  = "bundle"
	// "asset" is a one-shot, router-originated, read-only channel
	// streaming a single file from the router's embedded asset FS.
	// Opened in response to ShellAssetRead. ChannelClose marks EOF.
	ChannelKindAsset = "asset"
	// "video" carries an encoded per-window pixel stream (PNG/WebP
	// frames, or out-of-band-negotiated H.264/VP9). The hint tells the
	// shell to mount its display decoder component on the bound channel
	// rather than treating it as opaque bytes. See docs/DISPLAY.md §5.
	ChannelKindVideo = "video"
	// "peer" carries a remote host's entire shell wire, multiplexed over
	// the browser's single connection to A (docs/REMOTE.md). The router
	// splices this channel verbatim to an ssh -L'd socket reaching host B;
	// the shell feeds its bytes to a second RouterClient (origin = the bound
	// ShellChannelBind.Origin). Opaque to A — never decoded router-side.
	ChannelKindPeer = "peer"
)

// ChannelOpen — app → router — requests a new raw channel bound to
// the app's window. The router replies with ChannelOpened on success
// or ChannelOpenErr on failure; matching is via req_id.
type ChannelOpen struct {
	T        string `json:"t"`
	ReqID    uint64 `json:"req_id"`
	WindowID uint32 `json:"window_id"`
	Kind     string `json:"kind,omitempty"`
}

func NewChannelOpen(reqID uint64, windowID uint32) ChannelOpen {
	return ChannelOpen{T: TChannelOpen, ReqID: reqID, WindowID: windowID}
}

func NewChannelOpenKind(reqID uint64, windowID uint32, kind string) ChannelOpen {
	return ChannelOpen{T: TChannelOpen, ReqID: reqID, WindowID: windowID, Kind: kind}
}

// ChannelOpened — router → app — the channel is live. Raw bytes can
// now flow on channel id ChannelID until either side sends
// ChannelClose (or it dies underneath).
type ChannelOpened struct {
	T         string `json:"t"`
	ReqID     uint64 `json:"req_id"`
	ChannelID uint32 `json:"channel_id"`
}

func NewChannelOpened(reqID uint64, channelID uint32) ChannelOpened {
	return ChannelOpened{T: TChannelOpened, ReqID: reqID, ChannelID: channelID}
}

// ChannelOpenErr — router → app — the open failed; codes mirror §13.
type ChannelOpenErr struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	Code  string `json:"code"`
	Msg   string `json:"msg"`
}

func NewChannelOpenErr(reqID uint64, code, msg string) ChannelOpenErr {
	return ChannelOpenErr{T: TChannelOpenErr, ReqID: reqID, Code: code, Msg: msg}
}

// ChannelClose — either side — half-closes a channel. After sending,
// the sender MUST NOT write more bytes on that channel; the peer
// receives bytes already in flight, then ChannelClosed.
type ChannelClose struct {
	T         string `json:"t"`
	ChannelID uint32 `json:"channel_id"`
}

func NewChannelClose(channelID uint32) ChannelClose {
	return ChannelClose{T: TChannelClose, ChannelID: channelID}
}

// ChannelClosed — router → app (or peer) — confirms the channel is
// dead in both directions. Reason is best-effort human text.
type ChannelClosed struct {
	T         string `json:"t"`
	ChannelID uint32 `json:"channel_id"`
	Reason    string `json:"reason,omitempty"`
}

func NewChannelClosed(channelID uint32, reason string) ChannelClosed {
	return ChannelClosed{T: TChannelClosed, ChannelID: channelID, Reason: reason}
}

// PeekType returns the value of the "t" string field of a JSON
// object, without otherwise parsing it. Returns an empty string if
// the field is missing or not a string.
func PeekType(data []byte) (string, error) {
	var probe struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return "", err
	}
	return probe.T, nil
}

// DecodeCtrl parses a single JSON control message into the concrete
// type matching its t field. Used by both transports for channel 0
// — handshake, asset pull, errors, and the shell vocabulary.
//
// Unknown t values yield a wrapping error; callers may also call
// PeekType and dispatch themselves.
func DecodeCtrl(data []byte) (any, error) {
	t, err := PeekType(data)
	if err != nil {
		return nil, fmt.Errorf("ctrl decode: %w", err)
	}
	switch t {
	case TIdentity:
		var m Identity
		return m, json.Unmarshal(data, &m)
	case TIdentityAck:
		var m IdentityAck
		return m, json.Unmarshal(data, &m)
	case TError:
		var m Error
		return m, json.Unmarshal(data, &m)
	case TChannelOpen:
		var m ChannelOpen
		return m, json.Unmarshal(data, &m)
	case TChannelOpened:
		var m ChannelOpened
		return m, json.Unmarshal(data, &m)
	case TChannelOpenErr:
		var m ChannelOpenErr
		return m, json.Unmarshal(data, &m)
	case TChannelClose:
		var m ChannelClose
		return m, json.Unmarshal(data, &m)
	case TChannelClosed:
		var m ChannelClosed
		return m, json.Unmarshal(data, &m)
	// Shell-channel tags (§8) are dispatched alongside; they share
	// the same JSON channel 0 discipline.
	case TShellCatalog:
		var m ShellCatalog
		return m, json.Unmarshal(data, &m)
	case TShellAppDeclared:
		var m ShellAppDeclared
		return m, json.Unmarshal(data, &m)
	case TShellSessionSnapshot:
		var m ShellSessionSnapshot
		return m, json.Unmarshal(data, &m)
	case TShellSessionPatch:
		var m ShellSessionPatch
		return m, json.Unmarshal(data, &m)
	case TShellWindowCloseClicked:
		var m ShellWindowCloseClicked
		return m, json.Unmarshal(data, &m)
	case TShellWindowFocus:
		var m ShellWindowFocus
		return m, json.Unmarshal(data, &m)
	case TShellWindowMove:
		var m ShellWindowMove
		return m, json.Unmarshal(data, &m)
	case TShellWindowResize:
		var m ShellWindowResize
		return m, json.Unmarshal(data, &m)
	case TShellWindowState:
		var m ShellWindowState
		return m, json.Unmarshal(data, &m)
	case TShellAppMsgSend:
		var m ShellAppMsgSend
		return m, json.Unmarshal(data, &m)
	case TShellLaunch:
		var m ShellLaunch
		return m, json.Unmarshal(data, &m)
	case TShellPeerAttach:
		var m ShellPeerAttach
		return m, json.Unmarshal(data, &m)
	case TShellPeerDetach:
		var m ShellPeerDetach
		return m, json.Unmarshal(data, &m)
	case TShellPeerError:
		var m ShellPeerError
		return m, json.Unmarshal(data, &m)
	case TShellAppMsgDeliver:
		var m ShellAppMsgDeliver
		return m, json.Unmarshal(data, &m)
	case TShellNotify:
		var m ShellNotify
		return m, json.Unmarshal(data, &m)
	case TShellLog:
		var m ShellLog
		return m, json.Unmarshal(data, &m)
	case TShellReload:
		var m ShellReload
		return m, json.Unmarshal(data, &m)
	case TShellChannelBind:
		var m ShellChannelBind
		return m, json.Unmarshal(data, &m)
	case TShellChannelUnbind:
		var m ShellChannelUnbind
		return m, json.Unmarshal(data, &m)
	case TShellChannelCredit:
		var m ShellChannelCredit
		return m, json.Unmarshal(data, &m)
	case TShellClipboardSet:
		var m ShellClipboardSet
		return m, json.Unmarshal(data, &m)
	case TShellClipboardGet:
		var m ShellClipboardGet
		return m, json.Unmarshal(data, &m)
	case TShellClipboardData:
		var m ShellClipboardData
		return m, json.Unmarshal(data, &m)
	case TShellClipboardChanged:
		var m ShellClipboardChanged
		return m, json.Unmarshal(data, &m)
	case TShellAppCrashed:
		var m ShellAppCrashed
		return m, json.Unmarshal(data, &m)
	case TShellAssetRead:
		var m ShellAssetRead
		return m, json.Unmarshal(data, &m)
	case TShellAssetReadOK:
		var m ShellAssetReadOK
		return m, json.Unmarshal(data, &m)
	case TShellAssetReadErr:
		var m ShellAssetReadErr
		return m, json.Unmarshal(data, &m)
	case TShellPanelRead:
		var m ShellPanelRead
		return m, json.Unmarshal(data, &m)
	case TShellPanelReadOK:
		var m ShellPanelReadOK
		return m, json.Unmarshal(data, &m)
	case TShellPanelReadErr:
		var m ShellPanelReadErr
		return m, json.Unmarshal(data, &m)
	}
	return nil, fmt.Errorf("ctrl decode: unknown t %q", t)
}

// EncodeCtrl marshals any of the JSON control message types defined in
// this package, asserting the t field is set on the value.
func EncodeCtrl(v any) ([]byte, error) {
	return json.Marshal(v)
}
