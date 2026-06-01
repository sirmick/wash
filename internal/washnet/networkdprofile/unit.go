// Package networkdprofile compiles wash-net's model.Config to (and parses it
// back from) systemd-networkd unit files — the .netdev / .network INI text
// networkd reads from /etc/systemd/network (docs/NET.md §4, Phase C). It is the
// pure "compile to networkd" sibling of nmprofile's "compile to NM keyfiles":
// both are text transforms over the same IR, golden-testable at the CLI with no
// D-Bus, no kernel, no VM. The networkd D-Bus/exec backend (apps/netd/be/
// networkd) writes these files + runs `networkctl reload/reconfigure`; the VM
// test is only the final "real networkd accepts it" oracle.
//
// Shape note: networkd SPLITS device-from-addressing exactly the way the
// UCI-shaped model already does — a virtual Device (bridge/vlan/wireguard) is a
// .netdev, an Interface's addressing is a .network matched to its link. So none
// of the NM "merge what UCI splits" gymnastics (synthetic bridge-port
// connections, wifi+iface collapse) is needed. networkd covers the IP layer
// only; wifi/AP/firewall/dhcp-server live in sibling backends (§2.3), so this
// corpus has no wifi scenario.
package networkdprofile

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// iniSection is one [Section] with its ordered key/value lines. networkd keys
// AND sections repeat (Address=, DNS=, VLAN=, multiple [WireGuardPeer]), so this
// is an ordered list, not a map — the structural difference from nmprofile's
// keyfile, whose keys are unique-and-sorted.
type iniSection struct {
	name string
	kv   [][2]string
}

// unit builds a networkd unit file: sections in append order, keys in append
// order within a section. networkd is order-tolerant; we keep a deterministic
// order (driven by the model's slice order) so goldens are stable. section()
// always appends, so repeated sections like [WireGuardPeer] are preserved.
type unit struct {
	secs []iniSection
}

func (u *unit) section(name string) { u.secs = append(u.secs, iniSection{name: name}) }

func (u *unit) kv(key, val string) {
	s := &u.secs[len(u.secs)-1]
	s.kv = append(s.kv, [2]string{key, val})
}

func (u *unit) String() string {
	var b strings.Builder
	for i := range u.secs {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[%s]\n", u.secs[i].name)
		for _, kv := range u.secs[i].kv {
			fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
		}
	}
	return b.String()
}

// parseUnitINI parses unit text into ordered sections, preserving repeated
// sections and keys (and dropping comments / blank lines). The renderer
// re-imposes a stable order, so round-trips are canonical.
func parseUnitINI(text string) []iniSection {
	var secs []iniSection
	cur := -1
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			secs = append(secs, iniSection{name: line[1 : len(line)-1]})
			cur = len(secs) - 1
			continue
		}
		if i := strings.IndexByte(line, '='); i >= 0 && cur >= 0 {
			k := strings.TrimSpace(line[:i])
			v := strings.TrimSpace(line[i+1:])
			secs[cur].kv = append(secs[cur].kv, [2]string{k, v})
		}
	}
	return secs
}

// firstVal returns the first value of key in the first section named sec, or "".
func firstVal(secs []iniSection, sec, key string) string {
	for _, s := range secs {
		if s.name != sec {
			continue
		}
		for _, kv := range s.kv {
			if kv[0] == key {
				return kv[1]
			}
		}
	}
	return ""
}

// allVals returns every value of key across every section named sec, in order.
func allVals(secs []iniSection, sec, key string) []string {
	var out []string
	for _, s := range secs {
		if s.name != sec {
			continue
		}
		for _, kv := range s.kv {
			if kv[0] == key {
				out = append(out, kv[1])
			}
		}
	}
	return out
}

// --- filename scheme --------------------------------------------------------
//
// networkd applies files in lexical filename order; the priority prefix bands
// devices before the networks that bind to them. Names are derived from model
// identity so a round-trip regenerates them byte-identically:
//
//	<netdevPrio>-<devname>.netdev      a virtual device (bridge/vlan/wireguard)
//	<networkPrio>-<owner|link>.network a link's config; owner = the Interface
//	                                   name when one owns the link, else the
//	                                   link name (bridge-port / vlan-parent stub)
const (
	netdevPrio  = "20"
	networkPrio = "30"
)

func netdevFile(dev string) string { return netdevPrio + "-" + dev + ".netdev" }

func networkFile(key string) string { return networkPrio + "-" + key + ".network" }

// stemNoPrio recovers the filename key (owner/link) from a unit filename:
// strips ext and a leading numeric "NN-" priority. The key itself may contain
// '-' (e.g. "eth-static"), so only a numeric leading segment is treated as the
// prefix.
func stemNoPrio(fn, ext string) string {
	base := strings.TrimSuffix(fn, ext)
	if i := strings.IndexByte(base, '-'); i > 0 {
		if _, err := strconv.Atoi(base[:i]); err == nil {
			return base[i+1:]
		}
	}
	return base
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// --- shared scalar helpers --------------------------------------------------

func joinPrefixes(ps []netip.Prefix) string {
	ss := make([]string, len(ps))
	for i, p := range ps {
		ss[i] = p.String()
	}
	return strings.Join(ss, ",")
}

func parsePrefixList(s string) []netip.Prefix {
	var out []netip.Prefix
	for _, x := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if p, err := netip.ParsePrefix(x); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func endpoint(host string, port int) string {
	if strings.Contains(host, ":") { // IPv6 literal
		return fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

func parseEndpoint(ep string) (string, int) {
	host, portStr := ep, ""
	if strings.HasPrefix(ep, "[") { // [v6]:port
		if i := strings.LastIndex(ep, "]"); i >= 0 {
			host = ep[1:i]
			if len(ep) > i+1 && ep[i+1] == ':' {
				portStr = ep[i+2:]
			}
		}
	} else if i := strings.LastIndex(ep, ":"); i >= 0 {
		host, portStr = ep[:i], ep[i+1:]
	}
	port, _ := strconv.Atoi(portStr)
	return host, port
}
