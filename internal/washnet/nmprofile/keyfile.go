// Package nmprofile compiles wash-net's model.Config to (and, later, parses it
// from) NetworkManager keyfile connections — the .nmconnection INI text NM's
// keyfile plugin reads from /etc/NetworkManager/system-connections (docs/NET.md
// §5). This is the pure "compile to NM profiles" half, the sibling of the UCI
// codec's "transpile to UCI": both are text transforms over the same IR, so the
// whole mapping is golden-testable at the CLI with no D-Bus and no VM. The NM
// D-Bus backend (apps/netd/be/nm) writes these keyfiles + reloads/activates;
// the VM test is only the final "real NM accepts it" oracle.
package nmprofile

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/sirmick/wash/internal/washnet/model"
)

// RenderKeyfiles compiles a Config into NM keyfile connections, keyed by
// connection id. Most interfaces map 1:1 to a connection; an interface whose
// device is a bridge maps to a `bridge` connection (carrying the addressing)
// plus one enslaved `bridge-port` connection per member — the model↔NM shape
// gap (UCI's flat Interface+Device vs NM's connection-per-link).
func RenderKeyfiles(c model.Config) (map[string]string, error) {
	devByName := map[string]model.Device{}
	for _, d := range c.Devices {
		devByName[d.Name] = d
	}
	ifaceByName := map[string]model.Interface{}
	for _, i := range c.Interfaces {
		ifaceByName[i.Name] = i
	}
	out := map[string]string{}

	// Wifi: NM merges what UCI splits — a WifiIface plus the network Interface it
	// references become ONE 802-11-wireless connection. Render those, and mark
	// their network interfaces so the interface loop doesn't re-emit them.
	wifiNetworks := map[string]bool{}
	for _, w := range c.SSIDs {
		if err := renderWifi(w, ifaceByName[w.Network], out); err != nil {
			return nil, fmt.Errorf("wifi %q: %w", w.Name, err)
		}
		if w.Network != "" {
			wifiNetworks[w.Network] = true
		}
	}

	for _, iface := range c.Interfaces {
		if wifiNetworks[iface.Name] {
			continue // already rendered as the wifi connection's IP config
		}
		if _, ok := iface.Proto.(model.WireGuardProto); ok {
			if err := renderWireGuard(iface, c.WGPeers, out); err != nil {
				return nil, fmt.Errorf("interface %q: %w", iface.Name, err)
			}
			continue
		}
		if dev, ok := devByName[iface.Device]; ok && dev.Type == "bridge" {
			if err := renderBridge(iface, dev, out); err != nil {
				return nil, fmt.Errorf("interface %q: %w", iface.Name, err)
			}
			continue
		}
		if dev, ok := devByName[iface.Device]; ok && dev.Type == "8021q" {
			if err := renderVLAN(iface, dev, out); err != nil {
				return nil, fmt.Errorf("interface %q: %w", iface.Name, err)
			}
			continue
		}
		var k keyfile
		k.section("connection")
		k.kv("id", iface.Name)
		k.kv("type", "802-3-ethernet")
		if iface.Device != "" {
			k.kv("interface-name", iface.Device)
		}
		if err := renderIPv4(&k, iface.Proto); err != nil {
			return nil, fmt.Errorf("interface %q: %w", iface.Name, err)
		}
		k.section("ipv6")
		k.kv("method", "auto")
		out[iface.Name] = k.String()
	}
	return out, nil
}

// renderBridge emits the bridge connection (id = the interface name, holding the
// addressing) and one bridge-port connection per member.
func renderBridge(i model.Interface, dev model.Device, out map[string]string) error {
	var k keyfile
	k.section("connection")
	k.kv("id", i.Name)
	k.kv("type", "bridge")
	k.kv("interface-name", dev.Name)
	if err := renderIPv4(&k, i.Proto); err != nil {
		return err
	}
	k.section("ipv6")
	k.kv("method", "auto")
	out[i.Name] = k.String()

	for _, port := range dev.Ports {
		var p keyfile
		p.section("connection")
		p.kv("id", portConnID(dev.Name, port))
		p.kv("interface-name", port)
		p.kv("master", dev.Name)
		p.kv("slave-type", "bridge")
		p.kv("type", "802-3-ethernet")
		out[portConnID(dev.Name, port)] = p.String()
	}
	return nil
}

