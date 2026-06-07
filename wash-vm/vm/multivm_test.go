package vm

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/washnet/codec"
	"github.com/sirmick/wash/internal/washnet/model"
	"github.com/sirmick/wash/internal/washnet/segment"
	"github.com/sirmick/wash/internal/washnet/ucibuf"
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
	// Leading space is load-bearing: "100% packet loss" (fully blocked) contains
	// the substring "0% packet loss", but not " 0% packet loss".
	return strings.Contains(out, " 0% packet loss")
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

	// Both VMs are stock OpenWRT (eth0 enslaved to a default br-lan); strip them to
	// bare NICs so we test the hub, not the bridge (a bridged port eats the frames).
	stripToBareNIC(ctx, t, a, nicA)
	stripToBareNIC(ctx, t, b, nicB)

	// Untagged: a flat subnet on the shared segment.
	mustRun(ctx, t, a, "ip addr add 10.99.0.1/24 dev "+nicA)
	mustRun(ctx, t, b, "ip addr add 10.99.0.2/24 dev "+nicB)
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
	// br-lan with its own dnsmasq + firewall + boot-time DHCP clients). Strip it to
	// a bare DHCP client so it isn't a competing DHCP server and its NIC isn't
	// bridged (a bridged port hands the OFFER to br-lan, not to udhcpc).
	stripToBareNIC(ctx, t, client, cNic)

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

// --- M3: full-monty multi-VLAN router (DHCP + DNS + inter-VLAN firewall) ----

func mustLaunch(ctx context.Context, t *testing.T, o OpenWRTOpts) *OpenWRT {
	t.Helper()
	w, err := LaunchOpenWRT(ctx, o)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	return w
}

