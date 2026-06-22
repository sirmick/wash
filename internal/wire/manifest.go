package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// ProtocolVersion is the wash wire protocol version this build speaks.
// Router and SDK share it. Apps declaring a different value in their
// manifest are listed-disabled by the registry.
const ProtocolVersion = 1

// ProbeHeader is the first line of `<binary> --wash-manifest` stdout.
// Probe output is framed, not a single JSON blob:
//
//	<header-json>\n
//	<raw bytes of bundle[0]><raw bytes of bundle[1]>...
//
// so the FE bundle(s) ride along as raw bytes — no base64. The router
// caches them at probe time; there is no post-handshake bundle-upload
// step. json.Marshal never emits a bare newline, so the first '\n'
// reliably delimits the header from the raw payload that follows.
//
// Bundles is empty for apps with no FE (background services, CLI
// helpers). The router treats "manifest with no bundle" as a usable
// entry; the shell-side mount path surfaces a recognizable error if a
// window mount is attempted anyway.
type ProbeHeader struct {
	Manifest Manifest      `json:"manifest"`
	Bundles  []BundleFrame `json:"bundles,omitempty"`
}

// BundleFrame describes one raw bundle blob in the probe payload, in
// the order the blobs are concatenated after the header newline.
type BundleFrame struct {
	Kind string `json:"kind"` // BundleMain | BundlePanel
	Len  int    `json:"len"`
}

// Bundle kinds carried in the probe payload.
const (
	// BundleMain is the app's FE bundle (index.js). Absent for
	// background services and CLI helpers with no window.
	BundleMain = "main"
	// BundlePanel is the app's settings-panel bundle (panel.js),
	// present when the manifest declares a SettingsPanel.
	BundlePanel = "panel"
)

// NamedBundle pairs a bundle kind with its raw bytes for WriteProbe.
type NamedBundle struct {
	Kind  string
	Bytes []byte
}

// WriteProbe writes framed probe output to w: the header JSON line
// (manifest + frame descriptors) followed by each bundle's raw bytes
// in slice order. Zero-length or nil bundles are skipped entirely, so
// an app with no FE emits just "header\n".
func WriteProbe(w io.Writer, m Manifest, bundles []NamedBundle) error {
	hdr := ProbeHeader{Manifest: m}
	for _, b := range bundles {
		if len(b.Bytes) == 0 {
			continue
		}
		hdr.Bundles = append(hdr.Bundles, BundleFrame{Kind: b.Kind, Len: len(b.Bytes)})
	}
	line, err := json.Marshal(hdr)
	if err != nil {
		return err
	}
	if _, err := w.Write(line); err != nil {
		return err
	}
	if _, err := w.Write([]byte{'\n'}); err != nil {
		return err
	}
	for _, b := range bundles {
		if len(b.Bytes) == 0 {
			continue
		}
		if _, err := w.Write(b.Bytes); err != nil {
			return err
		}
	}
	return nil
}

// ReadProbe parses framed probe output: the header line plus the raw
// bundle blobs that follow, returned as a kind→bytes map (kinds are
// unique). A truncated payload (fewer bytes than the header declares)
// is an error so a botched build is surfaced, not silently mounted.
// The returned slices are copies, independent of data.
func ReadProbe(data []byte) (ProbeHeader, map[string][]byte, error) {
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		return ProbeHeader{}, nil, errors.New("probe: no header newline")
	}
	var hdr ProbeHeader
	if err := json.Unmarshal(data[:nl], &hdr); err != nil {
		return ProbeHeader{}, nil, fmt.Errorf("probe header: %w", err)
	}
	rest := data[nl+1:]
	out := make(map[string][]byte, len(hdr.Bundles))
	off := 0
	for _, f := range hdr.Bundles {
		if f.Len < 0 || off+f.Len > len(rest) {
			return hdr, nil, fmt.Errorf("probe: bundle %q truncated (want %d at off %d, have %d)", f.Kind, f.Len, off, len(rest)-off)
		}
		b := make([]byte, f.Len)
		copy(b, rest[off:off+f.Len])
		out[f.Kind] = b
		off += f.Len
	}
	return hdr, out, nil
}

