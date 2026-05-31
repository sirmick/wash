package nmprofile

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
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
	var bridges, eths, wgs, wifis, vlans []parsed

	for _, name := range sortedKeys(kfs) {
		ini := parseINI(kfs[name])
		conn := ini["connection"]
		switch conn["type"] {
		case "bridge":
			bridges = append(bridges, parsed{conn, ini})
		case "wireguard":
			wgs = append(wgs, parsed{conn, ini})
		case "vlan":
			vlans = append(vlans, parsed{conn, ini})
		case "802-11-wireless":
			wifis = append(wifis, parsed{conn, ini})
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
	for _, v := range vlans {
		vl := v.ini["vlan"]
		vid, _ := strconv.Atoi(vl["id"])
		dev := model.Device{Name: v.conn["interface-name"], Type: "8021q", Ifname: vl["parent"], VID: vid}
		c.Devices = append(c.Devices, dev)
		proto, err := parseIPv4(v.ini["ipv4"])
		if err != nil {
			return c, fmt.Errorf("vlan %q: %w", v.conn["id"], err)
		}
		c.Interfaces = append(c.Interfaces, model.Interface{Name: v.conn["id"], Device: dev.Name, Proto: proto})
	}
	for _, w := range wgs {
		iface, peers := parseWireGuard(w.conn, w.ini)
		c.Interfaces = append(c.Interfaces, iface)
		c.WGPeers = append(c.WGPeers, peers...)
	}
	for _, wf := range wifis {
		ssid, iface, err := parseWifi(wf.conn, wf.ini)
		if err != nil {
			return c, fmt.Errorf("wifi %q: %w", wf.conn["id"], err)
		}
		c.SSIDs = append(c.SSIDs, ssid)
		c.Interfaces = append(c.Interfaces, iface)
	}
	return c, nil
}

// parseWifi splits an 802-11-wireless connection back into a WifiIface and the
// network Interface that carries its IP config, linked by a derived name (NM
// doesn't store UCI's internal cross-ref, so the canonical name round-trips).
func parseWifi(conn map[string]string, ini map[string]map[string]string) (model.WifiIface, model.Interface, error) {
	id := conn["id"]
	wl := ini["802-11-wireless"]
	w := model.WifiIface{Name: id, Network: id, SSID: wl["ssid"], Mode: uciWifiMode(wl["mode"])}
	switch ini["802-11-wireless-security"]["key-mgmt"] {
	case "wpa-psk":
		w.Encryption = model.EncPSK2{Key: ini["802-11-wireless-security"]["psk"]}
	case "sae":
		w.Encryption = model.EncSAE{Key: ini["802-11-wireless-security"]["psk"]}
	default:
		w.Encryption = model.EncNone{}
	}
	proto, err := parseIPv4(ini["ipv4"])
	if err != nil {
		return w, model.Interface{}, err
	}
	return w, model.Interface{Name: id, Proto: proto}, nil
}

func uciWifiMode(mode string) string {
	if mode == "ap" {
		return "ap"
	}
	return "sta"
}

// deviceFor returns the explicit device of a connection, or "" when the device
// equals the connection id (the renderer derives ifname from the name then).
func deviceFor(conn map[string]string) string {
	if conn["interface-name"] == conn["id"] {
		return ""
	}
	return conn["interface-name"]
}

func parseWireGuard(conn map[string]string, ini map[string]map[string]string) (model.Interface, []model.WGPeer) {
	name := conn["id"]
	wg := model.WireGuardProto{}
	if w := ini["wireguard"]; w != nil {
		wg.PrivateKey = w["private-key"]
		if lp := w["listen-port"]; lp != "" {
			wg.ListenPort, _ = strconv.Atoi(lp)
		}
	}
	wg.Addresses = append(parseAddrs(ini["ipv4"]), parseAddrs(ini["ipv6"])...)

	iface := model.Interface{Name: name, Device: deviceFor(conn), Proto: wg}

	var peers []model.WGPeer
	for sec, kv := range ini {
		if !strings.HasPrefix(sec, "wireguard-peer.") {
			continue
		}
		p := model.WGPeer{Interface: name, PublicKey: strings.TrimPrefix(sec, "wireguard-peer.")}
		p.AllowedIPs = parsePrefixList(kv["allowed-ips"])
		if ep := kv["endpoint"]; ep != "" {
			p.EndpointHost, p.EndpointPort = parseEndpoint(ep)
		}
		p.PresharedKey = kv["preshared-key"]
		if k := kv["persistent-keepalive"]; k != "" {
			p.PersistentKeepalive, _ = strconv.Atoi(k)
		}
		peers = append(peers, p)
	}
	sort.Slice(peers, func(a, b int) bool { return peers[a].PublicKey < peers[b].PublicKey })
	return iface, peers
}

// parseAddrs reads address1..N from a manual [ipvN] section (ignoring any
// trailing gateway, which WireGuard tunnels don't carry).
func parseAddrs(sec map[string]string) []netip.Prefix {
	if sec["method"] != "manual" {
		return nil
	}
	var out []netip.Prefix
	for n := 1; ; n++ {
		v := sec[fmt.Sprintf("address%d", n)]
		if v == "" {
			break
		}
		if pfx, err := netip.ParsePrefix(strings.SplitN(v, ",", 2)[0]); err == nil {
			out = append(out, pfx)
		}
	}
	return out
}

func parsePrefixList(s string) []netip.Prefix {
	var out []netip.Prefix
	for _, x := range strings.Split(strings.Trim(s, ";"), ";") {
		if x == "" {
			continue
		}
		if p, err := netip.ParsePrefix(x); err == nil {
			out = append(out, p)
		}
	}
	return out
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
