// Package networkd is the systemd-networkd backend.Applier (docs/NET.md §2.3,
// Phase C): it renders a Config to .netdev/.network unit files
// (internal/washnet/networkdprofile), writes them to /etc/systemd/network, and
// drives `networkctl reload` + `reconfigure`. Unlike the NM backend it needs no
// D-Bus — files-then-reload — so the impure surface is just a small exec seam
// (the `runner`) plus a default-route probe, both injectable so Apply/Verify/
// Rollback unit-test against a fake runner + temp dir, with the real
// networkctl/ip oracle exercised only in the Fedora microvm.
//
// networkd owns the IP layer ONLY (§2.3): interfaces, devices (bridge/vlan),
// routes, policy rules, WireGuard. Firewall zones, the DHCP server, wifi and AP
// are sibling direct backends (nftables/dnsmasq/hostapd) — so this backend's
// Capabilities deliberately omit CanZones/CanDHCPServer/CanAP and the wireless
// kinds, and the UI greys them until those backends land.
package networkd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/model"
)

// UnitDir is where systemd-networkd reads .netdev/.network units.
const UnitDir = "/etc/systemd/network"

// Capabilities is the IP-layer profile networkd covers (docs/NET.md §2.3). Same
// link/route/wireguard set as NM, minus the wireless kinds (networkd does no
// wifi) — and, like NM, without the router features (zones/dhcp-server/ap) that
// belong to the sibling nftables/dnsmasq/hostapd backends.
func Capabilities() caps.Capabilities {
	return caps.New([]string{
		"network/interface",
		"network/device",
		"network/route",
		"network/rule",
		"network/wireguard_peer",
	}, caps.CanWireGuard, caps.CanVLAN, caps.CanBridge, caps.CanPolicyRouting)
}

// Detect probes whether systemd-networkd can run here and is the active link
// manager (docs/NET.md §2.7) — read-only, for netd's `auto` selection. Available
// = networkctl is present; Active = systemd-networkd is running. Uses the real
// exec seam (the decision logic that consumes this is unit-tested separately).
func Detect() backend.Detection {
	r := execRunner{}
	if _, err := r.run("networkctl", "--version"); err != nil {
		return backend.Detection{Note: "networkctl not present"}
	}
	out, err := r.run("systemctl", "is-active", "systemd-networkd")
	if err == nil && strings.TrimSpace(out) == "active" {
		return backend.Detection{Available: true, Active: true, Note: "systemd-networkd active"}
	}
	return backend.Detection{Available: true, Note: "systemd-networkd present but not active"}
}

// runner is the exec seam: it runs a host command and returns its combined
// output. The real impl shells out; tests inject a fake that records calls.
type runner interface {
	run(name string, args ...string) (string, error)
}

type execRunner struct{}

func (execRunner) run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// affectedLinks is the set of kernel links a config touches — interface links,
// virtual device names, bridge members, and vlan parents — so Apply can
// `networkctl reconfigure` exactly them after a reload. Sorted for determinism.
func affectedLinks(c model.Config) []string {
	set := map[string]bool{}
	for _, d := range c.Devices {
		set[d.Name] = true
		if d.Ifname != "" {
			set[d.Ifname] = true
		}
		for _, p := range d.Ports {
			set[p] = true
		}
	}
	for _, i := range c.Interfaces {
		link := i.Device
		if link == "" {
			link = i.Name
		}
		set[link] = true
	}
	out := make([]string, 0, len(set))
	for l := range set {
		if l != "" {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// hasDefaultRoute reports whether the kernel has any IPv4 default route, read
// straight from /proc/net/route — ground truth for the commit-confirm lock-out
// check (same probe the NM backend uses; networkd has no equivalent).
func hasDefaultRoute() bool {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n")[1:] { // skip header
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "00000000" {
			return true
		}
	}
	return false
}

// physicalLinks parses `ip -d -j link` output into the managed physical link
// names (ether, not loopback, not a virtual device) the FE wizards offer as
// VLAN parents / bridge members.
func physicalLinks(jsonOut string) []string {
	var links []struct {
		Ifname   string `json:"ifname"`
		LinkType string `json:"link_type"`
		LinkInfo *struct {
			InfoKind string `json:"info_kind"`
		} `json:"linkinfo"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &links); err != nil {
		return nil
	}
	var out []string
	for _, l := range links {
		if l.LinkType != "ether" || l.Ifname == "lo" {
			continue
		}
		if l.LinkInfo != nil && l.LinkInfo.InfoKind != "" {
			continue // a virtual device (bridge/vlan/wireguard), not a physical NIC
		}
		out = append(out, l.Ifname)
	}
	sort.Strings(out)
	return out
}
