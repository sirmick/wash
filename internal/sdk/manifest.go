package sdk

import "github.com/sirmick/wash/internal/wire"

// ProtocolVersion is the wire protocol version this SDK speaks.
const ProtocolVersion = wire.ProtocolVersion

// Surface and Instancing values mirror WIRE.md §5.1.
const (
	SurfaceWindow     = wire.SurfaceWindow
	SurfaceDesktop    = wire.SurfaceDesktop
	SurfaceBackground = wire.SurfaceBackground

	InstancingMulti     = wire.InstancingMulti
	InstancingSingle    = wire.InstancingSingle
	InstancingSingleton = wire.InstancingSingleton

	CapSpawn        = wire.CapSpawn
	CapPrepareSpawn = wire.CapPrepareSpawn
	CapWindows      = wire.CapWindows
	CapEnvPublish   = wire.CapEnvPublish
	CapRestart      = wire.CapRestart
)

// Manifest / WindowHints / RootVariant alias the canonical wire
// definitions so apps can keep writing sdk.Manifest{...} literals.
type (
	Manifest    = wire.Manifest
	WindowHints = wire.WindowHints
	RootVariant = wire.RootVariant
)
