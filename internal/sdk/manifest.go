package sdk

// ProtocolVersion is the wire protocol version this SDK speaks.
const ProtocolVersion = 1

// Surface and Instancing values mirror WIRE.md §5.1.
const (
	SurfaceWindow  = "window"
	SurfaceDesktop = "desktop"

	InstancingMulti     = "multi"
	InstancingSingle    = "single"
	InstancingSingleton = "singleton"

	CapSpawn        = "spawn"
	CapPrepareSpawn = "prepare_spawn"
)

// Manifest is the manifest schema from WIRE.md §5.1, kept here so
// apps can construct it as a Go literal without importing the router.
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

	// Hidden keeps the app out of the launcher catalog. It is still
	// spawnable (by --initial-app or by another app's
	// spawn.request).
	Hidden bool `json:"hidden,omitempty"`
}

// WindowHints mirrors router.WindowHints. v0.0 honors width/height.
type WindowHints struct {
	DefaultWidth  uint32 `json:"default_width,omitempty"`
	DefaultHeight uint32 `json:"default_height,omitempty"`
	MinWidth      uint32 `json:"min_width,omitempty"`
	MinHeight     uint32 `json:"min_height,omitempty"`
	Resizable     *bool  `json:"resizable,omitempty"`
}