func mustNIC(ctx context.Context, t *testing.T, w *OpenWRT, mac string) string {
	t.Helper()
	d, err := nicByMAC(ctx, w, mac)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func applyOK(ctx context.Context, t *testing.T, w *OpenWRT, path string) {
	t.Helper()
	if out, err := w.Run(ctx, "WASH_NETD_BACKEND=uci washnet-apply "+path); err != nil || !strings.Contains(out, "APPLIED") {
		t.Fatalf("apply %s: err=%v\n%s", path, err, out)
	}
}

// stripToBareNIC reduces a stock-OpenWRT VM to a bare L2 endpoint: stop its
// network/dnsmasq/firewall (so it's not a competing DHCP/DNS server on the
// segment), kill any netifd-spawned udhcpc that would race a manual one,
// un-bridge the NIC from the default br-lan and bring it up raw.
func stripToBareNIC(ctx context.Context, t *testing.T, w *OpenWRT, nic string) {
	t.Helper()
	mustRun(ctx, t, w, "/etc/init.d/network stop; /etc/init.d/dnsmasq stop; /etc/init.d/firewall stop; /etc/init.d/odhcpd stop 2>/dev/null; killall udhcpc odhcpd 2>/dev/null; sleep 1; "+
		"ip link set "+nic+" nomaster 2>/dev/null; ip addr flush dev "+nic+"; ip link set "+nic+" up")
}

// washClientVLAN strips a stock-OpenWRT VM down to a bare client (see M2), then
// brings up an 802.1q sub-interface so it lives on a single VLAN of the shared
// trunk. Returns the VLAN ifname (eth0.<vid>).
func washClientVLAN(ctx context.Context, t *testing.T, w *OpenWRT, nic, vid string) string {
	t.Helper()
	stripToBareNIC(ctx, t, w, nic)
	vif := nic + "." + vid
	mustRun(ctx, t, w, "modprobe 8021q 2>/dev/null; ip link add link "+nic+" name "+vif+" type vlan id "+vid+" 2>/dev/null; ip link set "+vif+" up")
	return vif
}

// dhcpLease runs a single udhcpc on ifname (sending hostname so the inside
// dnsmasq registers it in DNS) and returns the leased IPv4 the script applied.
func dhcpLease(ctx context.Context, t *testing.T, w *OpenWRT, ifname, hostname string) string {
	t.Helper()
	if err := w.WriteFile(ctx, "/tmp/udhcpc.sh", udhcpcScript); err != nil {
		t.Fatal(err)
	}
	host := ""
	if hostname != "" {
		host = " -x hostname:" + hostname
	}
	out, _ := w.Run(ctx, "chmod +x /tmp/udhcpc.sh; udhcpc -i "+ifname+" -s /tmp/udhcpc.sh"+host+" -t 12 -T 2 -n -q 2>&1 | tail -4; ip -4 addr show "+ifname)
	ip := ipOf(out)
	if ip == "" {
		t.Fatalf("no DHCP lease on %s:\n%s", ifname, out)
	}
	return ip
}

// ipOf extracts the first dotted-quad after "inet " (from `ip addr show`).
func ipOf(out string) string {
	i := strings.Index(out, "inet ")
	if i < 0 {
		return ""
	}
	rest := out[i+len("inet "):]
	if j := strings.IndexAny(rest, "/ \n"); j >= 0 {
		return strings.TrimSpace(rest[:j])
	}
	return ""
}

// routerVLANs is the config wash authors for a two-VLAN router: a tagged
// sub-interface per VLAN on the trunk NIC, each its own static gateway + DHCP
// pool + firewall zone, plus the inside dnsmasq with a static DNS record. When
// interVLAN is set it also opens vlan10<->vlan20 forwarding (otherwise the
// default forward REJECT isolates them). nic is the router's trunk NIC.
func routerVLANs(nic string, interVLAN bool) string {
	fwd := ""
	if interVLAN {
		fwd = "config forwarding\n\toption src 'vlan10'\n\toption dest 'vlan20'\n\n" +
			"config forwarding\n\toption src 'vlan20'\n\toption dest 'vlan10'\n\n"
	}
	cfg := `# ==== network ====
config interface 'loopback'
	option device 'lo'
	option proto 'static'
	option ipaddr '127.0.0.1/8'

config device
	option name 'NIC.10'
	option type '8021q'
	option ifname 'NIC'
	option vid '10'

config device
	option name 'NIC.20'
	option type '8021q'
	option ifname 'NIC'
	option vid '20'

config interface 'v10'
	option device 'NIC.10'
	option proto 'static'
	option ipaddr '10.10.0.1/24'

config interface 'v20'
	option device 'NIC.20'
	option proto 'static'
	option ipaddr '10.20.0.1/24'

# ==== firewall ====
config defaults
	option input 'ACCEPT'
	option output 'ACCEPT'
	option forward 'REJECT'

config zone
	option name 'vlan10'
	list network 'v10'
	option input 'ACCEPT'
	option output 'ACCEPT'
	option forward 'REJECT'

config zone
	option name 'vlan20'
	list network 'v20'
	option input 'ACCEPT'
	option output 'ACCEPT'
	option forward 'REJECT'

` + fwd + `# ==== dhcp ====
config dnsmasq
	option domainneeded '1'
	option local '/lan/'
	option domain 'lan'
	option expandhosts '1'

config dhcp 'v10'
	option interface 'v10'
	option start '100'
	option limit '50'
	option leasetime '12h'

config dhcp 'v20'
	option interface 'v20'
	option start '100'
	option limit '50'
	option leasetime '12h'

config domain
	option name 'nas'
	option ip '10.10.0.5'
`
	return strings.ReplaceAll(cfg, "NIC", nic)
}

// TestRouterVLANsDHCPDNS is the M3 gate — the full-monty router test. A
// wash-configured OpenWRT router trunks two VLANs over a shared L2 segment; a
// client lands in each VLAN and proves, end to end against real
// netifd+dnsmasq+fw4 the UCI applier drives:
//   - DHCP per VLAN (each client leases from its own pool),
//   - inside DNS (a static record AND a client's DHCP-registered hostname,
//     resolved across the gateway),
//   - the inter-VLAN firewall is real: blocked by the default forward REJECT,
//     then permitted once wash adds the zone forwardings — same packet path,
//     only policy changed.
func TestRouterVLANsDHCPDNS(t *testing.T) {
	disk := openwrtArtifacts(t)
	ctx, cancel := context.WithTimeout(context.Background(), 360*time.Second)
	defer cancel()

	const group = "230.0.0.97"
	port := 26000 + (os.Getpid() % 1000)
	macR, macA, macB := "52:54:00:c0:de:10", "52:54:00:c0:de:11", "52:54:00:c0:de:12"

	router := mustLaunch(ctx, t, OpenWRTOpts{Disk: disk, Extra: MCastLAN("trunk", group, port, macR)})
	defer router.Close()
	ca := mustLaunch(ctx, t, OpenWRTOpts{Disk: disk, Extra: MCastLAN("trunk", group, port, macA)})
	defer ca.Close()
	cb := mustLaunch(ctx, t, OpenWRTOpts{Disk: disk, Extra: MCastLAN("trunk", group, port, macB)})
	defer cb.Close()

	rNic := mustNIC(ctx, t, router, macR)
	aNic := mustNIC(ctx, t, ca, macA)
	bNic := mustNIC(ctx, t, cb, macB)

	// wash configures the two-VLAN router (no inter-VLAN forwarding yet).
	if err := router.WriteFile(ctx, "/tmp/r.uci", routerVLANs(rNic, false)); err != nil {
		t.Fatal(err)
	}
	applyOK(ctx, t, router, "/tmp/r.uci")
	// Restart fw4 + dnsmasq once the VLAN sub-interfaces are actually up: both bind
	// to devices at load time, and the applier reloads them right after the async
	// network reload — before netifd has created eth0.10/eth0.20 (the reload-
	// ordering nit). Without this fw4 leaves the VLANs unzoned and forwards freely.
	mustRun(ctx, t, router, "sleep 3; /etc/init.d/firewall restart; /etc/init.d/dnsmasq restart; sleep 1")

	// Each client tags into its VLAN and leases from that VLAN's pool.
	aVif := washClientVLAN(ctx, t, ca, aNic, "10")
	bVif := washClientVLAN(ctx, t, cb, bNic, "20")
	aIP := dhcpLease(ctx, t, ca, aVif, "alice")
	bIP := dhcpLease(ctx, t, cb, bVif, "bob")
	if !strings.HasPrefix(aIP, "10.10.0.") {
		t.Fatalf("client A (vlan10) leased %q, want 10.10.0.x", aIP)
	}
	if !strings.HasPrefix(bIP, "10.20.0.") {
		t.Fatalf("client B (vlan20) leased %q, want 10.20.0.x", bIP)
	}
	t.Logf("OK: DHCP per VLAN — A(vlan10)=%s  B(vlan20)=%s", aIP, bIP)

	// Inside DNS: a static record, and B resolving A's DHCP-registered hostname
	// across its own gateway.
	if out, _ := ca.Run(ctx, "nslookup nas.lan 10.10.0.1"); !strings.Contains(out, "10.10.0.5") {
		t.Fatalf("static DNS record nas.lan -> 10.10.0.5 not resolved:\n%s", out)
	}
	if out, _ := cb.Run(ctx, "nslookup alice.lan 10.20.0.1"); !strings.Contains(out, aIP) {
		t.Fatalf("client hostname alice.lan not resolved to %s by the inside DNS:\n%s", aIP, out)
	}
	t.Log("OK: inside DNS resolves a static record and a client's DHCP hostname")

	// Firewall is real: inter-VLAN is blocked by the default forward REJECT.
	if out, _ := ca.Run(ctx, "ping -c2 -W2 "+bIP); pingOK(out) {
		t.Fatalf("inter-VLAN ping A->B should be blocked by zone forward REJECT:\n%s", out)
	}
	t.Log("OK: inter-VLAN traffic blocked by the default firewall")

	// wash opens vlan10<->vlan20 forwarding; same path, now permitted.
	if err := router.WriteFile(ctx, "/tmp/r2.uci", routerVLANs(rNic, true)); err != nil {
		t.Fatal(err)
	}
	applyOK(ctx, t, router, "/tmp/r2.uci")
	mustRun(ctx, t, router, "/etc/init.d/firewall restart; sleep 2")
	if out, _ := ca.Run(ctx, "ping -c3 -W3 "+bIP); !pingOK(out) {
		t.Fatalf("inter-VLAN ping A->B should pass after wash opens forwarding:\n%s", out)
	}
	t.Logf("OK: inter-VLAN ping %s->%s permitted after wash opened forwarding", aIP, bIP)
}

// multiSegUCI builds a three-segment router config THROUGH the segment lens
// (segment.Materialize) and renders it to UCI — proving the lens write side, the
// same object set the FE's segment-model kernel composes, produces an enforceable
// gateway. trunk is the router's NIC. Policy: trusted→cam one-way (asymmetric);
// guest isolated from both; default forward REJECT.
func multiSegUCI(trunk string) string {
	seg := func(zone, ifn string, vid int, cidr string) segment.Segment {
		dev := fmt.Sprintf("%s.%d", trunk, vid)
		return segment.Segment{
			Name:   ifn,
			Iface:  model.Interface{Name: ifn, Device: dev, Proto: model.StaticProto{IPAddr: []netip.Prefix{netip.MustParsePrefix(cidr)}}},
			Device: &model.Device{Name: dev, Type: "8021q", Ifname: trunk, VID: vid},
			Zone:   &model.Zone{Name: zone, Networks: []string{ifn}, Input: "ACCEPT", Output: "ACCEPT", Forward: "REJECT"},
			Pool:   &model.DHCPPool{Name: ifn, Interface: ifn, Start: 100, Limit: 50, LeaseTime: "12h"},
		}
	}
	proj := segment.Projection{
		Segments: []segment.Segment{
			seg("trusted", "v10", 10, "10.10.0.1/24"),
			seg("guest", "v20", 20, "10.20.0.1/24"),
			seg("cam", "v30", 30, "10.30.0.1/24"),
		},
		Policy: segment.Policy{
			Defaults:    []model.Defaults{{Input: "ACCEPT", Output: "ACCEPT", Forward: "REJECT"}},
			Forwardings: []model.Forwarding{{Src: "trusted", Dest: "cam"}}, // one-way: trusted may reach cam
		},
		Leftovers: model.Config{
			Interfaces: []model.Interface{{Name: "loopback", Device: "lo", Proto: model.StaticProto{IPAddr: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/8")}}}},
			Dnsmasq:    []model.Dnsmasq{{DomainNeeded: true, Local: "/lan/", Domain: "lan", ExpandHosts: true}},
		},
	}
	files, err := codec.Render(segment.Materialize(proj))
	if err != nil {
		panic(err)
	}
	return ucibuf.Marshal(files)
}

// TestRouterMultiSegmentPolicy is the capstone: a wash-configured OpenWRT router
// with THREE segments and DISTINCT per-zone policy — config built through the
// segment lens — drives real netifd/dnsmasq/fw4, and three clients (one per VLAN)
// prove the box enforces it: per-segment DHCP, and the firewall matrix end to end
// — trusted→cam permitted (one-way), cam→trusted blocked, guest isolated from both.
func TestRouterMultiSegmentPolicy(t *testing.T) {
	disk := openwrtArtifacts(t)
	ctx, cancel := context.WithTimeout(context.Background(), 420*time.Second)
	defer cancel()

	const group = "230.0.0.96"
	port := 28000 + (os.Getpid() % 1000)
	macR := "52:54:00:c0:de:20"
	macT, macG, macC := "52:54:00:c0:de:21", "52:54:00:c0:de:22", "52:54:00:c0:de:23"

	router := mustLaunch(ctx, t, OpenWRTOpts{Disk: disk, Extra: MCastLAN("trunk", group, port, macR)})
	defer router.Close()
	ct := mustLaunch(ctx, t, OpenWRTOpts{Disk: disk, Extra: MCastLAN("trunk", group, port, macT)})
	defer ct.Close()
	cg := mustLaunch(ctx, t, OpenWRTOpts{Disk: disk, Extra: MCastLAN("trunk", group, port, macG)})
	defer cg.Close()
	cc := mustLaunch(ctx, t, OpenWRTOpts{Disk: disk, Extra: MCastLAN("trunk", group, port, macC)})
	defer cc.Close()

	// wash configures the three-segment router (via the segment lens).
	if err := router.WriteFile(ctx, "/tmp/seg.uci", multiSegUCI(mustNIC(ctx, t, router, macR))); err != nil {
		t.Fatal(err)
	}
	applyOK(ctx, t, router, "/tmp/seg.uci")
	mustRun(ctx, t, router, "sleep 3; /etc/init.d/firewall restart; /etc/init.d/dnsmasq restart; sleep 1")

	// Each client tags into its VLAN and leases from that segment's pool.
	tIP := dhcpLease(ctx, t, ct, washClientVLAN(ctx, t, ct, mustNIC(ctx, t, ct, macT), "10"), "trusted")
	gIP := dhcpLease(ctx, t, cg, washClientVLAN(ctx, t, cg, mustNIC(ctx, t, cg, macG), "20"), "guest")
	cIP := dhcpLease(ctx, t, cc, washClientVLAN(ctx, t, cc, mustNIC(ctx, t, cc, macC), "30"), "cam")
	if !strings.HasPrefix(tIP, "10.10.0.") {
		t.Fatalf("trusted client leased %q, want 10.10.0.x", tIP)
	}
	if !strings.HasPrefix(gIP, "10.20.0.") {
		t.Fatalf("guest client leased %q, want 10.20.0.x", gIP)
	}
	if !strings.HasPrefix(cIP, "10.30.0.") {
		t.Fatalf("cam client leased %q, want 10.30.0.x", cIP)
	}
	t.Logf("OK: per-segment DHCP — trusted=%s guest=%s cam=%s", tIP, gIP, cIP)

	// The matrix is enforced. trusted→cam is permitted (the one forwarding); the
	// reply rides conntrack, so the ping completes.
	if out, _ := ct.Run(ctx, "ping -c3 -W3 "+cIP); !pingOK(out) {
		t.Fatalf("trusted→cam should be permitted (forwarding trusted→cam):\n%s", out)
	}
	t.Log("OK: trusted→cam permitted")

	// cam→trusted is blocked: no reverse forwarding, and cam initiates (a new flow).
	if out, _ := cc.Run(ctx, "ping -c2 -W2 "+tIP); pingOK(out) {
		t.Fatalf("cam→trusted should be blocked (no reverse forwarding) — asymmetric policy:\n%s", out)
	}
	t.Log("OK: cam→trusted blocked (asymmetric)")

	// guest is isolated from both LANs.
	if out, _ := cg.Run(ctx, "ping -c2 -W2 "+tIP); pingOK(out) {
		t.Fatalf("guest→trusted should be blocked:\n%s", out)
	}
	if out, _ := cg.Run(ctx, "ping -c2 -W2 "+cIP); pingOK(out) {
		t.Fatalf("guest→cam should be blocked:\n%s", out)
	}
	t.Logf("OK: guest isolated — multi-segment per-zone policy enforced on a real router")
}
