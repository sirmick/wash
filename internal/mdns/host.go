package mdns

// Host self-description for advertising THIS box on the wash service. Both
// entry points that represent "an invitation to connect" — a standalone
// wash-router (via the com.wash.remote supervisor) and the multi-user
// wash-login front door — advertise the same host identity, so the builder
// lives here next to the wire code rather than in either caller. A future
// standalone discovery daemon would reuse this verbatim.
//
// Two advertisers for the same host on one machine (login + an active
// session's router) is harmless: mDNS is built for multiple responders
// (we open the socket SO_REUSEPORT for exactly this), the records are
// byte-identical, and a browsing peer dedups them by instance. So there
// is deliberately no cross-process coordination here.

import (
	"net"
	"os"
	"strings"
)

// HostServiceInfo describes this box for the wash mDNS service, or returns
// nil if this box should not advertise. port is the SSH port a peer
// connects to (callers pass the conventional 22). It returns nil when:
//
//   - WASH_DISCOVERY_NO_ADVERTISE is set (the explicit opt-out — a box that
//     should browse but never be browsed), or
//   - the hostname is unknown, or
//   - there is no routable IPv4 to announce (container/link-local only),
//     since a peer has nothing to connect to.
//
// The nil-when-no-address behaviour means a caller can unconditionally pass
// the result to New(Options{Advertise: ...}); a nil Advertise simply browses
// (or, for an advertise-only caller, does nothing) instead of announcing a
// box no peer can reach.
func HostServiceInfo(port int) *ServiceInfo {
	if os.Getenv("WASH_DISCOVERY_NO_ADVERTISE") != "" {
		return nil
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return nil
	}
	host = strings.TrimSuffix(host, ".local")
	ips := routableIPv4()
	if len(ips) == 0 {
		return nil
	}
	return &ServiceInfo{
		Instance: host,
		Port:     port,
		Text:     []string{"wash=1"},
		IPv4:     ips,
	}
}

// routableIPv4 returns this box's non-loopback, non-link-local IPv4
// addresses on real interfaces — the ones a peer could SSH to. Mirrors the
// "routable v4" filter AND the docker-plumbing exclusion in
// apps/session/be/netifaces.go (kept separate there: single consumer).
// Skipping docker bridges matters: their 172.17.0.1/172.18.0.1 addresses are
// unreachable from a peer and every docker host shares them, so advertising
// them would hand out bogus connect targets.
func routableIPv4() []net.IP {
	all, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, ifc := range all {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || dockerIface(ifc.Name) {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := ipn.IP.To4()
			if v4 == nil || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, v4)
		}
	}
	return out
}

// dockerIface matches container plumbing — the default bridge, veth pairs,
// and docker's per-network bridges (br-<12 hex>). Mirrors the same-named
// helper in apps/session/be/netifaces.go; wash's own bridges (br0/br1) and
// vlans (eth0.10) are intentionally NOT matched.
func dockerIface(name string) bool {
	switch {
	case name == "docker0", strings.HasPrefix(name, "veth"):
		return true
	case strings.HasPrefix(name, "br-") && len(name) == 15: // "br-" + 12 hex
		return true
	default:
		return false
	}
}
