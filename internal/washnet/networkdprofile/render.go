package networkdprofile

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/sirmick/wash/internal/washnet/model"
)

// netfile accumulates everything that lands in one link's .network file. A link
// is keyed by its Match Name; an Interface owns it (and supplies addressing),
// and/or it's a bridge port (Bridge=) and/or a vlan parent (VLAN=). Merging by
// link means an interface that is also a vlan parent shares one file, exactly as
// networkd requires (one .network per link).
type netfile struct {
	link   string            // [Match] Name
	owner  string            // owning Interface.Name, or "" for a pure stub
	proto  model.ProtoConfig // owner's addressing, nil for a stub
	bridge string            // master, if this link is a bridge port
	vlans  []string          // vlan devices anchored at this link (parent stub)
}

// RenderUnits compiles a Config into systemd-networkd unit files, keyed by
// filename. Devices (bridge/vlan/wireguard) become .netdev files; each link's
// addressing/membership becomes a .network file.
func RenderUnits(c model.Config) (map[string]string, error) {
	out := map[string]string{}
	nets := map[string]*netfile{}
	at := func(link string) *netfile {
		nf := nets[link]
		if nf == nil {
			nf = &netfile{link: link}
			nets[link] = nf
		}
		return nf
	}

	for _, d := range c.Devices {
		switch d.Type {
		case "bridge":
			out[netdevFile(d.Name)] = renderBridgeNetdev(d)
			for _, p := range d.Ports {
				at(p).bridge = d.Name
			}
		case "8021q":
			out[netdevFile(d.Name)] = renderVLANNetdev(d)
			parent := at(d.Ifname)
			parent.vlans = append(parent.vlans, d.Name)
		default:
			return nil, fmt.Errorf("device %q: type %q not supported by the networkd backend", d.Name, d.Type)
		}
	}

	for _, iface := range c.Interfaces {
		if wg, ok := iface.Proto.(model.WireGuardProto); ok {
			out[netdevFile(iface.Name)] = renderWGNetdev(iface, wg, c.WGPeers)
			nf := at(iface.Name) // the wg link == the netdev name
			nf.owner, nf.proto = iface.Name, iface.Proto
			continue
		}
		link := iface.Device
		if link == "" {
			link = iface.Name
		}
		nf := at(link)
		nf.owner, nf.proto = iface.Name, iface.Proto
	}

	for _, nf := range nets {
		text, err := renderNetwork(nf)
		if err != nil {
			return nil, err
		}
		key := nf.owner
		if key == "" {
			key = nf.link
		}
		out[networkFile(key)] = text
	}
	return out, nil
}

func renderBridgeNetdev(d model.Device) string {
	var u unit
	u.section("NetDev")
	u.kv("Name", d.Name)
	u.kv("Kind", "bridge")
	return u.String()
}

func renderVLANNetdev(d model.Device) string {
	var u unit
	u.section("NetDev")
	u.kv("Name", d.Name)
	u.kv("Kind", "vlan")
	u.section("VLAN")
	u.kv("Id", strconv.Itoa(d.VID))
	return u.String()
}

// renderWGNetdev emits a wireguard .netdev: the [WireGuard] private key/port and
// one [WireGuardPeer] section per peer on this interface (sorted by key for
// stable output). The tunnel addresses live in the sibling .network.
func renderWGNetdev(iface model.Interface, wg model.WireGuardProto, allPeers []model.WGPeer) string {
	var u unit
	u.section("NetDev")
	u.kv("Name", iface.Name)
	u.kv("Kind", "wireguard")

	u.section("WireGuard")
	if wg.PrivateKey != "" {
		u.kv("PrivateKey", wg.PrivateKey)
	}
	if wg.ListenPort != 0 {
		u.kv("ListenPort", strconv.Itoa(wg.ListenPort))
	}

	var peers []model.WGPeer
	for _, p := range allPeers {
		if p.Interface == iface.Name {
			peers = append(peers, p)
		}
	}
	sort.Slice(peers, func(a, b int) bool { return peers[a].PublicKey < peers[b].PublicKey })
	for _, p := range peers {
		u.section("WireGuardPeer")
		u.kv("PublicKey", p.PublicKey)
		if len(p.AllowedIPs) > 0 {
			u.kv("AllowedIPs", joinPrefixes(p.AllowedIPs))
		}
		if p.EndpointHost != "" {
			u.kv("Endpoint", endpoint(p.EndpointHost, p.EndpointPort))
		}
		if p.PresharedKey != "" {
			u.kv("PresharedKey", p.PresharedKey)
		}
		if p.PersistentKeepalive != 0 {
			u.kv("PersistentKeepalive", strconv.Itoa(p.PersistentKeepalive))
		}
	}
	return u.String()
}

// renderNetwork emits a link's .network: the [Match] plus a [Network] carrying
// addressing (from the owner's proto union, both families in one section — no
// per-family split like NM) and any Bridge=/VLAN= membership. A DHCP hostname
// goes under [DHCPv4].
func renderNetwork(nf *netfile) (string, error) {
	var u unit
	u.section("Match")
	u.kv("Name", nf.link)
	u.section("Network")
	switch p := nf.proto.(type) {
	case model.StaticProto:
		if p.IPAddr.IsValid() {
			u.kv("Address", p.IPAddr.String())
		}
		if p.IP6Addr.IsValid() {
			u.kv("Address", p.IP6Addr.String())
		}
		if p.Gateway.IsValid() {
			u.kv("Gateway", p.Gateway.String())
		}
		if p.IP6Gw.IsValid() {
			u.kv("Gateway", p.IP6Gw.String())
		}
		for _, d := range p.DNS {
			u.kv("DNS", d.String())
		}
	case model.DHCPProto:
		u.kv("DHCP", dhcpValue(p))
	case model.WireGuardProto:
		for _, a := range p.Addresses {
			u.kv("Address", a.String())
		}
	case model.NoneProto:
		u.kv("LinkLocalAddressing", "no")
	case nil:
		// pure stub (bridge port / vlan parent) — membership only, set below
	default:
		return "", fmt.Errorf("proto %q not supported by the networkd backend", p.UCITag())
	}
	if nf.bridge != "" {
		u.kv("Bridge", nf.bridge)
	}
	for _, v := range nf.vlans {
		u.kv("VLAN", v)
	}
	if p, ok := nf.proto.(model.DHCPProto); ok && p.Hostname != "" {
		u.section("DHCPv4")
		u.kv("Hostname", p.Hostname)
	}
	return u.String(), nil
}

// dhcpValue maps the model's per-family DHCP flags to networkd's DHCP= enum. A
// bare "dhcp" (neither flag) means BOTH families, matching nmprofile's rule.
func dhcpValue(p model.DHCPProto) string {
	v4, v6 := p.IPv4, p.IPv6
	if !v4 && !v6 {
		v4, v6 = true, true
	}
	switch {
	case v4 && v6:
		return "yes"
	case v4:
		return "ipv4"
	case v6:
		return "ipv6"
	default:
		return "no"
	}
}
