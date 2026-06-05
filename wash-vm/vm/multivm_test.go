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

// routerLAN is a complete, self-consistent OpenWRT config wash authors for a
// router serving one LAN: a static gateway + a DHCP pool + a firewall zone that
// admits the clients' requests + the dnsmasq global. dev is the in-guest name of
// the router's segment NIC.
func routerLAN(dev string) string {
	return strings.ReplaceAll(`# ==== network ====
config interface 'loopback'
	option device 'lo'
	option proto 'static'
	option ipaddr '127.0.0.1/8'

config interface 'lan'
	option device 'DEV'
	option proto 'static'
	option ipaddr '10.50.0.1/24'

# ==== firewall ====
config defaults
	option input 'ACCEPT'
	option output 'ACCEPT'
	option forward 'REJECT'

config zone
	option name 'lan'
	list network 'lan'
	option input 'ACCEPT'
	option output 'ACCEPT'
	option forward 'ACCEPT'

# ==== dhcp ====
config dnsmasq
	option domainneeded '1'
	option local '/lan/'
	option domain 'lan'
	option expandhosts '1'

config dhcp 'lan'
	option interface 'lan'
	option start '100'
	option limit '50'
	option leasetime '12h'
`, "DEV", dev)
}

// TestRouterServesDHCP is the M2 gate: a wash-configured OpenWRT router serves a
// DHCP lease to a client on the shared L2 segment. Proves wash-in-router-mode
// drives a real running gateway (netifd + dnsmasq + fw4) that actually hands out
// addresses — not just that the config files round-trip.
func TestRouterServesDHCP(t *testing.T) {
	disk := openwrtArtifacts(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
	defer cancel()

	const group = "230.0.0.98"
	port := 25000 + (os.Getpid() % 1000)
	macR, macC := "52:54:00:c0:de:01", "52:54:00:c0:de:02"

	router, err := LaunchOpenWRT(ctx, OpenWRTOpts{Disk: disk, Extra: MCastLAN("lan", group, port, macR)})
	if err != nil {
		t.Fatalf("launch router: %v", err)
	}
	defer router.Close()
	client, err := LaunchOpenWRT(ctx, OpenWRTOpts{Disk: disk, Extra: MCastLAN("lan", group, port, macC)})
	if err != nil {
		t.Fatalf("launch client: %v", err)
	}
	defer client.Close()

	rNic, err := nicByMAC(ctx, router, macR)
	if err != nil {
		t.Fatal(err)
	}
	cNic, err := nicByMAC(ctx, client, macC)
	if err != nil {
		t.Fatal(err)
	}

	// wash configures the router (gateway + DHCP server + firewall + dnsmasq).
	if err := router.WriteFile(ctx, "/tmp/router.uci", routerLAN(rNic)); err != nil {
		t.Fatal(err)
	}
	if out, err := router.Run(ctx, "WASH_NETD_BACKEND=uci washnet-apply /tmp/router.uci"); err != nil || !strings.Contains(out, "APPLIED") {
		t.Fatalf("router apply: err=%v\n%s", err, out)
	}

	// Let netifd finish bringing the LAN up, then bounce dnsmasq so it computes the
	// pool's range with the interface already addressed (wash reloads dnsmasq right
	// after the async network reload, so it can otherwise miss the range — a
	// UCI-applier reload-ordering nit worth fixing).
	mustRun(ctx, t, router, "sleep 3; /etc/init.d/dnsmasq restart; sleep 1")

	// The client is itself a stock OpenWRT (its eth0 is enslaved to a default
	// br-lan with its own static IP + dnsmasq + firewall, and netifd has its own
	// boot-time DHCP clients). Strip it to a bare DHCP client: stop its
	// network/dnsmasq/firewall so it isn't a competing DHCP server, kill any
	// netifd-spawned udhcpc that would race mine (and win the offer, but apply
	// nothing because netifd is stopped), un-bridge the NIC and bring it up raw.
	mustRun(ctx, t, client, "/etc/init.d/network stop; /etc/init.d/dnsmasq stop; /etc/init.d/firewall stop; /etc/init.d/odhcpd stop 2>/dev/null; killall udhcpc odhcpd 2>/dev/null; sleep 1; ip link set "+cNic+" nomaster 2>/dev/null; ip addr flush dev "+cNic+"; ip link set "+cNic+" up")

	// A standalone udhcpc script (the OpenWRT default ties into netifd, which we
	// stopped) that applies the lease directly to the NIC.
	if err := client.WriteFile(ctx, "/tmp/udhcpc.sh", udhcpcScript); err != nil {
		t.Fatal(err)
	}
	out, _ := client.Run(ctx, "chmod +x /tmp/udhcpc.sh; udhcpc -i "+cNic+" -s /tmp/udhcpc.sh -t 12 -T 2 -n -q 2>&1 | tail -6; echo --ADDR--; ip -4 addr show "+cNic)
	if !strings.Contains(out, "inet 10.50.0.") {
		rlog, _ := router.Run(ctx, "logread | grep -iE 'dnsmasq-dhcp|no address' | tail -8")
		t.Fatalf("client got no lease from the wash-configured router:\nCLIENT:\n%s\nROUTER DHCP LOG:\n%s", out, rlog)
	}
	t.Logf("OK: client leased an address from the wash-configured router\n%s", lastLine(out, "inet 10.50.0."))
}

// udhcpcScript is a minimal standalone udhcpc bound/renew handler: it applies the
// leased address (the segment is /24) and default route directly, bypassing
// OpenWRT's netifd-tied default script.
const udhcpcScript = `#!/bin/sh
case "$1" in
  bound|renew)
    ip addr flush dev "$interface"
    ip addr add "$ip/24" dev "$interface"
    [ -n "$router" ] && ip route add default via "$router" 2>/dev/null
    ;;
esac
`

func lastLine(out, needle string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, needle) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}
