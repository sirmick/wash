// host.ifaces producer — companion to host.stats (hoststats.go).
//
// The Network sidebar panel wants the box's live addressing at a glance: the IP
// of every interface a user cares about. We piggyback on the host-stats ticker
// (same 5s cadence, same Conn) rather than spinning a second loop, and push a
// separate `host.ifaces` app_msg the sidebar's NetWidget renders.
//
// "Cares about" = up, with at least one routable address, and neither loopback
// nor docker plumbing. wash's own bridges/vlans (br0, eth0.10) are kept; the
// container bridges (docker0, veth*, br-<hash>) are filtered, mirroring
// wash-top's real-vs-virtual split (apps/top/be/proc.go realNetIface).

package session

import (
	"log"
	"net"
	"strings"

	"github.com/sirmick/wash/pkg/sdk"
)

// netIface is one non-local, non-docker interface and its addresses.
type netIface struct {
	Name string   `json:"name"`
	IPs  []string `json:"ips"`
}

// pushNetIfaces enumerates the interesting interfaces and ships them to the FE.
func pushNetIfaces(c *sdk.Conn) {
	ifaces := readNetIfaces()
	if ifaces == nil {
		ifaces = []netIface{}
	}
	if err := c.SendAppMsg(map[string]any{"kind": "host.ifaces", "interfaces": ifaces}); err != nil {
		log.Printf("wash-session host.ifaces: send: %v", err)
	}
}

// readNetIfaces returns each up, non-loopback, non-docker interface that has at
// least one routable IP, with its IPv4 + global IPv6 addresses.
func readNetIfaces() []netIface {
	all, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netIface
	for _, ifc := range all {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 || dockerIface(ifc.Name) {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		var ips []string
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP
			// Skip the noise: loopback, link-local (fe80::/169.254), and the
			// unspecified address. Keep routable v4 + global v6.
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
				continue
			}
			ips = append(ips, ip.String())
		}
		if len(ips) == 0 {
			continue
		}
		out = append(out, netIface{Name: ifc.Name, IPs: ips})
	}
	return out
}

// dockerIface matches container plumbing: the default bridge, veth pairs, and
// docker's per-network bridges (br-<12 hex>). wash's bridges (br0/br1) and vlans
// (eth0.10) are NOT matched, so they still surface.
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
