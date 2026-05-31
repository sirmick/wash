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
// connection id. One connection per interface today; bridge/vlan/wifi/wireguard
// mappings extend renderInterface + add member/peer connections.
func RenderKeyfiles(c model.Config) (map[string]string, error) {
	out := map[string]string{}
	for _, iface := range c.Interfaces {
		name, text, err := renderInterface(iface)
		if err != nil {
			return nil, fmt.Errorf("interface %q: %w", iface.Name, err)
		}
		out[name] = text
	}
	return out, nil
}

func renderInterface(i model.Interface) (string, string, error) {
	var k keyfile
	k.section("connection")
	k.kv("id", i.Name)
	k.kv("type", "802-3-ethernet") // plain wired; bridge/vlan/wifi specialise this
	if i.Device != "" {
		k.kv("interface-name", i.Device)
	}

	switch p := i.Proto.(type) {
	case model.StaticProto:
		k.section("ipv4")
		k.kv("method", "manual")
		// NM address1 = "addr/prefix[,gateway]"
		addr := p.IPAddr.String()
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
		k.section("ipv4")
		k.kv("method", "auto")
	case model.NoneProto:
		k.section("ipv4")
		k.kv("method", "disabled")
	default:
		return "", "", fmt.Errorf("proto %q not yet supported by the nm backend", i.Proto.UCITag())
	}

	// IPv6: default to SLAAC/auto until the model carries an ipv6 proto.
	k.section("ipv6")
	k.kv("method", "auto")

	return i.Name, k.String(), nil
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
