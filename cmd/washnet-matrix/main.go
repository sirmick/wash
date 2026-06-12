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
	// Write the three config files WHOLESALE (replacing OpenWRT's stock files
	// entirely) rather than mutating — no stock-zone collisions, and the config is
	// the same shape the wash net app emits. Then apply with ordered reloads.
	writeFile(ctx, w, "network", routerNetwork())
	writeFile(ctx, w, "firewall", routerFirewall())
	writeFile(ctx, w, "dhcp", routerDHCP())
	// network restart is ASYNC — fw4 must reload only AFTER the VLAN L3 devices
	// exist, else the zones bind to nothing and input falls to default-drop (the
	// known UCI-applier reload-ordering bug). Wait for br-lan.10, then reload fw4.
	out, err := w.Run(ctx, "/etc/init.d/network restart >/dev/null 2>&1\n"+
		"for i in $(seq 1 20); do ip link show br-lan.10 >/dev/null 2>&1 && break; sleep 1; done\nsleep 2\n"+
		"/etc/init.d/firewall restart >/dev/null 2>&1\nsleep 3\necho APPLIED")
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

// writeFile drops a UCI config file wholesale into the guest (heredoc, quoted
// delimiter so nothing expands), replacing OpenWRT's stock one.
func writeFile(ctx context.Context, w *vm.OpenWRT, name, content string) {
	out, err := w.Run(ctx, fmt.Sprintf("cat > /etc/config/%s <<'CFGEOF'\n%s\nCFGEOF\necho WROTE=%s", name, content, name))
	if err != nil || !strings.Contains(out, "WROTE="+name) {
		die("write /etc/config/%s: %v\n%s", name, err, out)
	}
}

func routerNetwork() string {
	var b strings.Builder
	b.WriteString("config interface 'loopback'\n\toption device 'lo'\n\toption proto 'static'\n\toption ipaddr '127.0.0.1'\n\toption netmask '255.0.0.0'\n\n")
	b.WriteString("config interface 'wan'\n\toption device 'eth0'\n\toption proto 'dhcp'\n\n")
	b.WriteString("config device\n\toption name 'br-lan'\n\toption type 'bridge'\n\toption vlan_filtering '1'\n")
	for i := range segs {
		fmt.Fprintf(&b, "\tlist ports 'eth%d'\n", i+1)
	}
	b.WriteString("\n")
	for i, s := range segs {
		fmt.Fprintf(&b, "config bridge-vlan\n\toption device 'br-lan'\n\toption vlan '%d'\n\tlist ports 'eth%d:u*'\n\n", s.vid, i+1)
	}
	for _, s := range segs {
		fmt.Fprintf(&b, "config interface '%s'\n\toption device 'br-lan.%d'\n\toption proto 'static'\n\toption ipaddr '%s'\n\toption netmask '255.255.255.0'\n\n", s.name, s.vid, s.gw)
	}
	return b.String()
}

func routerFirewall() string {
	var b strings.Builder
	b.WriteString("config defaults\n\toption syn_flood '1'\n\toption input 'REJECT'\n\toption output 'ACCEPT'\n\toption forward 'REJECT'\n\n")
	for _, s := range segs {
		fmt.Fprintf(&b, "config zone\n\toption name '%s'\n\tlist network '%s'\n\toption input 'ACCEPT'\n\toption output 'ACCEPT'\n\toption forward 'REJECT'\n\n", s.name, s.name)
	}
	b.WriteString("config zone\n\toption name 'wan'\n\tlist network 'wan'\n\toption input 'REJECT'\n\toption output 'ACCEPT'\n\toption forward 'REJECT'\n\toption masq '1'\n\toption mtu_fix '1'\n\n")
	// policy: lan→wan/iot/cam, iot→wan; cam→nothing (quarantined incl. internet)
	for _, f := range [][2]string{{"lan", "wan"}, {"lan", "iot"}, {"lan", "cam"}, {"iot", "wan"}} {
		fmt.Fprintf(&b, "config forwarding\n\toption src '%s'\n\toption dest '%s'\n\n", f[0], f[1])
	}
	return b.String()
}

func routerDHCP() string {
	var b strings.Builder
	b.WriteString("config dnsmasq\n\toption domainneeded '1'\n\toption localise_queries '1'\n\toption rebind_protection '1'\n\toption local '/lan/'\n\toption domain 'lan'\n\toption expandhosts '1'\n\toption authoritative '1'\n\toption leasefile '/tmp/dhcp.leases'\n\n")
	for _, s := range segs {
		fmt.Fprintf(&b, "config dhcp '%s'\n\toption interface '%s'\n\toption start '100'\n\toption limit '150'\n\toption leasetime '12h'\n\n", s.name, s.name)
	}
	return b.String()
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
	// Wait until the gateway answers (the point-to-point link + router firewall
	// have settled) — kills the run-to-run flakiness of pinging too early.
	for i := 0; i < 15; i++ {
		o, _ := w.Run(ctx, fmt.Sprintf("ping -c1 -W1 %s >/dev/null 2>&1; echo RESULT:$?", s.gw))
		if m := resultRe.FindStringSubmatch(o); m != nil && m[1] == "0" {
			break
		}
		time.Sleep(2 * time.Second)
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
