package vm

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// nicByMAC resolves the in-guest interface name for a known MAC, robust against
// eth0/eth1 PCI enumeration order. The sentinel is split ("NICRESULT""=") so the
// console's echo of the command never matches the contiguous token in the output.
func nicByMAC(ctx context.Context, w *OpenWRT, mac string) (string, error) {
	cmd := `D=; for i in /sys/class/net/*; do [ "$(cat $i/address)" = "` + mac + `" ] && D=${i##*/}; done; echo "NICRESULT""=$D"`
	out, err := w.Run(ctx, cmd)
	if err != nil {
		return "", err
	}
	i := strings.Index(out, "NICRESULT=")
	if i < 0 {
		return "", fmt.Errorf("no NICRESULT for mac %s:\n%s", mac, out)
	}
	dev := strings.TrimSpace(strings.SplitN(out[i+len("NICRESULT="):], "\n", 2)[0])
	if dev == "" {
		return "", fmt.Errorf("no interface with mac %s", mac)
	}
	return dev, nil
}

func pingOK(out string) bool {
	return strings.Contains(out, "0% packet loss")
}

// TestMultiVMSegment is the M1 gate: two VMs exchange frames over a shared qemu
// `socket` (mcast) L2 segment — a virtual hub pinned to loopback, with NO host
// bridge/tap/root. Proves both untagged and VLAN-tagged traffic crosses the hub
// (the latter is what the router test will trunk between router and clients).
func TestMultiVMSegment(t *testing.T) {
	disk := openwrtArtifacts(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	const group = "230.0.0.99"
	port := 24000 + (os.Getpid() % 1000) // unique-ish per run
	macA, macB := "52:54:00:a1:b2:01", "52:54:00:a1:b2:02"

	a, err := LaunchOpenWRT(ctx, OpenWRTOpts{Disk: disk, Extra: MCastLAN("seg", group, port, macA)})
	if err != nil {
		t.Fatalf("launch A: %v", err)
	}
	defer a.Close()
	b, err := LaunchOpenWRT(ctx, OpenWRTOpts{Disk: disk, Extra: MCastLAN("seg", group, port, macB)})
	if err != nil {
		t.Fatalf("launch B: %v", err)
	}
	defer b.Close()

	nicA, err := nicByMAC(ctx, a, macA)
	if err != nil {
		t.Fatal(err)
	}
	nicB, err := nicByMAC(ctx, b, macB)
	if err != nil {
		t.Fatal(err)
	}

	// Untagged: a flat subnet on the shared segment.
	mustRun(ctx, t, a, "ip addr add 10.99.0.1/24 dev "+nicA+"; ip link set "+nicA+" up")
	mustRun(ctx, t, b, "ip addr add 10.99.0.2/24 dev "+nicB+"; ip link set "+nicB+" up")
	if out, _ := a.Run(ctx, "ping -c3 -W3 10.99.0.2"); !pingOK(out) {
		t.Fatalf("untagged ping across the mcast segment failed:\n%s", out)
	}
	t.Log("OK: untagged frames traverse the socket-L2 hub")

	// VLAN-tagged 20 over the same hub.
	mustRun(ctx, t, a, "modprobe 8021q 2>/dev/null; ip link add link "+nicA+" name v20 type vlan id 20; ip addr add 10.20.0.1/24 dev v20; ip link set v20 up")
	mustRun(ctx, t, b, "modprobe 8021q 2>/dev/null; ip link add link "+nicB+" name v20 type vlan id 20; ip addr add 10.20.0.2/24 dev v20; ip link set v20 up")
	if out, _ := a.Run(ctx, "ping -c3 -W3 10.20.0.2"); !pingOK(out) {
		t.Fatalf("VLAN-20 ping across the mcast segment failed:\n%s", out)
	}
	t.Log("OK: VLAN-tagged frames traverse the hub")
}

func mustRun(ctx context.Context, t *testing.T, w *OpenWRT, cmd string) {
	t.Helper()
	if _, err := w.Run(ctx, cmd); err != nil {
		t.Fatalf("%q: %v", cmd, err)
	}
}
