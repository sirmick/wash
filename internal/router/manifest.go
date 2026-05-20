package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// ProtocolVersion is the wash wire protocol version this router speaks.
// Apps declaring a different protocol_version are listed-disabled.
const ProtocolVersion = 1

// Surface values (WIRE.md §5.1).
const (
	SurfaceWindow  = "window"
	SurfaceDesktop = "desktop"
)

// Instancing values (WIRE.md §5.1).
const (
	InstancingMulti  = "multi"
	InstancingSingle = "single"
)

// Capability strings recognized by v0.0.
const (
	CapSpawn = "spawn"
)

// MaxIconBytes is the cap on the inline icon data URI per WIRE.md §5.1.
const MaxIconBytes = 64 * 1024

// idRegexp is the manifest id regex from WIRE.md §5.1.
var idRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)+$`)

// elementPrefix is the required prefix for custom-element tags so the
// global registry stays namespaced.
const elementPrefix = "wash-app-"

// Manifest is the parsed shape printed by --wash-manifest (WIRE.md §5.1).
type Manifest struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Version         string       `json:"version"`
	ProtocolVersion int          `json:"protocol_version"`
	Element         string       `json:"element"`
	Surface         string       `json:"surface"`
	Icon            string       `json:"icon"`
	Instancing      string       `json:"instancing"`
	Capabilities    []string     `json:"capabilities"`
	Window          *WindowHints `json:"window,omitempty"`

	// Hidden keeps the app out of the launcher catalog. The app is
	// still spawnable (by --initial-app, or by another app's
	// spawn.request) — useful for test/utility apps.
	Hidden bool `json:"hidden,omitempty"`
}

// WindowHints carries the optional default window geometry. v0.0
// honors default width/height only (WIRE.md §5.1).
type WindowHints struct {
	DefaultWidth  uint32 `json:"default_width,omitempty"`
	DefaultHeight uint32 `json:"default_height,omitempty"`
	MinWidth      uint32 `json:"min_width,omitempty"`
	MinHeight     uint32 `json:"min_height,omitempty"`
	Resizable     *bool  `json:"resizable,omitempty"`
}

// HasCapability reports whether the manifest declares cap.
func (m *Manifest) HasCapability(cap string) bool {
	for _, c := range m.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// Validate checks all v0.0 manifest constraints. Returning an error
// makes the registry mark the app listed-disabled (never silently
// drop), with err.Error() as the displayed reason.
func (m *Manifest) Validate() error {
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
	case InstancingMulti, InstancingSingle:
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
// is returned even when Validate fails so the caller can list it
// disabled with the reason.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return &m, m.Validate()
}
