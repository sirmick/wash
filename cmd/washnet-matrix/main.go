// Command washnet-matrix is the real accessibility-matrix test for a wash/OpenWRT
// segmented router (the wash analogue of the Proxmox test-net.py). It boots a
// router microVM (WAN on qemu user-net → real internet; one VLAN-filtered bridge
// over per-segment access ports) plus a probe microVM on each segment, leases each
// probe by DHCP, then from every probe pings a target set and prints + asserts the
// accessibility matrix.
//
// Topology: every segment is an untagged access port on its own socket-mcast L2
// (so probes need no 802.1q) — the router's br-lan does the VLAN filtering + the
// inter-VLAN routing + the firewall, which is exactly what the matrix tests.
//
//	washnet-matrix --image out/vm/openwrt.img
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/sirmick/wash/wash-vm/vm"
)

// markers the guest prints; matched by regex so the echoed command line (which
// contains the literal "$A"/"$?") never matches — only the substituted result.
var (
	leaseRe  = regexp.MustCompile(`LEASE=(\d+\.\d+\.\d+\.\d+)`)
	resultRe = regexp.MustCompile(`RESULT:(\d+)`)
)

type seg struct {
	name string
	vid  int
	gw   string // router IP on this segment
}

var segs = []seg{
	{"lan", 10, "10.10.0.1"},
	{"iot", 20, "10.20.0.1"},
	{"cam", 30, "10.30.0.1"},
}

var internet = [][2]string{{"internet", "1.1.1.1"}}

var probeAddr = map[string]string{} // segment -> leased probe IP

