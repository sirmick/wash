// washgui-vm boots a single OpenWRT microVM running the full wash desktop and
// exposes its HTTP at a host port, so you can connect to the wash GUI managing a
// REAL OpenWRT router (netd autodetects the UCI backend on the box → the router
// screens — Networks / Firewall / Hosts / Advanced — are live, and apply enacts
// on the VM's own netifd/fw4/dnsmasq). The image must be built with the wash
// desktop baked: `make multicall && WASH_GUI=1 sh scripts/build-vm-image-openwrt.sh`.
//
//	go build -o out/washgui-vm ./cmd/washgui-vm && out/washgui-vm --port 8080
//	# → http://localhost:8080/  (a real OpenWRT box, in your browser, with wash)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sirmick/wash/wash-vm/vm"
)

const guestIP = "10.0.2.15" // qemu user-net default guest addr (slirp)

func main() {
	image := flag.String("image", "out/vm/openwrt.img", "OpenWRT image with wash baked (WASH_GUI=1)")
	port := flag.Int("port", 8080, "host port → the VM's wash :11000 (bound 0.0.0.0)")
	nics := flag.Int("nics", 3, "extra ethernet adapters (eth1..ethN) for segments to bind to")
	flag.Parse()

	if _, err := os.Stat(*image); err != nil {
		die("image not found: %s\n  build it: make multicall && WASH_GUI=1 sh scripts/build-vm-image-openwrt.sh", *image)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	const mac = "52:54:00:9a:b1:01"
	// eth0: a qemu user-net NIC with host:port → guest:11000; loopback-safe, no
	// bridge. This is the management plane (don't bind segments to it).
	extra := []string{
		"-netdev", fmt.Sprintf("user,id=net0,hostfwd=tcp:0.0.0.0:%d-%s:11000", *port, guestIP),
		"-device", "virtio-net-pci,netdev=net0,mac=" + mac,
	}
	// eth1..ethN: extra adapters for segments to bind to — isolated dummy L2
	// segments (socket-mcast on unique groups, no other members), so the GUI has
	// real ports to attach WAN/LAN to. No upstream traffic on this single box.
	for n := 1; n <= *nics; n++ {
		extra = append(extra, vm.MCastLAN(fmt.Sprintf("e%d", n), fmt.Sprintf("230.0.0.%d", 40+n), 26500+n, fmt.Sprintf("52:54:00:9a:b1:1%d", n))...)
	}

	fmt.Println("booting an OpenWRT microVM with the wash desktop …")
	w, err := vm.LaunchOpenWRT(ctx, vm.OpenWRTOpts{Disk: *image, Mem: "512M", Extra: extra})
	if err != nil {
		die("launch: %v", err)
	}
	defer w.Close()

	nic := resolveNIC(ctx, w, mac)
	// Pin the NIC to qemu's slirp addr so hostfwd reaches it (OpenWRT's default
	// would put it on br-lan/192.168.1.1, which slirp can't route to). Bring lo
	// back up, and stop the VM's own fw4 so the mgmt port (11000) is reachable —
	// it's unzoned, so fw4's default input would reject it. (A wash apply will
	// restart fw4; that's the known mgmt-vs-managed caveat of this dev harness.)
	run(ctx, w, "/etc/init.d/network stop 2>/dev/null; /etc/init.d/firewall stop 2>/dev/null; "+
		"ip link set lo up; ip link set "+nic+" nomaster 2>/dev/null; "+
		"ip addr flush dev "+nic+"; ip addr add "+guestIP+"/24 dev "+nic+"; ip link set "+nic+" up; "+
		"ip route add default via 10.0.2.2 2>/dev/null; true")

	// Lay out the apps dir in a TRUSTED location (root-owned, not world-writable):
	// the reserved-id services (netd, priv) refuse to load from a world-writable
	// dir like /tmp. The multicall dispatches by argv[0] via these symlinks.
	const appsDir = "/usr/lib/wash-apps"
	// The reserved-id trust check os.Stat()s the (symlinked) binary and wants
	// uid 0; the image-builder FILES overlay bakes /usr/bin/wash owned by the
	// build user, so chown it to root first (we're root in the VM).
	run(ctx, w, "chown root:root /usr/bin/wash 2>/dev/null; chmod 0755 /usr/bin/wash")
	run(ctx, w, "test -x /usr/bin/wash && echo OK-wash || echo MISSING-wash; mkdir -p "+appsDir+"; "+
		"/usr/bin/wash install-symlinks "+appsDir+" 2>&1 | tail -2; ln -sf /usr/bin/wash "+appsDir+"/wash; "+
		"echo apps:; ls "+appsDir+" 2>&1 | tr '\\n' ' '; echo")

	// Start the wash desktop (background; no setsid — busybox lacks it). netd
	// autodetects UCI on OpenWRT, so the router screens are real. Capture stderr.
	out := run(ctx, w, "cd "+appsDir+" && ( WASH_APPS_DIR="+appsDir+" WASH_LISTEN=0.0.0.0:11000 ./wash-router --show-hidden >/tmp/wash.log 2>&1 & ); "+
		"sleep 4; echo '=RUNNING='; pgrep -l wash-router 2>/dev/null; "+
		"echo '=WASH.LOG='; cat /tmp/wash.log 2>&1 | head -20; "+
		"echo '=SERVE='; wget -qO- http://127.0.0.1:11000/ 2>&1 | head -c 120")
	fmt.Printf("\n--- wash startup on the VM ---\n%s\n------------------------------\n", out)

	fmt.Printf("  ┌─ wash desktop on a real OpenWRT router ───────────────────────\n")
	fmt.Printf("  │  http://localhost:%d/   (Networking app → Networks/Firewall/Hosts/Advanced)\n", *port)
	fmt.Printf("  │  log in as root.  apply enacts on this box's netifd/fw4/dnsmasq.\n")
	fmt.Printf("  └───────────────────────────────────────────────────────────────\n")
	fmt.Println("  Ctrl-C to stop.  (streaming the VM's wash log below)")

	// Stream the VM's wash-router log so app-launch errors are visible from the
	// host (the console is otherwise ours alone). Re-dumps the tail each poll;
	// read the latest @@@LOG@@@ block.
	for ctx.Err() == nil {
		o, err := w.Run(ctx, "echo '@@@LOG@@@'; tail -n 40 /tmp/wash.log 2>/dev/null")
		if err == nil {
			if i := strings.LastIndex(o, "@@@LOG@@@"); i >= 0 {
				fmt.Print(o[i+len("@@@LOG@@@"):])
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	fmt.Println("\nshutting down …")
}

func resolveNIC(ctx context.Context, w *vm.OpenWRT, mac string) string {
	out, err := w.Run(ctx, `D=; for i in /sys/class/net/*; do [ "$(cat $i/address)" = "`+mac+`" ] && D=${i##*/}; done; echo "NIC""=$D"`)
	if err != nil {
		die("resolve nic: %v", err)
	}
	i := strings.Index(out, "NIC=")
	if i < 0 {
		die("no NIC for mac %s:\n%s", mac, out)
	}
	dev := strings.TrimSpace(strings.SplitN(out[i+len("NIC="):], "\n", 2)[0])
	if dev == "" {
		die("no interface with mac %s", mac)
	}
	return dev
}

func run(ctx context.Context, w *vm.OpenWRT, cmd string) string {
	out, err := w.Run(ctx, cmd)
	if err != nil {
		die("%q: %v\n%s", cmd, err, out)
	}
	return out
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "washgui-vm: "+format+"\n", a...)
	os.Exit(1)
}
