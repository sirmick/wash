package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

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

// idRegexp is the manifest id regex from WIRE.md §5.1.
var idRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)+$`)

// elementPrefix is the required prefix for custom-element tags so the
// global registry stays namespaced.
const elementPrefix = "wash-app-"

// validateManifest checks all v0.0 manifest constraints. Returning an
// error makes the registry mark the app listed-disabled (never silently
// drop), with err.Error() as the displayed reason.
func validateManifest(m *Manifest) error {
	if !idRegexp.MatchString(m.ID) {
		return fmt.Errorf("invalid id %q", m.ID)
	}
	if m.Name == "" {
		return errors.New("name is empty")
	}
	if m.Version == "" {
		return errors.New("version is empty")
	}
	if m.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("protocol_version %d incompatible (router speaks %d)", m.ProtocolVersion, ProtocolVersion)
	}
	if len(m.Element) <= len(elementPrefix) || m.Element[:len(elementPrefix)] != elementPrefix {
		return fmt.Errorf("element %q must start with %q", m.Element, elementPrefix)
	}
	switch m.Surface {
	case SurfaceWindow, SurfaceDesktop:
	default:
		return fmt.Errorf("invalid surface %q", m.Surface)
	}
	switch m.Instancing {
	case InstancingMulti, InstancingSingle, InstancingSingleton:
	default:
		return fmt.Errorf("invalid instancing %q", m.Instancing)
	}
	if m.Icon == "" {
		return errors.New("icon is empty (must be inline data URI)")
	}
	if len(m.Icon) > MaxIconBytes {
		return fmt.Errorf("icon is %d bytes, cap is %d", len(m.Icon), MaxIconBytes)
	}
	return nil
}

// ParseManifest parses one --wash-manifest probe output. The manifest
// is returned even when validation fails so the caller can list it
// disabled with the reason.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return &m, validateManifest(&m)
}