// portConnID names an enslaved member connection deterministically so the
// round-trip regenerates an identical keyfile.
func portConnID(bridge, port string) string { return bridge + "-port-" + port }

// renderVLAN emits a `vlan` connection from an Interface over an 802.1q device.
func renderVLAN(i model.Interface, dev model.Device, out map[string]string) error {
	var k keyfile
	k.section("connection")
	k.kv("id", i.Name)
	k.kv("type", "vlan")
	k.kv("interface-name", dev.Name) // e.g. eth0.10
	k.section("vlan")
	k.kv("id", strconv.Itoa(dev.VID))
	k.kv("parent", dev.Ifname)
	if err := renderIPv4(&k, i.Proto); err != nil {
		return err
	}
	k.section("ipv6")
	k.kv("method", "auto")
	out[i.Name] = k.String()
	return nil
}

// ifname is the kernel link name for an interface: its explicit device, else
// its name (a WireGuard interface like "wg0" carries no separate device).
func ifname(i model.Interface) string {
	if i.Device != "" {
		return i.Device
	}
	return i.Name
}

// renderWireGuard emits a `wireguard` connection: the [wireguard] private
// key/port, one [wireguard-peer.<pubkey>] section per peer (sorted by key for
// stable output), and the tunnel addresses split into [ipv4]/[ipv6].
func renderWireGuard(i model.Interface, allPeers []model.WGPeer, out map[string]string) error {
	wg := i.Proto.(model.WireGuardProto)
	var k keyfile
	k.section("connection")
	k.kv("id", i.Name)
	k.kv("type", "wireguard")
	k.kv("interface-name", ifname(i))

	k.section("wireguard")
	if wg.PrivateKey != "" {
		k.kv("private-key", wg.PrivateKey)
	}
	if wg.ListenPort != 0 {
		k.kv("listen-port", strconv.Itoa(wg.ListenPort))
	}

	var peers []model.WGPeer
	for _, p := range allPeers {
		if p.Interface == i.Name {
			peers = append(peers, p)
		}
	}
	sort.Slice(peers, func(a, b int) bool { return peers[a].PublicKey < peers[b].PublicKey })
	for _, p := range peers {
		k.section("wireguard-peer." + p.PublicKey)
		if len(p.AllowedIPs) > 0 {
			k.kv("allowed-ips", joinPrefixesSemi(p.AllowedIPs))
		}
		if p.EndpointHost != "" {
			k.kv("endpoint", endpointStr(p.EndpointHost, p.EndpointPort))
		}
		if p.PresharedKey != "" {
			k.kv("preshared-key", p.PresharedKey)
		}
		if p.PersistentKeepalive != 0 {
			k.kv("persistent-keepalive", strconv.Itoa(p.PersistentKeepalive))
		}
	}

	var v4, v6 []netip.Prefix
	for _, a := range wg.Addresses {
		if a.Addr().Is4() {
			v4 = append(v4, a)
		} else {
			v6 = append(v6, a)
		}
	}
	renderAddrSection(&k, "ipv4", v4)
	renderAddrSection(&k, "ipv6", v6)
	out[i.Name] = k.String()
	return nil
}

// renderAddrSection writes a manual [ipvN] section from a list of addresses, or
// method=disabled when there are none (a WireGuard tunnel has no DHCP/SLAAC).
func renderAddrSection(k *keyfile, sec string, addrs []netip.Prefix) {
	k.section(sec)
	if len(addrs) == 0 {
		k.kv("method", "disabled")
		return
	}
	k.kv("method", "manual")
	for n, a := range addrs {
		k.kv(fmt.Sprintf("address%d", n+1), a.String())
	}
}

