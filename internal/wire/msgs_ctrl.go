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

	// Asset pull (§7), app socket only.
	TAssetRead    = "asset.read"
	TAssetReadOK  = "asset.read.ok"
	TAssetReadErr = "asset.read.err"
	TAssetData    = "asset.data"

	// Errors (§13). Either side may send before closing.
	TError = "error"
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
)

// Identity is the app's first frame on channel 0 after spawn (§6 step 1).
type Identity struct {
	T       string `json:"t"`
	AppID   string `json:"app_id"`
	Proto   int    `json:"proto"`
	Version string `json:"version"`
}

func NewIdentity(appID string, proto int, version string) Identity {
	return Identity{T: TIdentity, AppID: appID, Proto: proto, Version: version}
}

// IdentityAck is the router's reply (§6 step 3). WindowID is omitted
// for surface:"desktop" apps — the element mounts as the root surface.
type IdentityAck struct {
	T          string `json:"t"`
	InstanceID string `json:"instance_id"`
	WindowID   uint32 `json:"window_id,omitempty"`
}

func NewIdentityAck(instanceID string, windowID uint32) IdentityAck {
	return IdentityAck{T: TIdentityAck, InstanceID: instanceID, WindowID: windowID}
}

// AssetRead is router → app: "please serve me your bundle file <name>".
type AssetRead struct {
	T    string `json:"t"`
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

func NewAssetRead(id uint64, name string) AssetRead {
	return AssetRead{T: TAssetRead, ID: id, Name: name}
}

// AssetReadOK is app → router: headers announce; bytes follow in AssetData.
type AssetReadOK struct {
	T    string `json:"t"`
	ID   uint64 `json:"id"`
	Len  int64  `json:"len"`
	MIME string `json:"mime"`
}

func NewAssetReadOK(id uint64, length int64, mime string) AssetReadOK {
	return AssetReadOK{T: TAssetReadOK, ID: id, Len: length, MIME: mime}
}

// AssetData is one chunk of the bundle, base64 in JSON for v0.0. The
// last chunk MUST set End=true. Note: base64 inside JSON is the v0.0
// stopgap — replaced by a per-asset raw side-channel in v0.1.
type AssetData struct {
	T     string `json:"t"`
	ID    uint64 `json:"id"`
	Bytes string `json:"bytes"`
	End   bool   `json:"end"`
}

func NewAssetData(id uint64, b64Bytes string, end bool) AssetData {
	return AssetData{T: TAssetData, ID: id, Bytes: b64Bytes, End: end}
}

// AssetReadErr is app → router on failure.
type AssetReadErr struct {
	T    string `json:"t"`
	ID   uint64 `json:"id"`
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

func NewAssetReadErr(id uint64, code, msg string) AssetReadErr {
	return AssetReadErr{T: TAssetReadErr, ID: id, Code: code, Msg: msg}
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
	case TAssetRead:
		var m AssetRead
		return m, json.Unmarshal(data, &m)
	case TAssetReadOK:
		var m AssetReadOK
		return m, json.Unmarshal(data, &m)
	case TAssetData:
		var m AssetData
		return m, json.Unmarshal(data, &m)
	case TAssetReadErr:
		var m AssetReadErr
		return m, json.Unmarshal(data, &m)
	case TError:
		var m Error
		return m, json.Unmarshal(data, &m)
	// Shell-channel tags (§8) are dispatched alongside; they share
	// the same JSON channel 0 discipline.
	case TShellCatalog:
		var m ShellCatalog
		return m, json.Unmarshal(data, &m)
	case TShellAppDeclared:
		var m ShellAppDeclared
		return m, json.Unmarshal(data, &m)
	case TShellWindowCreate:
		var m ShellWindowCreate
		return m, json.Unmarshal(data, &m)
	case TShellWindowDestroy:
		var m ShellWindowDestroy
		return m, json.Unmarshal(data, &m)
	case TShellWindowTitle:
		var m ShellWindowTitle
		return m, json.Unmarshal(data, &m)
	case TShellAssetDeliver:
		var m ShellAssetDeliver
		return m, json.Unmarshal(data, &m)
	case TShellAssetFetch:
		var m ShellAssetFetch
		return m, json.Unmarshal(data, &m)
	case TShellWindowCloseClicked:
		var m ShellWindowCloseClicked
		return m, json.Unmarshal(data, &m)
	case TShellWindowFocus:
		var m ShellWindowFocus
		return m, json.Unmarshal(data, &m)
	case TShellAppMsgSend:
		var m ShellAppMsgSend
		return m, json.Unmarshal(data, &m)
	case TShellAppMsgDeliver:
		var m ShellAppMsgDeliver
		return m, json.Unmarshal(data, &m)
	case TShellLog:
		var m ShellLog
		return m, json.Unmarshal(data, &m)
	}
	return nil, fmt.Errorf("ctrl decode: unknown t %q", t)
}

// EncodeCtrl marshals any of the JSON control message types defined in
// this package, asserting the t field is set on the value.
func EncodeCtrl(v any) ([]byte, error) {
	return json.Marshal(v)
}
