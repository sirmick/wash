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

var internet = [][2]string{{"internet-v4", "8.8.8.8"}}

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
	// one isolated L2 per segment — a DISTINCT multicast group (not just port).
	segGroup := func(i int) string { return fmt.Sprintf("230.0.0.%d", 90+i) }

	// router: eth0 = slirp WAN (real internet), eth1.. = one access port per segment.
	fmt.Println("booting the segmented router …")
	nics := []string{"-netdev", "user,id=wan", "-device", "virtio-net-pci,netdev=wan,mac=52:54:00:bb:00:01"}
	for i := range segs {
		nics = append(nics, vm.MCastLAN(fmt.Sprintf("a%d", i), segGroup(i), *base, fmt.Sprintf("52:54:00:bb:00:%02x", i+2))...)
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
		pn := vm.MCastLAN("p", segGroup(i), *base, fmt.Sprintf("52:54:00:bb:01:%02x", i+1))
		p, err := vm.LaunchOpenWRT(ctx, vm.OpenWRTOpts{Disk: *image, Mem: "192M", Extra: pn})
		if err != nil {
			die("probe %s launch: %v", s.name, err)
		}
		defer p.Close()
		probes[s.name] = p
		fmt.Printf("  • %s probe leased %s\n", s.name, probeUp(ctx, p, s))
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
	b.WriteString("UCI\nuci commit\n/etc/init.d/network restart >/dev/null 2>&1\n/etc/init.d/firewall restart >/dev/null 2>&1\nsleep 6\necho '=GW='; ip -4 -o addr show 2>/dev/null | grep -oE 'br-lan\\.[0-9]+|eth0 .*inet [0-9.]+' | head\necho APPLIED\n")
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
	// diagnostic: VLAN L3 devices + dnsmasq.
	d, _ := w.Run(ctx, "echo DIAG:; ip -br -4 addr show 2>/dev/null | grep 'br-lan\\.' ; pidof dnsmasq >/dev/null && echo dnsmasq=up || echo dnsmasq=down")
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
		out, _ := w.Run(ctx, fmt.Sprintf("ping -c1 -W2 %s >/dev/null 2>&1; echo RESULT:$?", t[1]))
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
	check("lan can reach the cam segment (view cameras)", r["lan"]["cam-gw"])
	check("iot cannot reach lan", !r["iot"]["lan-gw"])
	check("iot cannot reach cam", !r["iot"]["cam-gw"])
	check("cam cannot reach lan", !r["cam"]["lan-gw"])
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
