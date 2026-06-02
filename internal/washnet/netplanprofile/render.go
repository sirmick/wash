package netplanprofile

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/sirmick/wash/internal/washnet/model"
)

// Render compiles a Config into the netplan document, keyed by filename (one
// file: FileName — wash takes over and is the sole author under /etc/netplan).
// The map[filename]string shape matches the corpus tests and the backend.
func Render(c model.Config) (map[string]string, error) {
	n := network{Version: 2}
	eth := map[string]device{}
	br := map[string]device{}
	vl := map[string]device{}
	tun := map[string]device{}
	wifi := map[string]device{}

	// Devices first, so an Interface's addressing lands on the right class.
	isBridge := map[string]bool{}
	isVlan := map[string]bool{}
	for _, d := range c.Devices {
		switch d.Type {
		case "bridge":
			br[d.Name] = device{Interfaces: append([]string{}, d.Ports...)}
			isBridge[d.Name] = true
			for _, p := range d.Ports {
				if _, ok := eth[p]; !ok {
					eth[p] = device{}
				}
			}
		case "8021q":
			vl[d.Name] = device{ID: intp(d.VID), Link: d.Ifname}
			isVlan[d.Name] = true
			if _, ok := eth[d.Ifname]; !ok {
				eth[d.Ifname] = device{}
			}
		default:
			return nil, fmt.Errorf("device %q: type %q not supported by the netplan backend", d.Name, d.Type)
		}
	}

	for _, iface := range c.Interfaces {
		if wg, ok := iface.Proto.(model.WireGuardProto); ok {
			tun[iface.Name] = renderWG(wg, iface.Name, c.WGPeers)
			continue
		}
		link := iface.Device
		if link == "" {
			link = iface.Name
		}
		addr := applyProto(iface.Proto)
		switch {
		case isBridge[link]:
			d := br[link]
			mergeAddr(&d, addr)
			br[link] = d
		case isVlan[link]:
			d := vl[link]
			mergeAddr(&d, addr)
			vl[link] = d
		default:
			d := eth[link]
			mergeAddr(&d, addr)
			eth[link] = d
		}
	}

	// Wireless: each SSID lands on its radio's wifis entry; addressing comes
	// from the Interface that referenced it (already applied above if the
	// network name matched a device, else the wifi entry carries it here).
	for _, s := range c.SSIDs {
		d := wifi[s.Device]
		if d.AccessPoints == nil {
			d.AccessPoints = map[string]accesspoint{}
		}
		ap := accesspoint{Hidden: s.Hidden}
		switch e := s.Encryption.(type) {
		case model.EncPSK2:
			ap.Password = e.Key // bare password ⇒ netplan's WPA2-PSK
		case model.EncSAE:
			ap.Auth = &apauth{KeyManagement: "sae", Password: e.Key} // WPA3
		}
		d.AccessPoints[s.SSID] = ap
		wifi[s.Device] = d
		// If an Interface named the radio for addressing, fold it in.
		if eaddr, ok := eth[s.Device]; ok {
			mergeAddr(&d, eaddr)
			wifi[s.Device] = d
			delete(eth, s.Device)
		}
	}

	if len(eth) > 0 {
		n.Ethernets = eth
	}
	if len(br) > 0 {
		n.Bridges = br
	}
	if len(vl) > 0 {
		n.Vlans = vl
	}
	if len(tun) > 0 {
		n.Tunnels = tun
	}
	if len(wifi) > 0 {
		n.Wifis = wifi
	}

	out, err := yaml.Marshal(doc{Network: n})
	if err != nil {
		return nil, err
	}
	return map[string]string{FileName: string(out)}, nil
}

// applyProto turns one interface's protocol into the addressing fields of a
// netplan device entry.
func applyProto(p model.ProtoConfig) device {
	var d device
	switch v := p.(type) {
	case model.StaticProto:
		d.DHCP4 = boolp(false)
		if v.IPAddr.IsValid() {
			d.Addresses = append(d.Addresses, v.IPAddr.String())
		}
		if v.IP6Addr.IsValid() {
			d.Addresses = append(d.Addresses, v.IP6Addr.String())
		}
		// gateway → a default route (netplan deprecated gateway4/6).
		if v.Gateway.IsValid() {
			d.Routes = append(d.Routes, nproute{To: "default", Via: v.Gateway.String()})
		}
		if v.IP6Gw.IsValid() {
			d.Routes = append(d.Routes, nproute{To: "default", Via: v.IP6Gw.String()})
		}
		if len(v.DNS) > 0 {
			ns := &nameservers{}
			for _, a := range v.DNS {
				ns.Addresses = append(ns.Addresses, a.String())
			}
			d.Nameservers = ns
		}
	case model.DHCPProto:
		// bare dhcp (neither flag) means BOTH families, matching the
		// nmprofile/networkdprofile rule.
		v4, v6 := v.IPv4, v.IPv6
		if !v4 && !v6 {
			v4, v6 = true, true
		}
		if v4 {
			d.DHCP4 = boolp(true)
		}
		if v6 {
			d.DHCP6 = boolp(true)
		}
	case model.NoneProto, nil:
		// unmanaged addressing — leave the entry bare.
	}
	return d
}

// mergeAddr folds addressing fields from src into dst (dst already holds
// class-specific keys like Interfaces/ID/Link).
func mergeAddr(dst *device, src device) {
	dst.DHCP4 = src.DHCP4
	dst.DHCP6 = src.DHCP6
	dst.Addresses = src.Addresses
	dst.Routes = src.Routes
	dst.Nameservers = src.Nameservers
}

func renderWG(wg model.WireGuardProto, name string, peers []model.WGPeer) device {
	d := device{Mode: "wireguard", Key: wg.PrivateKey}
	if wg.ListenPort != 0 {
		d.Port = intp(wg.ListenPort)
	}
	for _, a := range wg.Addresses {
		d.Addresses = append(d.Addresses, a.String())
	}
	for _, p := range peers {
		if p.Interface != name {
			continue
		}
		pe := wgpeer{Keys: wgkeys{Public: p.PublicKey, Shared: p.PresharedKey}}
		for _, a := range p.AllowedIPs {
			pe.AllowedIPs = append(pe.AllowedIPs, a.String())
		}
		if p.EndpointHost != "" {
			pe.Endpoint = fmt.Sprintf("%s:%d", p.EndpointHost, p.EndpointPort)
		}
		if p.PersistentKeepalive != 0 {
			pe.Keepalive = intp(p.PersistentKeepalive)
		}
		d.Peers = append(d.Peers, pe)
	}
	return d
}
