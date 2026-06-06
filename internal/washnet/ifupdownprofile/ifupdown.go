// Package ifupdownprofile compiles the wash UCI model to/from a Debian
// /etc/network/interfaces file (ifupdown). It is the ifupdown counterpart of
// networkdprofile/netplanprofile: Render(model.Config) → one interfaces file,
// Parse → model.Config.
//
// ifupdown is a deliberately small backend (docs/NET-BACKENDS.md §4): per-
// interface static/dhcp/manual, bridges via bridge-utils (bridge_ports), vlans
// via the vlan pkg (vlan-raw-device). No wireguard / zones / nat / wifi.
//
// Like netplan, ifupdown keys stanzas by ifname, so the UCI interface *name*
// isn't preserved — a parsed Interface takes Name == its ifname, and round-trip
// identity is at the text layer: Render(Parse(Render(c))) == Render(c).
package ifupdownprofile

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/sirmick/wash/internal/washnet/model"
)

// FileName is the single file wash owns (takeover): /etc/network/interfaces.
const FileName = "interfaces"

// stanza is one rendered `iface` block.
type stanza struct {
	name      string
	method    string // static | dhcp | manual
	addr      string // CIDR (static)
	gateway   string
	dns       []string
	bridge    []string // bridge_ports (this iface is a bridge)
	rawDevice string   // vlan-raw-device (this iface is a vlan)
}

// Render compiles a Config into one interfaces file, keyed by FileName.
func Render(c model.Config) (map[string]string, error) {
	stanzas := map[string]*stanza{}
	at := func(name string) *stanza {
		s := stanzas[name]
		if s == nil {
			s = &stanza{name: name, method: "manual"}
			stanzas[name] = s
		}
		return s
	}

	// Devices: bridges + vlans contribute their structural options.
	for _, d := range c.Devices {
		switch d.Type {
		case "bridge":
			at(d.Name).bridge = append([]string{}, d.Ports...)
		case "8021q":
			at(d.Name).rawDevice = d.Ifname
		default:
			return nil, fmt.Errorf("device %q: type %q not supported by the ifupdown backend", d.Name, d.Type)
		}
	}

	// Interfaces: addressing.
	for _, iface := range c.Interfaces {
		if _, ok := iface.Proto.(model.WireGuardProto); ok {
			return nil, fmt.Errorf("interface %q: wireguard is not supported by the ifupdown backend", iface.Name)
		}
		name := iface.Device
		if name == "" {
			name = iface.Name
		}
		s := at(name)
		applyProto(s, iface.Proto)
	}

	names := make([]string, 0, len(stanzas))
	for n := range stanzas {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	for i, n := range names {
		if i > 0 {
			b.WriteString("\n")
		}
		writeStanza(&b, stanzas[n])
	}
	return map[string]string{FileName: b.String()}, nil
}

func applyProto(s *stanza, p model.ProtoConfig) {
	switch v := p.(type) {
	case model.StaticProto:
		s.method = "static"
		// ifupdown's inet stanza carries one address; use the primary (extra
		// addresses would need alias stanzas — out of scope, the multi-IP target is
		// UCI).
		if a := v.Primary(); a.IsValid() {
			s.addr = a.String()
		}
		if v.Gateway.IsValid() {
			s.gateway = v.Gateway.String()
		}
		for _, a := range v.DNS {
			s.dns = append(s.dns, a.String())
		}
	case model.DHCPProto:
		s.method = "dhcp"
	case model.NoneProto, nil:
		s.method = "manual"
	}
}

func writeStanza(b *strings.Builder, s *stanza) {
	fmt.Fprintf(b, "auto %s\n", s.name)
	fmt.Fprintf(b, "iface %s inet %s\n", s.name, s.method)
	if s.addr != "" {
		fmt.Fprintf(b, "    address %s\n", s.addr)
	}
	if s.gateway != "" {
		fmt.Fprintf(b, "    gateway %s\n", s.gateway)
	}
	if len(s.dns) > 0 {
		fmt.Fprintf(b, "    dns-nameservers %s\n", strings.Join(s.dns, " "))
	}
	if len(s.bridge) > 0 {
		fmt.Fprintf(b, "    bridge_ports %s\n", strings.Join(s.bridge, " "))
	}
	if s.rawDevice != "" {
		fmt.Fprintf(b, "    vlan-raw-device %s\n", s.rawDevice)
	}
}

// Parse decompiles an interfaces file into a model.Config (the inverse of
// Render). The `lo`/loopback stanza is ignored (wash doesn't model it).
func Parse(files map[string]string) (model.Config, error) {
	// Concatenate all sources (a real box may use interfaces.d/*).
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var all strings.Builder
	for _, k := range keys {
		all.WriteString(files[k])
		all.WriteString("\n")
	}

	var c model.Config
	var cur *stanza
	flush := func() {
		if cur == nil || cur.name == "lo" {
			cur = nil
			return
		}
		stanzaToModel(&c, cur)
		cur = nil
	}

	for _, raw := range strings.Split(all.String(), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "auto", "allow-hotplug":
			continue // the `iface` line drives stanza creation
		case "iface":
			flush()
			if len(fields) >= 4 { // iface NAME inet METHOD
				cur = &stanza{name: fields[1], method: fields[3]}
			}
		default:
			if cur == nil {
				continue
			}
			applyOption(cur, fields)
		}
	}
	flush()

	sortConfig(&c)
	return c, nil
}