// Surface values (WIRE.md §5.1).
const (
	SurfaceWindow  = "window"
	SurfaceDesktop = "desktop"
	// SurfaceBackground is a service-tier app: no window, no
	// launcher entry, no FE bundle. Autoboot on first shell
	// connect; other apps consume it via cross-app app_msg (the
	// sdk.StateService subscribe-with-snapshot pattern is the
	// canonical contract). wash-bulk, wash-priv, and com.wash.notify
	// are the v1 background services; future audio mixer / clipboard
	// daemon / network agent take the same slot.
	SurfaceBackground = "background"
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

	// CapWindows lets an app instance own more than one window via the
	// window.create/destroy events (docs/DISPLAY.md §4). Without it an
	// instance has exactly the one window the router mints at
	// identity.ack. Gated because unbounded window creation is a
	// chrome-DoS vector; wash-display is the canonical holder.
	CapWindows = "windows"

	// CapEnvPublish lets an app publish WASH_*-namespaced env hints that
	// the router merges into every subsequently-spawned app's
	// environment (env.publish). Used by wash-display to propagate the
	// compositor's DISPLAY / WAYLAND_DISPLAY. Privileged — it influences
	// other apps' environments — so it's a distinct capability and the
	// router additionally allowlists keys. See docs/DISPLAY_ENV.md.
	CapEnvPublish = "env-publish"

	// CapRestart lets an app ask the router to cycle a background
	// singleton service (the app.restart event, docs/SETTINGS.md §5):
	// terminate the running instance, GC its windows/channels, and
	// spawn a fresh one. Gated because cycling system services is a
	// privileged action — the settings app is the canonical holder.
	// Restricted router-side to surface=background targets.
	CapRestart = "restart"

	// CapOpen lets an app ask the router to open a file path in whichever
	// app registered for that extension (the open.request event). Narrower
	// than CapSpawn: the caller can't pick the target app, only hand off a
	// file to its declared handler. The file manager is the canonical holder.
	CapOpen = "open"
)

// MaxIconBytes is the cap on the inline icon data URI per WIRE.md §5.1.
const MaxIconBytes = 64 * 1024

// Manifest is the parsed shape printed by --wash-manifest (WIRE.md §5.1).
//
// Lives in wire so router and SDK share one definition. Adding a
// field here updates every consumer in lockstep.
type Manifest struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Version         string `json:"version"`
	ProtocolVersion int    `json:"protocol_version"`
	Element         string `json:"element"`
	Surface         string `json:"surface"`
	Icon            string `json:"icon"`
	// Accent is an optional brand color (any CSS color string,
	// typically a #rrggbb hex). The shell tints the app's launcher
	// icon with this. Apps that leave it empty get a deterministic
	// hue derived from the app id — no row stays monochrome.
	Accent       string       `json:"accent,omitempty"`
	Instancing   string       `json:"instancing"`
	Capabilities []string     `json:"capabilities"`
	Window       *WindowHints `json:"window,omitempty"`

	// Opens declares the file extensions this app handles for the
	// open.request flow (wash's mime-association): lowercase, dot-prefixed
	// (".png", ".md"). The router builds an extension→app index from every
	// manifest's Opens; opening a path launches its handler with
	// `--open <path>`. First registered handler for an extension wins.
	Opens []string `json:"opens,omitempty"`

	// SettingsPanel, when set, declares a control panel this app
	// supplies to the settings host. The host discovers it via
	// panels.list, fetches the panel bundle (panel.js, shipped raw in
	// the probe payload as BundlePanel) over a raw channel, and mounts
	// Element in a settings section — so an app owns its settings UI
	// instead of the settings app hardcoding it (docs/SETTINGS.md).
	SettingsPanel *SettingsPanel `json:"settings_panel,omitempty"`

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
	// Icon override (lucide sprite symbol name). When empty the launcher
	// reuses the source app's own icon (session rootEntryFor:
	// `src.root_variant.icon ?? src.icon`).
	Icon string `json:"icon,omitempty"`
	// Args appended to the child process argv (e.g. ["--login"] for
	// wash-term so root's shell sources profile/bashrc).
	Args []string `json:"args,omitempty"`
}

