// Command washnet-matrix is the real accessibility-matrix test for a wash/OpenWRT
// segmented router (the wash analogue of the Proxmox test-net.py). It boots a
// router microVM (WAN on qemu user-net → real internet; one VLAN-filtered bridge
// over per-segment access ports) plus a probe microVM on each segment, leases each
// probe by DHCP, then from every probe pings a target set and prints + asserts the
// accessibility matrix.
//
// Topology: every segment is an untagged access port on its own UDP point-to-point
// link (dgram netdev, so probes need no 802.1q) — the router's br-lan does the VLAN
// filtering + the inter-VLAN routing + the firewall, which is exactly what the
// matrix tests.
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
	"strconv"
	"strings"
	"sync"
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

// dgramLink wires a virtio NIC to a connectionless UDP point-to-point link on
// loopback: this end binds localPort and sends to remotePort; the peer mirrors
// it. Unlike mcast hubs, UDP unicast never reflects a sender's own frames — a
// bridged router (br-lan) would otherwise receive its own flooded broadcasts
// ("received packet with own address as source"), storm, and drop real traffic.
// Unlike listen=/connect= sockets, there's no handshake, so no boot-order race
// leaves a link dead. localPort/remotePort must be a unique swapped pair per
// segment; mac unique per VM; id unique per NIC within a VM.
func dgramLink(id string, localPort, remotePort int, mac string) []string {
	return []string{
		"-netdev", fmt.Sprintf("dgram,id=%s,local.type=inet,local.host=127.0.0.1,local.port=%d,remote.type=inet,remote.host=127.0.0.1,remote.port=%d", id, localPort, remotePort),
		"-device", "virtio-net-pci,netdev=" + id + ",mac=" + mac,
	}
}

// segPorts returns the (router-side, probe-side) UDP ports for segment i — two
// distinct ports per segment, swapped between the two ends of the link.
func segPorts(base, i int) (int, int) { return base + 2*i, base + 2*i + 1 }

var probeAddr = map[string]string{} // segment -> leased probe IP

// Every launched VM is tracked so closeAll() can reap them on ANY exit path —
// os.Exit (die / the final assert exit) skips deferred Close(), which is how a
// failed or signalled run leaked qemu and starved the host over repeated runs.
var (
	vmsMu sync.Mutex
	vms   []*vm.OpenWRT
)

func track(w *vm.OpenWRT) *vm.OpenWRT {
	vmsMu.Lock()
	vms = append(vms, w)
	vmsMu.Unlock()
	return w
}

func closeAll() {
	vmsMu.Lock()
	defer vmsMu.Unlock()
	for _, w := range vms {
		w.Close()
	}
}

