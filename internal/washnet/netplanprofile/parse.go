package netplanprofile

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sirmick/wash/internal/washnet/model"
)

// Parse decompiles a set of netplan documents back into a model.Config — the
// inverse of Render. Because netplan keys devices by ifname (eth0, br0), the
// UCI *interface name* doesn't survive: a parsed Interface takes Name == its
// device. So netplan's round-trip identity is at the YAML layer —
// Render(Parse(Render(c))) == Render(c) — not the model layer. The corpus tests
// assert that fixpoint plus the uci→YAML compile goldens.
func Parse(files map[string]string) (model.Config, error) {
	// Merge all docs (netplan merges /etc/netplan/*.yaml; wash owns one file).
	var n network
	names := make([]string, 0, len(files))
	for k := range files {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		var d doc
		if err := yaml.Unmarshal([]byte(files[k]), &d); err != nil {
			return model.Config{}, fmt.Errorf("netplan %s: %w", k, err)
		}
		mergeNetwork(&n, d.Network)
	}

	var c model.Config

	// Which bare ethernets are implied by a bridge/vlan (no Interface of own).
	member := map[string]bool{}
	for name, b := range n.Bridges {
		c.Devices = append(c.Devices, model.Device{Name: name, Type: "bridge", Ports: append([]string{}, b.Interfaces...)})
		for _, p := range b.Interfaces {
			member[p] = true
		}
		c.Interfaces = append(c.Interfaces, ifaceFrom(name, name, b))
	}
	for name, v := range n.Vlans {
		dev := model.Device{Name: name, Type: "8021q", Ifname: v.Link}
		if v.ID != nil {
			dev.VID = *v.ID
		}
		c.Devices = append(c.Devices, dev)
		member[v.Link] = true
		c.Interfaces = append(c.Interfaces, ifaceFrom(name, name, v))
	}
	for name, e := range n.Ethernets {
		if member[name] && bare(e) {
			continue // a bridge port / vlan parent — implied by the Device
		}
		c.Interfaces = append(c.Interfaces, ifaceFrom(name, name, e))
	}
	for name, t := range n.Tunnels {
		iface, peers := wgFrom(name, t)
		c.Interfaces = append(c.Interfaces, iface)
		c.WGPeers = append(c.WGPeers, peers...)
	}
	for radio, w := range n.Wifis {
		// Best-effort: each access-point → a WifiIface; addressing → an Interface.
		c.Radios = append(c.Radios, model.WifiDevice{Name: radio})
		for ssid, ap := range w.AccessPoints {
			si := model.WifiIface{Device: radio, SSID: ssid, Mode: "sta", Network: radio, Hidden: ap.Hidden}
			switch {
			case ap.Auth != nil && ap.Auth.KeyManagement == "sae":
				si.Encryption = model.EncSAE{Key: firstNonEmpty(ap.Auth.Password, ap.Password)}
			case ap.Auth != nil && ap.Auth.KeyManagement == "psk":
				si.Encryption = model.EncPSK2{Key: firstNonEmpty(ap.Auth.Password, ap.Password)}
			case ap.Password != "":
				si.Encryption = model.EncPSK2{Key: ap.Password}
			default:
				si.Encryption = model.EncNone{}
			}
			c.SSIDs = append(c.SSIDs, si)
		}
		c.Interfaces = append(c.Interfaces, ifaceFrom(radio, radio, w))
	}

	sortConfig(&c)
	return c, nil
}

// firstNonEmpty returns the first non-empty string (netplan may carry the PSK
// either as the bare password or inside the auth block).
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// ifaceFrom builds an Interface from a device entry's addressing.
func ifaceFrom(name, dev string, d device) model.Interface {
	return model.Interface{Name: name, Device: dev, Proto: protoFrom(d)}
}

