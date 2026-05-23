package wire

// ProtocolVersion is the wash wire protocol version this build speaks.
// Router and SDK share it. Apps declaring a different value in their
// manifest are listed-disabled by the registry.
const ProtocolVersion = 1

// Surface values (WIRE.md §5.1).
const (
	SurfaceWindow  = "window"
	SurfaceDesktop = "desktop"
)

// Instancing values (WIRE.md §5.1, extended).
const (
	// InstancingMulti — independent process per spawn (default).
	InstancingMulti = "multi"
	// InstancingSingle — one process serves many windows. Semantics
	// still deferred in the wire; reserved for future use.
	InstancingSingle = "single"
	// InstancingSingleton — at most one running instance globally,
	// addressable by app_id as a sentinel. Spawn requests for an
	// already-running singleton return its existing instance instead
	// of starting a second one. Suited for system-service apps
	// (wash-bulk, wash-priv) where having two would be either
	// pointless or actively wrong.
	InstancingSingleton = "singleton"
)

// Capability strings recognized by v0.1.
const (
	CapSpawn = "spawn"

	// CapPrepareSpawn lets an app ask the router to mint a
	// pending-attach record (instance_id + attach_token) for an
	// app_id, after which the calling app is responsible for the
	// fork+exec itself (e.g. wash-priv launching a registered binary
	// under sudo). Distinct from CapSpawn because the trust grant
	// is bigger: the spawner controls how the binary is launched,
	// including its uid.
	CapPrepareSpawn = "prepare_spawn"
)

// MaxIconBytes is the cap on the inline icon data URI per WIRE.md §5.1.
const MaxIconBytes = 64 * 1024

// Manifest is the parsed shape printed by --wash-manifest (WIRE.md §5.1).
//
// Lives in wire so router and SDK share one definition. Adding a
// field here updates every consumer in lockstep.
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

	// RootVariant declares that this app has a meaningful "run as
	// root" variant. When set, the launcher shows an additional
	// synthetic catalog row alongside the normal one; clicking it
	// routes through wash-priv (queue + approval + sudo + spawn).
	// The app itself doesn't need any awareness of being launched
	// privileged — it just inherits uid 0 at start.
	//
	// Apps that declare this MUST be safe to run as root: no
	// assumption that $HOME is the invoking user's, no per-uid
	// state files written in places only root can recover.
	RootVariant *RootVariant `json:"root_variant,omitempty"`
}

// RootVariant is the manifest-side hint for the launcher's synthetic
// "run as root" entry. All fields are optional.
type RootVariant struct {
	// Name override for the launcher row. Default: "<Name> (root)".
	Name string `json:"name,omitempty"`
	// Icon override (lucide sprite symbol name). Default: shield-alert.
	Icon string `json:"icon,omitempty"`
	// Args appended to the child process argv (e.g. ["--login"] for
	// wash-term so root's shell sources profile/bashrc).
	Args []string `json:"args,omitempty"`
}

// WindowHints carries the optional default window geometry. v0.1
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
