package vm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openwrtArtifacts(t *testing.T) (disk string) {
	t.Helper()
	wd, _ := os.Getwd()
	root := filepath.Join(wd, "..", "..")
	disk = filepath.Join(root, "out", "vm", "openwrt.img")
	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("qemu-system-x86_64 not found")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("/dev/kvm not available")
	}
	if _, err := os.Stat(disk); err != nil {
		t.Skipf("openwrt image missing: %s (run scripts/build-vm-image-openwrt.sh)", disk)
	}
	return disk
}

// TestOpenWRTRouterReadApply boots a real, running OpenWRT router and proves
// wash's UCI backend both READS its live gateway config and APPLIES an authored
// config that the running netifd carries to the kernel. This is the router half
// of the multi-VM truth test (docs/NET-ROUTER-UI.md) — the "wash in router mode"
// proof against actual uci/fw4/netifd, not a rootfs snapshot.
func TestOpenWRTRouterReadApply(t *testing.T) {
	disk := openwrtArtifacts(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	w, err := LaunchOpenWRT(ctx, OpenWRTOpts{Disk: disk})
	if err != nil {
		t.Fatalf("LaunchOpenWRT: %v", err)
	}
	defer w.Close()

	// READ: wash reconstructs the live gateway (firewall + dhcp + dnsmasq + RA).
	cfg, err := w.Run(ctx, "WASH_NETD_BACKEND=uci washnet-read")
	if err != nil {
		t.Fatalf("washnet-read: %v", err)
	}
	for _, want := range []string{"==== firewall", "option masq '1'", "==== dhcp", "config dnsmasq", "ra 'server'"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("live read missing %q — wash didn't reconstruct the gateway:\n%s", want, cfg)
		}
	}

	// APPLY: a complete, self-consistent network (no dangling firewall refs — the
	// stock fw zone 'wan' refs a not-yet-existing network, which wash's stricter
	// validate rejects, so a read→write of the stock config won't apply; the
	// router test authors its own consistent config, as here). Confirm the
	// running netifd carries it to the kernel.
	authored := "# ==== network ====\n" + strings.Join([]string{
		"config interface 'loopback'", "\toption device 'lo'", "\toption proto 'static'", "\toption ipaddr '127.0.0.1/8'", "",
		"config device", "\toption name 'br-lan'", "\toption type 'bridge'", "\tlist ports 'eth0'", "",
		"config interface 'lan'", "\toption device 'br-lan'", "\toption proto 'static'", "\toption ipaddr '192.168.9.1/24'", "",
	}, "\n")
	if err := w.WriteFile(ctx, "/tmp/cfg", authored); err != nil {
		t.Fatalf("stage config: %v", err)
	}
	out, err := w.Run(ctx, "WASH_NETD_BACKEND=uci washnet-apply /tmp/cfg")
	if err != nil || !strings.Contains(out, "APPLIED") {
		t.Fatalf("washnet-apply: err=%v\n%s", err, out)
	}
	ip, _ := w.Run(ctx, "ip -4 addr show br-lan")
	if !strings.Contains(ip, "192.168.9.1") {
		t.Errorf("netifd did not carry wash's config to the kernel:\n%s", ip)
	}
}
