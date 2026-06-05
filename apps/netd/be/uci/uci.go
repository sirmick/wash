// Package uci is the OpenWRT/UCI backend.Applier (docs/NET.md §11 Phase D). It
// renders the FULL Config to /etc/config/{network,firewall,dhcp,wireless} through
// the reflection-based codec (the model is UCI-shaped, so it's ~1:1), takes those
// files over, and reloads OpenWRT's services; Live reads them back through the
// same codec. UCI is the type-1/router backend — it expresses the whole gateway
// (fw4=nftables, dnsmasq, hostapd are all UCI-driven), so it advertises the full
// capability set, unlike the type-2 link-only backends (NM/networkd/netplan).
package uci

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

// ConfigDir is OpenWRT's UCI config tree; wash owns the packages it manages.
const ConfigDir = "/etc/config"

// managedPackages are the /etc/config files wash renders + owns. codec.Render
// keys its output by UCIPackage(), so these line up 1:1 with it.
var managedPackages = []string{"network", "firewall", "dhcp", "wireless"}

// reloadCmd maps a package to the command that re-reads it after a write.
// Wireless reloads via `wifi reload`; the rest via their init.d service.
var reloadCmd = map[string][]string{
	"network":  {"/etc/init.d/network", "reload"},
	"firewall": {"/etc/init.d/firewall", "reload"},
	"dhcp":     {"/etc/init.d/dnsmasq", "reload"},
	"wireless": {"wifi", "reload"},
}

// Capabilities is the full UCI profile — OpenWRT/UCI expresses the entire model
// (link + firewall + dhcp/dns + wireless), so nothing is greyed (the caps doc
// notes "the openwrt/UCI profile has them all").
func Capabilities() caps.Capabilities { return caps.Full() }

// Detect probes whether this is an OpenWRT/UCI box: the `uci` binary plus a
// populated /etc/config. Active when /etc/config/network exists (configured).
func Detect() backend.Detection {
	if _, err := exec.LookPath("uci"); err != nil {
		return backend.Detection{Note: "uci not present"}
	}
	if _, err := os.Stat(filepath.Join(ConfigDir, "network")); err == nil {
		return backend.Detection{Available: true, Active: true, Note: "/etc/config present (OpenWRT/UCI)"}
	}
	return backend.Detection{Available: true, Note: "uci present but /etc/config/network missing"}
}

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

// hasDefaultRoute is the commit-confirm lock-out oracle, read from /proc/net/route.
// (The fourth copy across the file-renderer backends; a shared probe pkg is the
// fair follow-up — see the ifupdown note.)
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

// physicalLinks parses `ip -d -j link` for the real (non-virtual, non-lo) NICs
// the Add wizards offer.
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