func main() {
	image := flag.String("image", "out/vm/openwrt.img", "OpenWRT image (with the washnet build)")
	base := flag.Int("base-port", 27300, "base UDP port; segment i uses ports base+2i (router) / base+2i+1 (probe)")
	flag.Parse()
	if _, err := os.Stat(*image); err != nil {
		die("image not found: %s", *image)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	t0 := time.Now()
	el := func() string { return fmt.Sprintf("[+%3ds]", int(time.Since(t0).Seconds())) }

	// Reap any qemu leaked by a prior run (a SIGKILL'd harness never runs its
	// defer Close) — they hold the UDP sockets and slow/OOM the host otherwise.
	if n := reapStaleQEMU(); n > 0 {
		fmt.Printf("reaped %d stale qemu process(es) from a prior run\n", n)
		time.Sleep(time.Second) // let the kernel release their sockets
	}

	// Each segment is an isolated UDP point-to-point link (dgram netdev) — untagged
	// probes; the router's br-lan does the VLAN filtering + inter-VLAN routing +
	// firewall, which is what the matrix tests. UDP unicast (vs an mcast hub) keeps
	// the bridge from receiving its own flooded broadcasts, and is connectionless
	// (vs listen=/connect=) so no boot-order race leaves a probe's link dead.
	fmt.Println("booting the segmented router …")
	nics := []string{"-netdev", "user,id=wan", "-device", "virtio-net-pci,netdev=wan,mac=52:54:00:bb:00:01"}
	for i := range segs {
		rp, pp := segPorts(*base, i)
		nics = append(nics, dgramLink(fmt.Sprintf("a%d", i), rp, pp,
			fmt.Sprintf("52:54:00:bb:00:%02x", i+2))...)
	}
	router, err := vm.LaunchOpenWRT(ctx, vm.OpenWRTOpts{Disk: *image, Mem: "256M", Extra: nics})
	if err != nil {
		die("router launch: %v", err)
	}
	track(router)
	hasInternet := applyRouter(ctx, router)
	fmt.Printf("  • router configured (lan/iot/cam VLANs, NAT to real internet) %s\n", el())

	// Boot + bring up all probes CONCURRENTLY — each VM has its own serial conn,
	// so cross-VM Run() is safe (within one VM it is not). Sequential boots cost
	// up to 90s × 3; concurrent is one boot wide. Each goroutine touches only its
	// own probe; shared maps are written under the mutex after the per-VM work.
	fmt.Println("booting 3 probes concurrently …")
	probes := map[string]*vm.OpenWRT{}
	var pmu sync.Mutex
	var pwg sync.WaitGroup
	perr := make([]error, len(segs))
	for i, s := range segs {
		pwg.Add(1)
		go func(i int, s seg) {
			defer pwg.Done()
			rp, pp := segPorts(*base, i)
			pn := dgramLink("p", pp, rp, fmt.Sprintf("52:54:00:bb:01:%02x", i+1))
			p, err := vm.LaunchOpenWRT(ctx, vm.OpenWRTOpts{Disk: *image, Mem: "192M", Extra: pn})
			if err != nil {
				perr[i] = fmt.Errorf("probe %s launch: %w", s.name, err)
				return
			}
			track(p)
			ip := probeUp(ctx, p, s)
			pmu.Lock()
			probes[s.name] = p
			probeAddr[s.name] = ip
			pmu.Unlock()
			fmt.Printf("  • %s probe leased %s %s\n", s.name, ip, el())
		}(i, s)
	}
	pwg.Wait()
	for _, e := range perr {
		if e != nil {
			die("%v", e)
		}
	}

	// debug the lan link before the full matrix (probe shells are never wedged; the
	// router's may be, from the internet probe, so we don't Run it here).
	if d, _ := probes[segs[0].name].Run(ctx, "echo DBG:; ip -br link show eth0; ip -br -4 addr show eth0; ping -c2 -W2 "+segs[0].gw+" >/dev/null 2>&1; echo GW=$?; echo NEIGH:; ip neigh show"); true {
		fmt.Printf("--- %s probe link ---\n%s\n", segs[0].name, trimDiag(d))
	}

	fmt.Printf("\nrunning the accessibility matrix … %s\n", el())
	targets := matrixTargets()
	// Each probe drives its own serial console, so probe the three concurrently.
	result := map[string]map[string]bool{}
	var rmu sync.Mutex
	var rwg sync.WaitGroup
	for _, s := range segs {
		rwg.Add(1)
		go func(s seg) {
			defer rwg.Done()
			r := pingAll(ctx, probes[s.name], targets)
			rmu.Lock()
			result[s.name] = r
			rmu.Unlock()
		}(s)
	}
	rwg.Wait()
	fmt.Printf("matrix complete %s\n", el())
	printMatrix(targets, result)
	code := boolToCode(assertPolicy(result, hasInternet))
	closeAll()
	os.Exit(code)
}

func matrixTargets() [][2]string {
	var t [][2]string
	for _, s := range segs {
		t = append(t, [2]string{s.name + "-gw", s.gw})
	}
	for _, s := range segs {
		t = append(t, [2]string{s.name + "-probe", probeAddr[s.name]})
	}
	// internet LAST: it's the only cell that can hang (an allowed probe's wget to a
	// black-holed WAN — busybox wget's -T doesn't bound the connect). Probing it
	// last means a hang can't wedge the probe's shell for any later cell.
	t = append(t, internet...)
	return t
}

// applyRouter writes the wash router config and applies it, returning whether the
// router itself can reach the internet over its WAN (so the caller knows whether
// the lan/iot internet-reachability assertions are meaningful in this environment).
func applyRouter(ctx context.Context, w *vm.OpenWRT) (hasInternet bool) {
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
	// diagnostic FIRST (all local, never hangs): forwardings + masq + ip_forward.
	d, _ := w.Run(ctx, "echo DIAG:; echo ipfwd=$(cat /proc/sys/net/ipv4/ip_forward); echo fwds=$(uci show firewall | grep -c =forwarding); echo masq=$(nft list table inet fw4 2>/dev/null | grep -c masquerade); echo fwdrules=$(nft list table inet fw4 2>/dev/null | grep -cE 'jump forward|accept')")
	// Then a SINGLE internet probe, LAST: over TCP not ICMP (qemu slirp NATs TCP
	// but not ICMP, so ping 8.8.8.8 can never work through the WAN), bounded by a Go
	// deadline (OpenWRT busybox has no `timeout` applet). It's last because a hung
	// wget (black-holed WAN) leaves the router shell wedged — nothing Runs on the
	// router after this, so that's harmless.
	wctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	o, _ := w.Run(wctx, "wget -q -T4 -O /dev/null http://1.1.1.1/ 2>/dev/null; echo RESULT:$?")
	cancel()
	if m := resultRe.FindStringSubmatch(o); m != nil && m[1] == "0" {
		hasInternet = true
	}
	if i := strings.Index(d, "DIAG:"); i >= 0 {
		fmt.Printf("  router: %s internet=%t\n", strings.Join(strings.Fields(d[i+5:]), " "), hasInternet)
	}
	return hasInternet
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
	return ip // caller records probeAddr under its mutex (concurrent probe bring-up)
}

func pingAll(ctx context.Context, w *vm.OpenWRT, targets [][2]string) map[string]bool {
	res := map[string]bool{}
	for _, t := range targets {
		if t[1] == "" || strings.Contains(t[1], "lease") {
			res[t[0]] = false
			continue
		}
		// internet via TCP (qemu slirp NATs TCP, not ICMP); everything else by ping
		// with -w3 as a hard overall deadline (busybox ignores -W for a black-holed
		// target otherwise). OpenWRT busybox has no `timeout` applet, so each cell
		// gets a per-cell Go deadline: a w.Run timeout does NOT kill the in-guest
		// command, but internet is probed LAST (see matrixTargets) so a hung wget
		// can't wedge any later cell.
		cmd := fmt.Sprintf("ping -c2 -W1 -w3 %s >/dev/null 2>&1; echo RESULT:$?", t[1])
		if strings.HasPrefix(t[0], "internet") {
			cmd = fmt.Sprintf("wget -q -T4 -O /dev/null http://%s/ 2>/dev/null; echo RESULT:$?", t[1])
		}
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		out, _ := w.Run(cctx, cmd)
		cancel()
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

func assertPolicy(r map[string]map[string]bool, hasInternet bool) bool {
	ok := true
	check := func(name string, cond bool) {
		s := "PASS"
		if !cond {
			s, ok = "FAIL", false
		}
		fmt.Printf("  [%s] %s\n", s, name)
	}
	fmt.Println("\nassertions:")
	// lan/iot internet reachability needs a WAN that actually NATs to the internet
	// (qemu slirp + a host with egress). When the router itself couldn't reach the
	// internet, SKIP those two — don't fail the segmentation gate on the
	// environment. cam-quarantine + the isolation checks are pure routing/firewall
	// and always run (cam can't even forward to wan, internet or not).
	if hasInternet {
		check("lan reaches the internet", r["lan"]["internet"])
		check("iot reaches the internet", r["iot"]["internet"])
	} else {
		fmt.Println("  [SKIP] lan/iot reach the internet (router has no WAN egress here)")
	}
	check("cam is quarantined from the internet", !r["cam"]["internet"])
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

// reapStaleQEMU SIGKILLs every qemu-system-x86_64 whose command line references a
// wash VM scratch dir (the "wash-owrt-" temp prefix used by vm.LaunchOpenWRT), so
// VMs leaked by a previously-killed harness (or net-demo) don't hold our UDP
// sockets / leak memory. Only our own VMs match — unrelated qemu is left alone.
func reapStaleQEMU() int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	killed := 0
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cl, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		s := strings.ReplaceAll(string(cl), "\x00", " ")
		if strings.Contains(s, "qemu-system-x86_64") && strings.Contains(s, "wash-owrt-") {
			if syscall.Kill(pid, syscall.SIGKILL) == nil {
				killed++
			}
		}
	}
	return killed
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "washnet-matrix: "+format+"\n", a...)
	closeAll() // os.Exit skips deferred Close(); reap our VMs so they don't leak
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
