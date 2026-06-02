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
	BackendNetplan  = "netplan"  // Ubuntu (and netplan-on-Debian) authority layer
	BackendIfupdown = "ifupdown" // classic Debian /etc/network/interfaces
	BackendFake     = "fake"
)

// detections is the read-only probe result for each real backend, gathered once
// at startup and fed to chooseBackend. (Kept as a struct, not a map, so the
// decision logic is explicit and exhaustively unit-tested.) Field order mirrors
// the auto precedence.
type detections struct {
	netplan  backend.Detection
	nm       backend.Detection
	networkd backend.Detection
	ifupdown backend.Detection
}

// chooseBackend is the pure backend-selection decision (docs/NET-BACKENDS.md),
// unit-tested exhaustively. `mode` is the operator's choice (env override or
// persisted setting); only "auto" consults the probes.
//
// Posture: wash *takes over* the box's authoritative config layer (like any
// desktop environment owns networking) — the chosen backend writes that layer
// directly, with commit-confirm auto-revert as the safety net. The auto policy
// just picks WHICH layer, and that ordering is layered, not flat:
//
//   - netplan is the AUTHORITY layer: it renders *to* NM or networkd, so on a
//     netplan-driven box wash must own /etc/netplan — writing NM keyfiles or
//     networkd units directly would be clobbered on the next `netplan apply`.
//     So an active netplan wins even when NM/networkd also report active; they
//     are netplan's render targets, not independent managers.
//   - Otherwise the active manager wins (NM, then networkd, then ifupdown).
//   - Then the available-but-idle fallbacks, same order.
//
// Returns the backend name and a short human reason.
func chooseBackend(mode string, d detections) (string, string) {
	switch mode {
	case BackendNM, BackendNetworkd, BackendNetplan, BackendIfupdown, BackendFake:
		return mode, "forced (WASH_NETD_BACKEND=" + mode + ")"
	case BackendAuto:
		switch {
		// Active tier — netplan first (it's the layer above nm/networkd).
		case d.netplan.Active:
			return BackendNetplan, "auto: netplan is the active config authority"
		case d.nm.Active:
			return BackendNM, "auto: NetworkManager is the active manager"
		case d.networkd.Active:
			return BackendNetworkd, "auto: systemd-networkd is the active manager"
		case d.ifupdown.Active:
			return BackendIfupdown, "auto: ifupdown (/etc/network/interfaces) is the active manager"
		// Available tier — installed but not the live manager.
		case d.netplan.Available:
			return BackendNetplan, "auto: netplan available"
		case d.nm.Available:
			return BackendNM, "auto: NetworkManager available"
		case d.networkd.Available:
			return BackendNetworkd, "auto: systemd-networkd available"
		case d.ifupdown.Available:
			return BackendIfupdown, "auto: ifupdown available"
		default:
			return BackendFake, "auto: no live backend detected"
		}
	default:
		return BackendFake, fmt.Sprintf("unknown backend %q — using fake", mode)
	}
}
