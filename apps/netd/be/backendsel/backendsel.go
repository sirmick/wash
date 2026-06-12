// Package backendsel is the one place wash decides which networking backend to
// use and builds it. Both the netd service and the washnet-* CLIs select
// backends through here, so the autodetect precedence and the name→applier
// mapping never diverge (docs/NET-BACKENDS.md §2).
package backendsel

import (
	"github.com/sirmick/wash/apps/netd/be/ifupdown"
	"github.com/sirmick/wash/apps/netd/be/netplan"
	"github.com/sirmick/wash/apps/netd/be/networkd"
	"github.com/sirmick/wash/apps/netd/be/nm"
	"github.com/sirmick/wash/apps/netd/be/uci"
	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/model"
)

// Backend names (a WASH_NETD_BACKEND value / persisted selection). "auto" probes.
const (
	Auto     = "auto"
	NM       = "nm"
	Networkd = "networkd"
	Netplan  = "netplan"  // Ubuntu (and netplan-on-Debian) authority layer
	Ifupdown = "ifupdown" // classic Debian /etc/network/interfaces
	UCI      = "uci"      // OpenWRT — the type-1/router backend (whole gateway)
	Fake     = "fake"
)

// Applier is the live-box backend seam: the apply contract plus Live (the
// confirmed base to diff against) and Devices (managed NICs for the UI).
type Applier interface {
	backend.Applier
	Live() model.Config
	Devices() []string
}

// Detections is the read-only probe result per backend, in auto-precedence order.
type Detections struct {
	UCI      backend.Detection
	Netplan  backend.Detection
	NM       backend.Detection
	Networkd backend.Detection
	Ifupdown backend.Detection
}

// Probe runs every backend's Detect() (read-only).
func Probe() Detections {
	return Detections{
		UCI:      uci.Detect(),
		Netplan:  netplan.Detect(),
		NM:       nm.Detect(),
		Networkd: networkd.Detect(),
		Ifupdown: ifupdown.Detect(),
	}
}

// Choose is the pure backend-selection decision. mode is the operator's choice
// (env override / persisted setting); only "auto" consults the probes. The auto
// policy is layered: netplan is the authority layer (it renders TO nm/networkd),
// so an active netplan wins even over an active NM/networkd; otherwise the active
// manager wins (NM, networkd, ifupdown), then the available-but-idle fallbacks.
func Choose(mode string, d Detections) (string, string) {
	switch mode {
	case NM, Networkd, Netplan, Ifupdown, UCI, Fake:
		return mode, "forced (WASH_NETD_BACKEND=" + mode + ")"
	case Auto:
		switch {
		case d.UCI.Active:
			// OpenWRT is unambiguous — none of NM/networkd/netplan/ifupdown run
			// there — so an active UCI box takes precedence outright.
			return UCI, "auto: OpenWRT/UCI is the active config authority"
		case d.Netplan.Active:
			return Netplan, "auto: netplan is the active config authority"
		case d.NM.Active:
			return NM, "auto: NetworkManager is the active manager"
		case d.Networkd.Active:
			return Networkd, "auto: systemd-networkd is the active manager"
		case d.Ifupdown.Active:
			return Ifupdown, "auto: ifupdown (/etc/network/interfaces) is the active manager"
		case d.UCI.Available:
			return UCI, "auto: uci available"
		case d.Netplan.Available:
			return Netplan, "auto: netplan available"
		case d.NM.Available:
			return NM, "auto: NetworkManager available"
		case d.Networkd.Available:
			return Networkd, "auto: systemd-networkd available"
		case d.Ifupdown.Available:
			return Ifupdown, "auto: ifupdown available"
		default:
			return Fake, "auto: no live backend detected"
		}
	default:
		return Fake, "unknown backend " + mode + " — using fake"
	}
}

// New builds the live applier for a backend name. Returns nil for "fake"/unknown
// (the fake applier lives in netd, which is the only fake consumer).
func New(name string) Applier {
	switch name {
	case NM:
		return nm.NewApplier()
	case Networkd:
		return networkd.NewApplier()
	case Netplan:
		return netplan.NewApplier()
	case Ifupdown:
		return ifupdown.NewApplier()
	case UCI:
		return uci.NewApplier()
	}
	return nil
}

// Available lists the backends Detect() found usable here.
func Available(d Detections) []string {
	var out []string
	if d.UCI.Available {
		out = append(out, UCI)
	}
	if d.Netplan.Available {
		out = append(out, Netplan)
	}
	if d.NM.Available {
		out = append(out, NM)
	}
	if d.Networkd.Available {
		out = append(out, Networkd)
	}
	if d.Ifupdown.Available {
		out = append(out, Ifupdown)
	}
	return out
}

// Autodetect probes and chooses in one call — the CLIs' "just use my box" path.
func Autodetect() (string, string) { return Choose(Auto, Probe()) }