func protoFrom(d device) model.ProtoConfig {
	v4 := d.DHCP4 != nil && *d.DHCP4
	v6 := d.DHCP6 != nil && *d.DHCP6
	if v4 || v6 {
		if v4 && v6 {
			return model.DHCPProto{} // bare = both families
		}
		return model.DHCPProto{IPv4: v4, IPv6: v6}
	}
	if len(d.Addresses) == 0 {
		return model.NoneProto{}
	}
	var sp model.StaticProto
	for _, a := range d.Addresses {
		if p, err := netip.ParsePrefix(a); err == nil {
			if p.Addr().Is4() {
				sp.IPAddr = p
			} else {
				sp.IP6Addr = p
			}
		}
	}
	for _, r := range d.Routes {
		if r.To != "default" && r.To != "0.0.0.0/0" && r.To != "::/0" {
			continue // standalone routes aren't a proto field
		}
		if gw, err := netip.ParseAddr(r.Via); err == nil {
			if gw.Is4() {
				sp.Gateway = gw
			} else {
				sp.IP6Gw = gw
			}
		}
	}
	if d.Nameservers != nil {
		for _, a := range d.Nameservers.Addresses {
			if ad, err := netip.ParseAddr(a); err == nil {
				sp.DNS = append(sp.DNS, ad)
			}
		}
	}
	return sp
}

func wgFrom(name string, d device) (model.Interface, []model.WGPeer) {
	wg := model.WireGuardProto{PrivateKey: d.Key}
	if d.Port != nil {
		wg.ListenPort = *d.Port
	}
	for _, a := range d.Addresses {
		if p, err := netip.ParsePrefix(a); err == nil {
			wg.Addresses = append(wg.Addresses, p)
		}
	}
	var peers []model.WGPeer
	for i, p := range d.Peers {
		wp := model.WGPeer{Name: fmt.Sprintf("%s%d", name, i), Interface: name, PublicKey: p.Keys.Public, PresharedKey: p.Keys.Shared}
		for _, a := range p.AllowedIPs {
			if pre, err := netip.ParsePrefix(a); err == nil {
				wp.AllowedIPs = append(wp.AllowedIPs, pre)
			}
		}
		if host, port, ok := splitEndpoint(p.Endpoint); ok {
			wp.EndpointHost, wp.EndpointPort = host, port
		}
		if p.Keepalive != nil {
			wp.PersistentKeepalive = *p.Keepalive
		}
		peers = append(peers, wp)
	}
	return model.Interface{Name: name, Device: "", Proto: wg}, peers
}

func splitEndpoint(s string) (string, int, bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, false
	}
	port, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return "", 0, false
	}
	return s[:i], port, true
}

// bare reports whether an ethernet entry carries no config of its own (so it's
// purely a bridge member / vlan parent).
func bare(d device) bool {
	return d.DHCP4 == nil && d.DHCP6 == nil && len(d.Addresses) == 0 &&
		len(d.Routes) == 0 && d.Nameservers == nil && len(d.AccessPoints) == 0
}

func mergeNetwork(dst *network, src network) {
	dst.Version = src.Version
	if src.Renderer != "" {
		dst.Renderer = src.Renderer
	}
	dst.Ethernets = mergeDevices(dst.Ethernets, src.Ethernets)
	dst.Bridges = mergeDevices(dst.Bridges, src.Bridges)
	dst.Vlans = mergeDevices(dst.Vlans, src.Vlans)
	dst.Tunnels = mergeDevices(dst.Tunnels, src.Tunnels)
	dst.Wifis = mergeDevices(dst.Wifis, src.Wifis)
}

func mergeDevices(dst, src map[string]device) map[string]device {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = map[string]device{}
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// sortConfig gives the parsed config a stable order (Render is order-agnostic
// for maps, but Interfaces/Devices are slices the YAML fixpoint depends on).
func sortConfig(c *model.Config) {
	sort.Slice(c.Interfaces, func(i, j int) bool { return c.Interfaces[i].Name < c.Interfaces[j].Name })
	sort.Slice(c.Devices, func(i, j int) bool { return c.Devices[i].Name < c.Devices[j].Name })
	sort.Slice(c.WGPeers, func(i, j int) bool { return c.WGPeers[i].Name < c.WGPeers[j].Name })
	sort.Slice(c.Radios, func(i, j int) bool { return c.Radios[i].Name < c.Radios[j].Name })
	sort.Slice(c.SSIDs, func(i, j int) bool { return c.SSIDs[i].SSID < c.SSIDs[j].SSID })
}
