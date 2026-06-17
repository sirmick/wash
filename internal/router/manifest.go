package router

import (
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
	CapRestart      = wire.CapRestart
	CapOpen         = wire.CapOpen
)

// MaxIconBytes is the cap on the inline icon data URI per WIRE.md §5.1.
const MaxIconBytes = wire.MaxIconBytes

// Manifest / WindowHints alias the canonical wire definitions so
// router-internal code can keep using the bare names.
type (
	Manifest    = wire.Manifest
	WindowHints = wire.WindowHints
)

// ParseProbe parses one --wash-manifest probe output. Probe output is
// framed (wire.ReadProbe): a header JSON line followed by raw bundle
// bytes — no base64. Returns the manifest plus the main FE bundle and
// the optional settings-panel bundle. The manifest is returned even
// when validation fails so the caller can list it disabled with the
// reason; a malformed/truncated frame payload yields (manifest, nil,
// nil, err) so the caller can list-disable with a "bad bundle" reason.
func ParseProbe(data []byte) (*Manifest, []byte, []byte, error) {
	hdr, bundles, err := wire.ReadProbe(data)
	if err != nil {
		// hdr.Manifest is zero on a header-parse failure; still return
		// it so the caller has a (possibly empty) manifest to disable.
		return &hdr.Manifest, nil, nil, fmt.Errorf("parse: %w", err)
	}
	return &hdr.Manifest, bundles[wire.BundleMain], bundles[wire.BundlePanel], wire.ValidateManifest(&hdr.Manifest)
}