func main() {
	image := flag.String("image", "out/vm/openwrt.img", "OpenWRT image (with the washnet build)")
	base := flag.Int("base-port", 27300, "base socket-mcast port (one L2 per segment)")
	flag.Parse()
	if _, err := os.Stat(*image); err != nil {
		die("image not found: %s", *image)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Each segment is an isolated point-to-point socket link (the router listens,
	// the probe connects) — a direct L2 wire, no multicast echo to confuse the
	// VLAN bridge. Untagged probes; the router's br-lan does the VLAN filtering.
	fmt.Println("booting the segmented router …")
	nics := []string{"-netdev", "user,id=wan", "-device", "virtio-net-pci,netdev=wan,mac=52:54:00:bb:00:01"}
	for i := range segs {
		nics = append(nics,
			"-netdev", fmt.Sprintf("socket,id=a%d,listen=127.0.0.1:%d", i, *base+i),
			"-device", fmt.Sprintf("virtio-net-pci,netdev=a%d,mac=52:54:00:bb:00:%02x", i, i+2))
	}
	router, err := vm.LaunchOpenWRT(ctx, vm.OpenWRTOpts{Disk: *image, Mem: "256M", Extra: nics})
	if err != nil {
		die("router launch: %v", err)
	}
	defer router.Close()
	applyRouter(ctx, router)
	fmt.Println("  • router configured (lan/iot/cam VLANs, NAT to real internet)")

	// one probe per segment, on that segment's access L2.
	probes := map[string]*vm.OpenWRT{}
	for i, s := range segs {
		fmt.Printf("booting probe %s (VLAN %d) …\n", s.name, s.vid)
		pn := []string{
			"-netdev", fmt.Sprintf("socket,id=p,connect=127.0.0.1:%d", *base+i),
			"-device", fmt.Sprintf("virtio-net-pci,netdev=p,mac=52:54:00:bb:01:%02x", i+1),
		}
		p, err := vm.LaunchOpenWRT(ctx, vm.OpenWRTOpts{Disk: *image, Mem: "192M", Extra: pn})
		if err != nil {
			die("probe %s launch: %v", s.name, err)
		}
		defer p.Close()
		probes[s.name] = p
		fmt.Printf("  • %s probe leased %s\n", s.name, probeUp(ctx, p, s))
	}

	// debug the lan link before the full matrix.
	if d, _ := probes[segs[0].name].Run(ctx, "echo DBG:; ip -br link show eth0; ip -br -4 addr show eth0; ip route show; ping -c2 -W2 "+segs[0].gw+" 2>&1 | tail -3; echo GW=$?"); true {
		fmt.Printf("--- %s probe link ---\n%s\n", segs[0].name, trimDiag(d))
	}
	if d, _ := router.Run(ctx, "echo DBG:; ip -br link show | grep -E 'eth1|br-lan'"); true {
		fmt.Printf("--- router ports ---\n%s\n", trimDiag(d))
	}

	fmt.Println("\nrunning the accessibility matrix …")
	targets := matrixTargets()
	result := map[string]map[string]bool{}
	for _, s := range segs {
		result[s.name] = pingAll(ctx, probes[s.name], targets)
	}
	printMatrix(targets, result)
	os.Exit(boolToCode(assertPolicy(result)))
}

func matrixTargets() [][2]string {
	t := append([][2]string{}, internet...)
	for _, s := range segs {
		t = append(t, [2]string{s.name + "-gw", s.gw})
	}
	for _, s := range segs {
		t = append(t, [2]string{s.name + "-probe", probeAddr[s.name]})
	}
	return t
}

func applyRouter(ctx context.Context, w *vm.OpenWRT) {
	var b strings.Builder
	// Clear the STOCK firewall zones/forwardings first — OpenWRT ships lan+wan
	// zones, so re-adding our own would duplicate the names and fw4 would refuse
	// to load (→ kernel default-drop). These are shell loops, before the uci batch.
	b.WriteString("while uci -q delete firewall.@zone[0]; do :; done\n")
	b.WriteString("while uci -q delete firewall.@forwarding[0]; do :; done\n")
	b.WriteString("uci batch <<'UCI'\n")
	b.WriteString("delete network.@device[0]\ndelete network.lan\ndelete network.wan\ndelete network.wan6\n")
	b.WriteString("set network.wan=interface\nset network.wan.device='eth0'\nset network.wan.proto='dhcp'\n")
	b.WriteString("set network.brlan=device\nset network.brlan.name='br-lan'\nset network.brlan.type='bridge'\nset network.brlan.vlan_filtering='1'\n")
	for i := range segs {
		fmt.Fprintf(&b, "add_list network.brlan.ports='eth%d'\n", i+1)
	}
	for i, s := range segs {
		fmt.Fprintf(&b, "set network.v%d=bridge-vlan\nset network.v%d.device='br-lan'\nset network.v%d.vlan='%d'\nadd_list network.v%d.ports='eth%d:u*'\n", s.vid, s.vid, s.vid, s.vid, s.vid, i+1)
		fmt.Fprintf(&b, "set network.%s=interface\nset network.%s.device='br-lan.%d'\nset network.%s.proto='static'\nset network.%s.ipaddr='%s'\nset network.%s.netmask='255.255.255.0'\n", s.name, s.name, s.vid, s.name, s.name, s.gw, s.name)
		fmt.Fprintf(&b, "set dhcp.%s=dhcp\nset dhcp.%s.interface='%s'\nset dhcp.%s.start='100'\nset dhcp.%s.limit='150'\nset dhcp.%s.leasetime='12h'\n", s.name, s.name, s.name, s.name, s.name, s.name)
		fmt.Fprintf(&b, "set firewall.z%s=zone\nset firewall.z%s.name='%s'\nadd_list firewall.z%s.network='%s'\nset firewall.z%s.input='ACCEPT'\nset firewall.z%s.output='ACCEPT'\nset firewall.z%s.forward='REJECT'\n", s.name, s.name, s.name, s.name, s.name, s.name, s.name, s.name)
	}
	b.WriteString("set firewall.zwan=zone\nset firewall.zwan.name='wan'\nadd_list firewall.zwan.network='wan'\nset firewall.zwan.input='REJECT'\nset firewall.zwan.output='ACCEPT'\nset firewall.zwan.forward='REJECT'\nset firewall.zwan.masq='1'\nset firewall.zwan.mtu_fix='1'\n")
	fwd := [][2]string{{"lan", "wan"}, {"lan", "iot"}, {"lan", "cam"}, {"iot", "wan"}}
	for i, f := range fwd {
		fmt.Fprintf(&b, "set firewall.f%d=forwarding\nset firewall.f%d.src='%s'\nset firewall.f%d.dest='%s'\n", i, i, f[0], i, f[1])
	}
	// network restart is ASYNC — fw4 must reload only AFTER the VLAN L3 devices
	// exist, else the zones bind to nothing and input falls to default-drop (the
	// known UCI-applier reload-ordering bug). Wait for br-lan.10, then reload fw4.
	b.WriteString("UCI\nuci commit\n/etc/init.d/network restart >/dev/null 2>&1\n")
	b.WriteString("for i in $(seq 1 20); do ip link show br-lan.10 >/dev/null 2>&1 && break; sleep 1; done\nsleep 2\n")
	b.WriteString("/etc/init.d/firewall restart >/dev/null 2>&1\nsleep 3\necho APPLIED\n")
	out, err := w.Run(ctx, b.String())
	if err != nil || !strings.Contains(out, "APPLIED") {
		die("router apply failed: %v\n%s", err, out)
	}
	// wait until the router itself can reach the internet (WAN DHCP + NAT up).
	for i := 0; i < 25; i++ {
		o, _ := w.Run(ctx, "ping -c1 -W2 8.8.8.8 >/dev/null 2>&1; echo RESULT:$?")
		if m := resultRe.FindStringSubmatch(o); m != nil && m[1] == "0" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	// diagnostic: forwardings + masq loaded? + ip_forward + a forward count.
	d, _ := w.Run(ctx, "echo DIAG:; echo ipfwd=$(cat /proc/sys/net/ipv4/ip_forward); echo fwds=$(uci show firewall | grep -c =forwarding); echo masq=$(nft list table inet fw4 2>/dev/null | grep -c masquerade); echo fwdrules=$(nft list table inet fw4 2>/dev/null | grep -cE 'jump forward|accept')")
	if i := strings.Index(d, "DIAG:"); i >= 0 {
		fmt.Printf("  router: %s\n", strings.Join(strings.Fields(d[i+5:]), " "))
	}
}

func probeUp(ctx context.Context, w *vm.OpenWRT, s seg) string {
	// stock OpenWRT enslaves eth0 into its own br-lan — detach it, then statically
	// address the bare port on this segment (untagged access into its VLAN). Static
	// rather than DHCP keeps the test deterministic; the matrix measures routing.
	ip := strings.TrimSuffix(s.gw, ".1") + ".50"
	script := fmt.Sprintf(`
/etc/init.d/network stop >/dev/null 2>&1
/etc/init.d/dnsmasq stop >/dev/null 2>&1
/etc/init.d/firewall stop >/dev/null 2>&1
ip link set eth0 nomaster 2>/dev/null
ip addr flush dev eth0 2>/dev/null
ip link set eth0 up 2>/dev/null
ip addr add %s/24 dev eth0 2>/dev/null
ip route add default via %s 2>/dev/null
echo "LEASE=%s end"
`, ip, s.gw, ip)
	if _, err := w.Run(ctx, script); err != nil {
		die("probe %s up: %v", s.name, err)
	}
	probeAddr[s.name] = ip
	return ip
}

func pingAll(ctx context.Context, w *vm.OpenWRT, targets [][2]string) map[string]bool {
	res := map[string]bool{}
	for _, t := range targets {
		if t[1] == "" || strings.Contains(t[1], "lease") {
			res[t[0]] = false
			continue
		}
		// internet via TCP (qemu slirp NATs TCP, not ICMP); everything else by ping
		// (-c3 — the first packet is often lost to ARP; exit 0 if any reply).
		cmd := fmt.Sprintf("ping -c3 -W1 %s >/dev/null 2>&1; echo RESULT:$?", t[1])
		if strings.HasPrefix(t[0], "internet") {
			cmd = fmt.Sprintf("wget -q -T4 -O /dev/null http://%s/ 2>/dev/null; echo RESULT:$?", t[1])
		}
		out, _ := w.Run(ctx, cmd)
		m := resultRe.FindStringSubmatch(out)
		res[t[0]] = m != nil && m[1] == "0"
	}
	return res
}

func printMatrix(targets [][2]string, result map[string]map[string]bool) {
	fmt.Printf("\n%-22s", "target \\ probe")
	for _, s := range segs {
		fmt.Printf("%-8s", s.name)
	}
	fmt.Printf("\n%s\n", strings.Repeat("─", 22+8*len(segs)))
	for _, t := range targets {
		fmt.Printf("%-22s", t[0])
		for _, s := range segs {
			if result[s.name][t[0]] {
				fmt.Printf("%-8s", "✓")
			} else {
				fmt.Printf("%-8s", "✗")
			}
		}
		fmt.Println()
	}
}

func assertPolicy(r map[string]map[string]bool) bool {
	ok := true
	check := func(name string, cond bool) {
		s := "PASS"
		if !cond {
			s, ok = "FAIL", false
		}
		fmt.Printf("  [%s] %s\n", s, name)
	}
	fmt.Println("\nassertions:")
	check("lan reaches the internet", r["lan"]["internet-v4"])
	check("iot reaches the internet", r["iot"]["internet-v4"])
	check("cam is quarantined from the internet", !r["cam"]["internet-v4"])
	// isolation is a FORWARD property → test probe→probe, not the gateway (a
	// gateway ping is router INPUT, allowed by every zone's input policy).
	check("lan can reach a cam host (view cameras)", r["lan"]["cam-probe"])
	check("iot cannot reach a lan host", !r["iot"]["lan-probe"])
	check("iot cannot reach a cam host", !r["iot"]["cam-probe"])
	check("cam cannot reach a lan host", !r["cam"]["lan-probe"])
	return ok
}


func boolToCode(ok bool) int {
	if ok {
		fmt.Println("\nMATRIX OK — the segmentation policy holds.")
		return 0
	}
	fmt.Println("\nMATRIX FAILED — policy violation above.")
	return 1
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "washnet-matrix: "+format+"\n", a...)
	os.Exit(2)
}

func trimDiag(s string) string {
	if i := strings.Index(s, "DBG:"); i >= 0 {
		s = s[i+4:]
	}
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r")
		if t := strings.TrimSpace(l); t != "" && !strings.Contains(t, "WASHMK") && !strings.HasPrefix(t, "echo ") && !strings.HasPrefix(t, "ip ") && !strings.HasPrefix(t, "ping ") {
			out = append(out, "    "+t)
		}
	}
	return strings.Join(out, "\n")
}
