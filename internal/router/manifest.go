package router

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/sirmick/wash/internal/wire"
)

// ProtocolVersion is the wash wire protocol version this router speaks.
// Apps declaring a different protocol_version are listed-disabled.
const ProtocolVersion = wire.ProtocolVersion

// Surface values.
const (
	SurfaceWindow     = wire.SurfaceWindow
	SurfaceDesktop    = wire.SurfaceDesktop
	SurfaceBackground = wire.SurfaceBackground
)

// Instancing values.
const (
	InstancingMulti     = wire.InstancingMulti
	InstancingSingle    = wire.InstancingSingle
	InstancingSingleton = wire.InstancingSingleton
)

// Capability strings recognized by v0.1.
const (
	CapSpawn        = wire.CapSpawn
	CapPrepareSpawn = wire.CapPrepareSpawn
	CapWindows      = wire.CapWindows
	CapEnvPublish   = wire.CapEnvPublish
)

// MaxIconBytes is the cap on the inline icon data URI per WIRE.md §5.1.
const MaxIconBytes = wire.MaxIconBytes

// Manifest / WindowHints alias the canonical wire definitions so
// router-internal code can keep using the bare names.
type (
	Manifest    = wire.Manifest
	WindowHints = wire.WindowHints
)

// ParseProbe parses one --wash-manifest probe output. Probe output
// is a ProbeOutput envelope: {manifest, bundle_b64?}. The manifest
// is returned even when validation fails so the caller can list it
// disabled with the reason. Bundle bytes are base64-decoded; an
// invalid base64 payload yields (manifest, nil, err) so the caller
// can list-disable the entry with a "bad bundle" reason.
func ParseProbe(data []byte) (*Manifest, []byte, error) {
	var p wire.ProbeOutput
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, nil, fmt.Errorf("parse: %w", err)
	}
	var bundle []byte
	if p.BundleB64 != "" {
		b, err := base64.StdEncoding.DecodeString(p.BundleB64)
		if err != nil {
			return &p.Manifest, nil, fmt.Errorf("bundle base64: %w", err)
		}
		bundle = b
	}
	return &p.Manifest, bundle, wire.ValidateManifest(&p.Manifest)
}
