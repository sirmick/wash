package router

import (
	"encoding/json"
	"fmt"

	"github.com/sirmick/wash/internal/wire"
)

// ProtocolVersion is the wash wire protocol version this router speaks.
// Apps declaring a different protocol_version are listed-disabled.
const ProtocolVersion = wire.ProtocolVersion

// Surface values.
const (
	SurfaceWindow  = wire.SurfaceWindow
	SurfaceDesktop = wire.SurfaceDesktop
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
)

// MaxIconBytes is the cap on the inline icon data URI per WIRE.md §5.1.
const MaxIconBytes = wire.MaxIconBytes

// Manifest / WindowHints alias the canonical wire definitions so
// router-internal code can keep using the bare names.
type (
	Manifest    = wire.Manifest
	WindowHints = wire.WindowHints
)

// ParseManifest parses one --wash-manifest probe output. The manifest
// is returned even when validation fails so the caller can list it
// disabled with the reason. Validation rules live in wire.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return &m, wire.ValidateManifest(&m)
}
