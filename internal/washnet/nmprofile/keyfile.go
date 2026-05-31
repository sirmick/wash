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
	"sort"
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
	out := map[string]string{}
	for _, iface := range c.Interfaces {
		if dev, ok := devByName[iface.Device]; ok && dev.Type == "bridge" {
			if err := renderBridge(iface, dev, out); err != nil {
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
