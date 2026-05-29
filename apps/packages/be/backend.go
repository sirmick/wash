package packages

import (
	"context"
)

// Package is the unit of search/install/upgrade as the FE renders
// it. Backends populate as much as their metadata system exposes;
// missing fields come back as zero strings / false.
//
// Kind distinguishes regular packages from collection-style units
// the same backend manages — apt's tasksel tasks, dnf's groups,
// possible future flatpak/snap entries. Default `""` means regular;
// the FE renders a small badge for non-empty kinds so the user
// knows what they're acting on. The backend's argv methods receive
// the kind so they can dispatch to the right CLI (tasksel vs
// apt-get, etc.).
type Package struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Summary   string `json:"summary"`
	Installed bool   `json:"installed"`
	Kind      string `json:"kind,omitempty"`
}

// Backend wraps one distribution's package manager (apt-get, dnf,
// apk, opkg, …). The five-method shape mirrors the user-facing
// operations the app supports: search is the only one that does
// real work in-process; the four action methods just describe a
// command that priv runs under PTY.
type Backend interface {
	// Name is the short identifier surfaced in the FE banner so a
	// user knows which backend they're driving.
	Name() string
	// Search returns matches for query. Empty query → empty result;
	// callers shouldn't list everything (cheap typo guard against
	// dumping 50k packages into the UI).
	Search(ctx context.Context, query string) ([]Package, error)
	// InstallArgv returns the command priv should run under sudo to
	// install pkg. kind is the Package.Kind ("" for regular, "task"
	// for tasksel, future "group" for dnf, …) so backends dispatch
	// to the right CLI without parsing the name. Returning nil
	// signals "operation not supported for this kind" — the caller
	// rejects the action with a user-visible error.
	InstallArgv(pkg, kind string) []string
	// RemoveArgv mirrors InstallArgv for removal.
	RemoveArgv(pkg, kind string) []string
	// UpgradePkgArgv mirrors InstallArgv for a single-package upgrade.
	// Returns nil for kinds with no per-unit upgrade concept (tasksel
	// tasks don't have a "upgrade the task" command — packages inside
	// upgrade individually via the regular per-package path).
	UpgradePkgArgv(pkg, kind string) []string

	// GlobalActions enumerates backend-wide operations the FE
	// surfaces in an "Actions" dropdown — refresh-the-index,
	// upgrade-everything, autoremove-orphans, clean-cache, …
	// The list is ordered (FE renders in returned order). Items
	// can carry a Danger hint for destructive ops (autoremove
	// uninstalls things; that gets the red treatment).
	GlobalActions() []GlobalAction
	// GlobalActionArgv returns the argv for the action with the
	// given id (matching one of GlobalActions()'s IDs). Returns
	// nil for unknown ids — the caller reports a user error.
	GlobalActionArgv(id string) []string
}

// GlobalAction describes one backend-wide action. ID is the stable
// identifier the FE round-trips back via run_action; Label / Desc
// are what the user sees. Surface determines where the FE renders
// it — "toolbar" for the prominent one-click action, "menu" (or
// empty) for the granular catalog in the Actions menu.
type GlobalAction struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Desc    string `json:"desc"`
	Danger  bool   `json:"danger,omitempty"`
	Surface string `json:"surface,omitempty"` // "toolbar" | "menu" (default)
}

// Detect probes each supported backend in order and returns the
// first one whose underlying binaries are present. Returns nil if
// none of apt/dnf/apk are installed; the FE renders a
// "no supported package manager" state.
//
// Only one of the three is installed on any given distro, so the
// probe order is for readability not precedence: a debian box wins
// on apt, fedora on dnf, alpine on apk. If a future hybrid box
// genuinely has two, the first-match-wins ordering matches what
// most users running that distro expect.
func Detect() Backend {
	if b := newAPT(); b != nil {
		return b
	}
	if b := newDNF(); b != nil {
		return b
	}
	if b := newAPK(); b != nil {
		return b
	}
	return nil
}
