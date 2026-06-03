// Package ifupdown is the Debian ifupdown backend.Applier (docs/NET-BACKENDS.md
// §4): it renders a Config to /etc/network/interfaces
// (internal/washnet/ifupdownprofile), takes the file over, and drives ifup/ifdown
// — CLI only. Live parses the file. A deliberately small backend: static/dhcp/
// manual interfaces, bridges (bridge-utils), vlans (vlan pkg). No wireguard/
// zones/nat/wifi.
package ifupdown

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
)

// InterfacesPath is the file wash owns (takeover).
const InterfacesPath = "/etc/network/interfaces"

// Capabilities is ifupdown's small profile: interfaces + bridge + vlan. No
// wireguard/zones/nat/wifi — those have no ifupdown expression here.
func Capabilities() caps.Capabilities {
	return caps.New([]string{
		"network/interface",
		"network/device",
	}, caps.CanBridge, caps.CanVLAN)
}

// Detect probes whether ifupdown manages this box (docs/NET-BACKENDS.md §2).
// Available = the ifup binary is present; Active = /etc/network/interfaces has a
// real (non-loopback) stanza, i.e. ifupdown is actually configured.
func Detect() backend.Detection {
	if _, err := exec.LookPath("ifup"); err != nil {
		return backend.Detection{Note: "ifup not present"}
	}
	if hasRealConfig(InterfacesPath) {
		return backend.Detection{Available: true, Active: true, Note: "/etc/network/interfaces configured"}
	}
	return backend.Detection{Available: true, Note: "ifupdown present but /etc/network/interfaces has no real config"}
}

// hasRealConfig reports whether path has any `iface` stanza other than lo.
func hasRealConfig(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) >= 2 && f[0] == "iface" && f[1] != "lo" {
			return true
		}
	}
	return false
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

// hasDefaultRoute reads /proc/net/route — the commit-confirm lock-out oracle.
// (Duplicated across the file-renderer backends; a shared probe pkg is a fair
// follow-up now that ifupdown is the third consumer.)
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