func applyOption(s *stanza, f []string) {
	if len(f) < 2 {
		return
	}
	switch f[0] {
	case "address":
		s.addr = f[1]
	case "gateway":
		s.gateway = f[1]
	case "dns-nameservers":
		s.dns = append(s.dns, f[1:]...)
	case "bridge_ports":
		s.bridge = append(s.bridge, f[1:]...)
	case "vlan-raw-device":
		s.rawDevice = f[1]
	}
}

func stanzaToModel(c *model.Config, s *stanza) {
	// Structural device first.
	switch {
	case len(s.bridge) > 0:
		c.Devices = append(c.Devices, model.Device{Name: s.name, Type: "bridge", Ports: append([]string{}, s.bridge...)})
	case s.rawDevice != "" || isVlanName(s.name):
		raw := s.rawDevice
		vid := 0
		if i := strings.LastIndex(s.name, "."); i >= 0 {
			vid, _ = strconv.Atoi(s.name[i+1:])
			if raw == "" {
				raw = s.name[:i]
			}
		}
		c.Devices = append(c.Devices, model.Device{Name: s.name, Type: "8021q", Ifname: raw, VID: vid})
	}
	c.Interfaces = append(c.Interfaces, model.Interface{Name: s.name, Device: s.name, Proto: protoOf(s)})
}

func protoOf(s *stanza) model.ProtoConfig {
	switch s.method {
	case "dhcp":
		return model.DHCPProto{IPv4: true}
	case "static":
		var sp model.StaticProto
		if p, err := netip.ParsePrefix(s.addr); err == nil {
			sp.IPAddr = append(sp.IPAddr, p)
		}
		if g, err := netip.ParseAddr(s.gateway); err == nil {
			sp.Gateway = g
		}
		for _, d := range s.dns {
			if a, err := netip.ParseAddr(d); err == nil {
				sp.DNS = append(sp.DNS, a)
			}
		}
		return sp
	default:
		return model.NoneProto{}
	}
}

func isVlanName(name string) bool {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return false
	}
	_, err := strconv.Atoi(name[i+1:])
	return err == nil
}

func sortConfig(c *model.Config) {
	sort.Slice(c.Interfaces, func(i, j int) bool { return c.Interfaces[i].Name < c.Interfaces[j].Name })
	sort.Slice(c.Devices, func(i, j int) bool { return c.Devices[i].Name < c.Devices[j].Name })
}