// SettingsPanel is the manifest-side descriptor for an app-supplied
// settings panel. The panel bundle (panel.js) defines the custom
// element named by Element; the settings host mounts it under the
// named Section and bridges its messages to the app over the svc.*
// relay.
type SettingsPanel struct {
	// Section is the nav label for the panel (e.g. "Display").
	Section string `json:"section"`
	// Element is the custom-element tag the panel bundle defines
	// (e.g. "wash-settings-panel-display"). Must start with the
	// settings-panel prefix so the tag namespace stays distinct from
	// app window elements.
	Element string `json:"element"`
	// Icon is an optional lucide symbol name for the nav entry.
	Icon string `json:"icon,omitempty"`
	// Order is an optional ascending sort hint among supplied panels
	// (default 0; ties break on Section name).
	Order int `json:"order,omitempty"`
}

// WindowHints carries the optional default window geometry. v0.1
// honors default width/height only (WIRE.md §5.1).
type WindowHints struct {
	DefaultWidth  uint32 `json:"default_width,omitempty"`
	DefaultHeight uint32 `json:"default_height,omitempty"`
	MinWidth      uint32 `json:"min_width,omitempty"`
	MinHeight     uint32 `json:"min_height,omitempty"`
	Resizable     *bool  `json:"resizable,omitempty"`
	// Chromeless drops the wash window frame (titlebar + border) so the
	// guest surface fills the whole window and draws its own chrome.
	// Used by apps that ship a pixel-locked native UI — wash-washamp
	// embeds Webamp, which paints the classic Winamp titlebar itself;
	// a wash titlebar on top would double it. The shell still owns the
	// window (z-order, focus, taskbar); the app drives move/close via
	// the window.wash API. Implies non-resizable.
	Chromeless bool `json:"chromeless,omitempty"`
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

// idRegexp is the manifest id regex from WIRE.md §5.1.
var idRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)+$`)

// elementPrefix is the required prefix for custom-element tags so the
// global registry stays namespaced.
const elementPrefix = "wash-app-"

// settingsPanelPrefix namespaces app-supplied settings-panel elements
// distinctly from app window elements (elementPrefix).
const settingsPanelPrefix = "wash-settings-panel-"

// ValidateManifest checks all manifest constraints from WIRE.md §5.1.
// Returning an error makes the router mark the app listed-disabled
// (never silently dropped), with err.Error() as the displayed reason.
// Lives in wire (not router) so the in-process app registry can call
// it without a dep on router — same validation, same error messages,
// regardless of whether the manifest came from --wash-manifest output
// or a compiled-in registration.
func ValidateManifest(m *Manifest) error {
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
	// Element + Icon are required for surfaces that ever paint; a
	// background service has no FE, so we accept an empty element/icon
	// for it. The router's catalog filter keeps background entries out
	// of the launcher either way, so an icon would never be rendered.
	if m.Surface != SurfaceBackground {
		if len(m.Element) <= len(elementPrefix) || m.Element[:len(elementPrefix)] != elementPrefix {
			return fmt.Errorf("element %q must start with %q", m.Element, elementPrefix)
		}
	}
	switch m.Surface {
	case SurfaceWindow, SurfaceDesktop, SurfaceBackground:
	default:
		return fmt.Errorf("invalid surface %q", m.Surface)
	}
	switch m.Instancing {
	case InstancingMulti, InstancingSingle, InstancingSingleton:
	default:
		return fmt.Errorf("invalid instancing %q", m.Instancing)
	}
	if m.Surface != SurfaceBackground {
		if m.Icon == "" {
			return errors.New("icon is empty (must be inline data URI)")
		}
		if len(m.Icon) > MaxIconBytes {
			return fmt.Errorf("icon is %d bytes, cap is %d", len(m.Icon), MaxIconBytes)
		}
	}
	if sp := m.SettingsPanel; sp != nil {
		if sp.Section == "" {
			return errors.New("settings_panel.section is empty")
		}
		if !strings.HasPrefix(sp.Element, settingsPanelPrefix) || len(sp.Element) <= len(settingsPanelPrefix) {
			return fmt.Errorf("settings_panel.element %q must start with %q", sp.Element, settingsPanelPrefix)
		}
	}
	return nil
}
