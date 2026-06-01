package networkdprofile

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/sirmick/wash/internal/washnet/model"
)

// ParseUnits decompiles a set of networkd unit files (keyed by filename) back
// into a model.Config — the inverse of RenderUnits, and the read path the UI
// uses to load the box's current settings. Parse∘Render and Render∘Parse are
// asserted identity over the corpus (networkd_test.go): the fidelity guarantee
// that makes "load current networkd state, edit it, write it back" lossless.
// ndev is a parsed .netdev: its Kind plus the raw sections (the wireguard case
// needs [WireGuard]/[WireGuardPeer] beyond the kind).
type ndev struct {
	kind string
	secs []iniSection
}

func ParseUnits(units map[string]string) (model.Config, error) {
	var c model.Config

	netdevs := map[string]ndev{}

	type pnet struct {
		owner string
		link  string
		secs  []iniSection
	}
	var nets []pnet

	for _, fn := range sortedKeys(units) {
		secs := parseUnitINI(units[fn])
		switch {
		case strings.HasSuffix(fn, ".netdev"):
			name := firstVal(secs, "NetDev", "Name")
			netdevs[name] = ndev{kind: firstVal(secs, "NetDev", "Kind"), secs: secs}
		case strings.HasSuffix(fn, ".network"):
			nets = append(nets, pnet{owner: stemNoPrio(fn, ".network"), link: firstVal(secs, "Match", "Name"), secs: secs})
		default:
			return c, fmt.Errorf("unit %q: unknown type (want .netdev/.network)", fn)
		}
	}

	// Membership is expressed in the .network files (Bridge=, VLAN=), not the
	// .netdev — scan them first so the device records can be assembled whole.
	ports := map[string][]string{}    // bridge dev → member links
	parentLink := map[string]string{} // vlan dev → parent link
	for _, n := range nets {
		for _, b := range allVals(n.secs, "Network", "Bridge") {
			ports[b] = append(ports[b], n.link)
		}
		for _, v := range allVals(n.secs, "Network", "VLAN") {
			parentLink[v] = n.link
		}
	}

	for _, name := range sortedNetdevNames(netdevs) {
		d := netdevs[name]
		switch d.kind {
		case "bridge":
			sort.Strings(ports[name])
			c.Devices = append(c.Devices, model.Device{Name: name, Type: "bridge", Ports: ports[name]})
		case "vlan":
			vid, _ := strconv.Atoi(firstVal(d.secs, "VLAN", "Id"))
			c.Devices = append(c.Devices, model.Device{Name: name, Type: "8021q", Ifname: parentLink[name], VID: vid})
		case "wireguard":
			// emitted as an Interface below (needs its .network's Addresses)
		default:
			return c, fmt.Errorf("netdev %q: kind %q not supported by the networkd backend", name, d.kind)
		}
	}

	for _, n := range nets {
		bridge := firstVal(n.secs, "Network", "Bridge")
		vlans := allVals(n.secs, "Network", "VLAN")
		hasAddr := firstVal(n.secs, "Network", "DHCP") != "" ||
			len(allVals(n.secs, "Network", "Address")) > 0 ||
			firstVal(n.secs, "Network", "LinkLocalAddressing") == "no"
		if !hasAddr && (bridge != "" || len(vlans) > 0) {
			continue // pure stub (bridge port / vlan parent) — not an interface
		}
		if nd, ok := netdevs[n.link]; ok && nd.kind == "wireguard" {
			iface, peers := buildWireGuard(n.owner, deviceFor(n.owner, n.link), n.secs, nd.secs)
			c.Interfaces = append(c.Interfaces, iface)
			c.WGPeers = append(c.WGPeers, peers...)
			continue
		}
		proto, err := parseProto(n.secs)
		if err != nil {
			return c, fmt.Errorf("network %q: %w", n.owner, err)
		}
		c.Interfaces = append(c.Interfaces, model.Interface{Name: n.owner, Device: deviceFor(n.owner, n.link), Proto: proto})
	}
	return c, nil
}

