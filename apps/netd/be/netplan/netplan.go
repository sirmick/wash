// Package netplan is the netplan backend.Applier (docs/NET-BACKENDS.md): it
// renders a Config to a single netplan document (internal/washnet/netplanprofile),
// takes over /etc/netplan as the sole author, and drives `netplan apply` — CLI
// only, no D-Bus. Live reads the merged config via `netplan get`, so it sees the
// real box regardless of whether the keyfiles render to /etc or /run (the bug
// the NM file-reader hit on Ubuntu/Debian-netplan systems).
//
// netplan is the authority LAYER: it renders to NM or networkd underneath, so on
// a netplan-driven box wash owns /etc/netplan and lets netplan handle the
// under-renderer. The impure surface is the exec seam (`runner`) plus a
// default-route probe, both injectable for hermetic tests.
package netplan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/caps"
)

// ConfigDir is where netplan reads its YAML source. wash takes it over.
const ConfigDir = "/etc/netplan"

// Capabilities is netplan's profile: the IP layer plus wireless (netplan does
// wifi, unlike networkd). No zones/dhcp-server/AP — those belong to the sibling
// nftables/dnsmasq/hostapd backends.
func Capabilities() caps.Capabilities {
	return caps.New([]string{
		"network/interface",
		"network/device",
		"network/route",
		"network/rule",
		"network/wireguard_peer",
		"wireless/wifi-iface",
		"wireless/wifi-device",
	}, caps.CanWireGuard, caps.CanVLAN, caps.CanBridge, caps.CanPolicyRouting)
}

// Detect probes whether netplan drives this box (docs/NET-BACKENDS.md §2).
// Available = the netplan binary is present; Active = /etc/netplan holds config
// (the box is netplan-managed — the authority layer). Read-only.
func Detect() backend.Detection {
	// Presence via LookPath, not a probe command: `netplan` has no --version
	// (it errors "specify a command"), so running it would always fail Detect.
	// netplan lives in /usr/sbin, so the caller's PATH must include sbin —
	// netd runs privileged, so it does; the dev router must add it.
	if _, err := exec.LookPath("netplan"); err != nil {
		return backend.Detection{Note: "netplan not present"}
	}
	if hasYAML(ConfigDir) {
		return backend.Detection{Available: true, Active: true, Note: "netplan config present in /etc/netplan"}
	}
	return backend.Detection{Available: true, Note: "netplan present but /etc/netplan empty"}
}

// hasYAML reports whether dir contains any *.yaml / *.yml (netplan source).
func hasYAML(dir string) bool {
	for _, ext := range []string{"*.yaml", "*.yml"} {
		if m, _ := filepath.Glob(filepath.Join(dir, ext)); len(m) > 0 {
			return true
		}
	}
	return false
}

// runner is the exec seam: runs a host command, returns combined output. Tests
// inject a fake that records calls.
type runner interface {
	run(name string, args ...string) (string, error)
}

type execRunner struct{}

func (execRunner) run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// hasDefaultRoute reads /proc/net/route — ground truth for the commit-confirm
// lock-out check. (Duplicated from the networkd backend; a shared probe package
// is a fair follow-up once ifupdown lands as the third consumer.)
func hasDefaultRoute() bool {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "00000000" {
			return true
		}
	}
	return false
}

// physicalLinks parses `ip -d -j link` into the managed physical NIC names the
// FE wizards offer as bridge members / vlan parents.
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
			continue
		}
		out = append(out, l.Ifname)
	}
	sort.Strings(out)
	return out
}
