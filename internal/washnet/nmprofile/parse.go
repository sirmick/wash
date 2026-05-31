package nmprofile

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/sirmick/wash/internal/washnet/model"
)

// ParseKeyfiles decompiles a set of NM keyfile connections (keyed by id) back
// into a model.Config — the inverse of RenderKeyfiles, and the read path the UI
// uses to load the box's current settings. Parse∘Render and Render∘Parse are
// asserted identity over the corpus (keyfile_test.go), which is the fidelity
// guarantee that makes "edit current state and write it back" safe.
func ParseKeyfiles(kfs map[string]string) (model.Config, error) {
	var c model.Config

	// Members (bridge-port connections) attach to their master's Device, so
	// collect them first, then assemble — sortedKeys keeps port order stable.
	ports := map[string][]string{} // bridge ifname → member ifnames
	type parsed struct {
		conn map[string]string
		ini  map[string]map[string]string
	}
	var bridges, eths []parsed

	for _, name := range sortedKeys(kfs) {
		ini := parseINI(kfs[name])
		conn := ini["connection"]
		switch conn["type"] {
		case "bridge":
			bridges = append(bridges, parsed{conn, ini})
		case "802-3-ethernet":
			if conn["slave-type"] == "bridge" {
				m := conn["master"]
				ports[m] = append(ports[m], conn["interface-name"])
			} else {
				eths = append(eths, parsed{conn, ini})
			}
		default:
			return c, fmt.Errorf("connection %q: type %q not yet supported by the nm backend", name, conn["type"])
		}
	}

	for _, b := range bridges {
		ifname := b.conn["interface-name"]
		c.Devices = append(c.Devices, model.Device{Name: ifname, Type: "bridge", Ports: ports[ifname]})
		proto, err := parseIPv4(b.ini["ipv4"])
		if err != nil {
			return c, fmt.Errorf("bridge %q: %w", b.conn["id"], err)
		}
		c.Interfaces = append(c.Interfaces, model.Interface{Name: b.conn["id"], Device: ifname, Proto: proto})
	}
	for _, e := range eths {
		iface, err := parseEthInterface(e.conn, e.ini)
		if err != nil {
			return c, fmt.Errorf("connection %q: %w", e.conn["id"], err)
		}
		c.Interfaces = append(c.Interfaces, iface)
	}
	return c, nil
}

func parseEthInterface(conn map[string]string, ini map[string]map[string]string) (model.Interface, error) {
	i := model.Interface{Name: conn["id"], Device: conn["interface-name"]}
	proto, err := parseIPv4(ini["ipv4"])
	if err != nil {
		return i, err
	}
	i.Proto = proto
	return i, nil
}

// parseIPv4 maps an [ipv4] section to the model's proto union.
func parseIPv4(ip4 map[string]string) (model.ProtoConfig, error) {
	switch ip4["method"] {
	case "manual":
		var sp model.StaticProto
		// NM address1 = "addr/prefix[,gateway]".
		if a := ip4["address1"]; a != "" {
			parts := strings.SplitN(a, ",", 2)
			pfx, err := netip.ParsePrefix(strings.TrimSpace(parts[0]))
			if err != nil {
				return nil, fmt.Errorf("address1 %q: %w", a, err)
			}
			sp.IPAddr = pfx
			if len(parts) == 2 {
				gw, err := netip.ParseAddr(strings.TrimSpace(parts[1]))
				if err != nil {
					return nil, fmt.Errorf("gateway in %q: %w", a, err)
				}
				sp.Gateway = gw
			}
		}
		// dns = "a;b;" (trailing semicolon).
		if d := strings.Trim(ip4["dns"], ";"); d != "" {
			for _, s := range strings.Split(d, ";") {
				if s == "" {
					continue
				}
				addr, err := netip.ParseAddr(s)
				if err != nil {
					return nil, fmt.Errorf("dns %q: %w", s, err)
				}
				sp.DNS = append(sp.DNS, addr)
			}
		}
		return sp, nil
	case "auto", "":
		return model.DHCPProto{}, nil
	case "disabled":
		return model.NoneProto{}, nil
	}
	return nil, fmt.Errorf("unsupported ipv4.method %q", ip4["method"])
}

// parseINI parses NM keyfile text into section→key→value. Order is dropped;
// the renderer re-imposes a stable order, so round-trips are canonical.
func parseINI(text string) map[string]map[string]string {
	out := map[string]map[string]string{}
	var cur string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			cur = line[1 : len(line)-1]
			if _, ok := out[cur]; !ok {
				out[cur] = map[string]string{}
			}
			continue
		}
		if i := strings.IndexByte(line, '='); i >= 0 && cur != "" {
			out[cur][strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