// deviceFor returns the explicit device for an interface, or "" when the link
// name equals the interface name (the renderer derives the link from the name
// then — e.g. a wireguard interface whose link IS the netdev).
func deviceFor(owner, link string) string {
	if owner == link {
		return ""
	}
	return link
}

// parseProto maps a link's [Network] addressing back to the proto union. Both
// families share one section, so a static link's v4/v6 addresses are sorted by
// family into the model fields.
func parseProto(secs []iniSection) (model.ProtoConfig, error) {
	if dhcp := firstVal(secs, "Network", "DHCP"); dhcp != "" && dhcp != "no" {
		p := model.DHCPProto{}
		switch dhcp {
		case "ipv4":
			p.IPv4 = true
		case "ipv6":
			p.IPv6 = true
		case "yes":
			// both — leave both flags false (bare = both), matching dhcpValue
		default:
			return nil, fmt.Errorf("unsupported DHCP=%q", dhcp)
		}
		p.Hostname = firstVal(secs, "DHCPv4", "Hostname")
		return p, nil
	}

	addrs := allVals(secs, "Network", "Address")
	if len(addrs) > 0 {
		var sp model.StaticProto
		for _, a := range addrs {
			pfx, err := netip.ParsePrefix(a)
			if err != nil {
				return nil, fmt.Errorf("address %q: %w", a, err)
			}
			if pfx.Addr().Is4() {
				sp.IPAddr = pfx
			} else {
				sp.IP6Addr = pfx
			}
		}
		for _, g := range allVals(secs, "Network", "Gateway") {
			ga, err := netip.ParseAddr(g)
			if err != nil {
				return nil, fmt.Errorf("gateway %q: %w", g, err)
			}
			if ga.Is4() {
				sp.Gateway = ga
			} else {
				sp.IP6Gw = ga
			}
		}
		for _, d := range allVals(secs, "Network", "DNS") {
			da, err := netip.ParseAddr(d)
			if err != nil {
				return nil, fmt.Errorf("dns %q: %w", d, err)
			}
			sp.DNS = append(sp.DNS, da)
		}
		return sp, nil
	}

	if firstVal(secs, "Network", "LinkLocalAddressing") == "no" {
		return model.NoneProto{}, nil
	}
	return model.NoneProto{}, nil
}

// buildWireGuard reassembles a wireguard Interface from its .network (tunnel
// addresses) and its .netdev (private key / port / peers).
func buildWireGuard(owner, device string, netSecs, devSecs []iniSection) (model.Interface, []model.WGPeer) {
	wg := model.WireGuardProto{PrivateKey: firstVal(devSecs, "WireGuard", "PrivateKey")}
	if lp := firstVal(devSecs, "WireGuard", "ListenPort"); lp != "" {
		wg.ListenPort, _ = strconv.Atoi(lp)
	}
	for _, a := range allVals(netSecs, "Network", "Address") {
		if pfx, err := netip.ParsePrefix(a); err == nil {
			wg.Addresses = append(wg.Addresses, pfx)
		}
	}
	iface := model.Interface{Name: owner, Device: device, Proto: wg}

	var peers []model.WGPeer
	for _, sec := range devSecs {
		if sec.name != "WireGuardPeer" {
			continue
		}
		p := model.WGPeer{Interface: owner}
		for _, kv := range sec.kv {
			switch kv[0] {
			case "PublicKey":
				p.PublicKey = kv[1]
			case "AllowedIPs":
				p.AllowedIPs = parsePrefixList(kv[1])
			case "Endpoint":
				p.EndpointHost, p.EndpointPort = parseEndpoint(kv[1])
			case "PresharedKey":
				p.PresharedKey = kv[1]
			case "PersistentKeepalive":
				p.PersistentKeepalive, _ = strconv.Atoi(kv[1])
			}
		}
		peers = append(peers, p)
	}
	sort.Slice(peers, func(a, b int) bool { return peers[a].PublicKey < peers[b].PublicKey })
	return iface, peers
}

func sortedNetdevNames(m map[string]ndev) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