func joinPrefixesSemi(ps []netip.Prefix) string {
	ss := make([]string, len(ps))
	for i, p := range ps {
		ss[i] = p.String()
	}
	return strings.Join(ss, ";") + ";"
}

func endpointStr(host string, port int) string {
	if strings.Contains(host, ":") { // IPv6 literal
		return fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// renderWifi emits an 802-11-wireless connection from a WifiIface merged with
// the network Interface it references (which carries the addressing).
func renderWifi(w model.WifiIface, iface model.Interface, out map[string]string) error {
	var k keyfile
	k.section("connection")
	k.kv("id", w.Name)
	k.kv("type", "802-11-wireless")

	k.section("802-11-wireless")
	k.kv("mode", nmWifiMode(w.Mode))
	k.kv("ssid", w.SSID)

	switch e := w.Encryption.(type) {
	case model.EncPSK2:
		k.section("802-11-wireless-security")
		k.kv("key-mgmt", "wpa-psk")
		if e.Key != "" {
			k.kv("psk", e.Key)
		}
	case model.EncSAE:
		k.section("802-11-wireless-security")
		k.kv("key-mgmt", "sae")
		if e.Key != "" {
			k.kv("psk", e.Key)
		}
	}

	if iface.Proto != nil {
		if err := renderIPv4(&k, iface.Proto); err != nil {
			return err
		}
	} else {
		k.section("ipv4")
		k.kv("method", "auto")
	}
	k.section("ipv6")
	k.kv("method", "auto")
	out[w.Name] = k.String()
	return nil
}

// nmWifiMode maps the model's wifi mode to NM's. Client (sta) → infrastructure.
func nmWifiMode(mode string) string {
	if mode == "ap" {
		return "ap"
	}
	return "infrastructure"
}

// renderIPv4 writes the [ipv4] section from the model's proto union.
func renderIPv4(k *keyfile, proto model.ProtoConfig) error {
	k.section("ipv4")
	switch p := proto.(type) {
	case model.StaticProto:
		k.kv("method", "manual")
		addr := p.IPAddr.String() // NM address1 = "addr/prefix[,gateway]"
		if p.Gateway.IsValid() {
			addr += "," + p.Gateway.String()
		}
		k.kv("address1", addr)
		if len(p.DNS) > 0 {
			ss := make([]string, len(p.DNS))
			for n, d := range p.DNS {
				ss[n] = d.String()
			}
			k.kv("dns", strings.Join(ss, ";")+";")
		}
	case model.DHCPProto:
		k.kv("method", "auto")
	case model.NoneProto:
		k.kv("method", "disabled")
	default:
		return fmt.Errorf("proto %q not yet supported by the nm backend", proto.UCITag())
	}
	return nil
}

// keyfile builds NM keyfile (INI) text: sections in append order, keys sorted
// within a section for stable goldens.
type keyfile struct {
	secs  []string
	bySec map[string][][2]string
}

func (k *keyfile) section(name string) {
	if k.bySec == nil {
		k.bySec = map[string][][2]string{}
	}
	if _, ok := k.bySec[name]; !ok {
		k.secs = append(k.secs, name)
		k.bySec[name] = nil
	}
}

func (k *keyfile) kv(key, val string) {
	cur := k.secs[len(k.secs)-1]
	k.bySec[cur] = append(k.bySec[cur], [2]string{key, val})
}

func (k *keyfile) String() string {
	var b strings.Builder
	for i, s := range k.secs {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[%s]\n", s)
		kvs := k.bySec[s]
		sort.SliceStable(kvs, func(a, b int) bool { return kvs[a][0] < kvs[b][0] })
		for _, kv := range kvs {
			fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
		}
	}
	return b.String()
}
