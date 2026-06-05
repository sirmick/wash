//go:build distro_integration

package uci

import (
	"net/netip"
	"os/exec"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/model"
)

// Runs INSIDE the OpenWRT rootfs container (skips elsewhere). Proves the full
// loop against real uci + /etc/config: read the box in through wash's codec,
// modify, write back, and confirm OpenWRT's own uci parses the result and shows
// the change. Compiled static (-tags=distro_integration, CGO off) and run in
// docker — see the harness in the commit/run notes.

func uciCmd(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("uci", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("uci %s failed — wash wrote invalid UCI: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestUCI_Distro_ReadModifyWrite(t *testing.T) {
	if _, err := exec.LookPath("uci"); err != nil {
		t.Skip("uci missing — not an OpenWRT box")
	}
	a := NewApplier() // real /etc/config

	// Seed a minimal /etc/config/network via uci itself, so the test is
	// self-contained on any OpenWRT image (the rootfs container ships none).
	// CIDR ipaddr is wash's format; reading stock split ipaddr+netmask is a
	// separate, known gap in the shared codec.
	seed := [][]string{
		{"set", "network.loopback=interface"}, {"set", "network.loopback.device=lo"},
		{"set", "network.loopback.proto=static"}, {"set", "network.loopback.ipaddr=127.0.0.1/8"},
		{"set", "network.lan=interface"}, {"set", "network.lan.device=br-lan"},
		{"set", "network.lan.proto=static"}, {"set", "network.lan.ipaddr=192.168.1.1/24"},
		{"set", "network.wan=interface"}, {"set", "network.wan.device=eth1"}, {"set", "network.wan.proto=dhcp"},
	}
	_ = exec.Command("touch", "/etc/config/network").Run()
	for _, s := range seed {
		uciCmd(t, s...)
	}
	uciCmd(t, "commit", "network")

	// --- UCI IN: read the box through wash. ---
	live := a.Live()
	lanIdx := -1
	for i := range live.Interfaces {
		if live.Interfaces[i].Name == "lan" {
			lanIdx = i
		}
	}
	if lanIdx < 0 {
		t.Fatalf("did not read a 'lan' interface from /etc/config/network: %+v", live.Interfaces)
	}
	t.Logf("UCI IN: lan proto read as %+v", live.Interfaces[lanIdx].Proto)

	// --- MODIFY: set lan's static address. ---
	live.Interfaces[lanIdx].Proto = model.StaticProto{IPAddr: netip.MustParsePrefix("192.168.9.1/24")}

	// --- UCI OUT: write it back through wash. ---
	if _, err := a.Apply(backend.RenderPlan{Target: live}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// --- OPENWRT WORKS: uci parses wash's output and shows the new address. ---
	if got := uciCmd(t, "get", "network.lan.ipaddr"); !strings.Contains(got, "192.168.9.1") {
		t.Errorf("uci get network.lan.ipaddr = %q, want 192.168.9.1/24", got)
	}
	uciCmd(t, "show", "network") // the whole package must parse, or uci errors

	// --- ROUND-TRIP: wash reads its own output back unchanged. ---
	back := a.Live()
	ok := false
	for _, i := range back.Interfaces {
		if i.Name != "lan" {
			continue
		}
		sp, isStatic := i.Proto.(model.StaticProto)
		if isStatic && sp.IPAddr.String() == "192.168.9.1/24" {
			ok = true
		}
	}
	if !ok {
		t.Errorf("lan static IP did not survive the write→read round-trip: %+v", back.Interfaces)
	}
	t.Logf("OK: read → modify → write → uci accepts → round-trip, all on real OpenWRT uci")
}
