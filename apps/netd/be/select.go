package netd

import (
	"fmt"

	"github.com/sirmick/wash/internal/washnet/backend"
)

// Backend names netd understands as a WASH_NETD_BACKEND value / persisted
// selection. "auto" probes; the rest force a specific backend.
const (
	BackendAuto     = "auto"
	BackendNM       = "nm"
	BackendNetworkd = "networkd"
	BackendFake     = "fake"
)

// detections is the read-only probe result for each real backend, gathered once
// at startup and fed to chooseBackend. (Kept as a struct, not a map, so the
// decision logic is explicit and exhaustively unit-tested.)
type detections struct {
	nm       backend.Detection
	networkd backend.Detection
}

// chooseBackend is the pure backend-selection decision (docs/NET.md §2.7),
// unit-tested exhaustively. `mode` is the operator's choice (env override or
// persisted setting); only "auto" consults the probes. The auto policy encodes
// posture: a *running* NetworkManager means the box is NM-managed, so coexist
// (don't disturb a working desktop) — NM wins even when networkd is also present.
// A box must explicitly opt into own-it (e.g. the Fedora image masks NM) for
// networkd to be chosen. Returns the backend name and a short human reason.
func chooseBackend(mode string, d detections) (string, string) {
	switch mode {
	case BackendNM, BackendNetworkd, BackendFake:
		return mode, "forced (WASH_NETD_BACKEND=" + mode + ")"
	case BackendAuto:
		switch {
		case d.nm.Active:
			return BackendNM, "auto: NetworkManager is the active manager"
		case d.networkd.Active:
			return BackendNetworkd, "auto: systemd-networkd is the active manager"
		case d.nm.Available:
			return BackendNM, "auto: NetworkManager available"
		case d.networkd.Available:
			return BackendNetworkd, "auto: systemd-networkd available"
		default:
			return BackendFake, "auto: no live backend detected"
		}
	default:
		return BackendFake, fmt.Sprintf("unknown backend %q — using fake", mode)
	}
}
