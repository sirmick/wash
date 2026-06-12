// washnet-demo launches three OpenWRT microVMs on a shared, loopback-only L2
// segment — one configured by wash as a two-VLAN router (DHCP + DNS + firewall),
// two as DHCP workstations — and exposes each VM's serial console in the browser
// on its own port. It's the interactive sibling of the M0–M3 multi-VM e2e tests:
// the same wash-in-router-mode setup, kept alive so you can poke at it.
//
//	go build -o out/washnet-demo ./cmd/washnet-demo && out/washnet-demo
//	# router        http://0.0.0.0:8001
//	# workstation-1 http://0.0.0.0:8002   (vlan10, DHCP)
//	# workstation-2 http://0.0.0.0:8003   (vlan20, DHCP)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sirmick/wash/wash-vm/vm"
)

func main() {
	image := flag.String("image", "out/vm/openwrt.img", "OpenWRT VM image (scripts/build-vm-image-openwrt.sh)")
	base := flag.Int("base-port", 8001, "console port for the router; +1/+2 for the workstations")
	flag.Parse()

	if _, err := os.Stat(*image); err != nil {
		die("image not found: %s\n  build it with: scripts/build-vm-image-openwrt.sh", *image)
	}

	// Ctrl-C / SIGTERM tears the whole thing down (qemu children + console servers
	// are bound to ctx, and the deferred Closes run on the cancel).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	const group = "230.0.0.71"
	const port = 27100
	macR, macA, macB := "52:54:00:de:00:01", "52:54:00:de:00:02", "52:54:00:de:00:03"

	fmt.Println("launching 3 OpenWRT microVMs on a shared L2 segment (loopback mcast, no host bridge) …")
	router := launch(ctx, *image, "router", group, port, macR)
	defer router.Close()
	ws1 := launch(ctx, *image, "workstation-1", group, port, macA)
	defer ws1.Close()
	ws2 := launch(ctx, *image, "workstation-2", group, port, macB)
	defer ws2.Close()

	fmt.Println("wash configures the router: two VLANs, a DHCP pool + DNS per VLAN, inter-VLAN firewall open …")
	configureRouter(ctx, router, macR)
	fmt.Println("workstations request DHCP from the wash-configured router …")
	dhcpWorkstation(ctx, ws1, macA, "10", "ws1")
	dhcpWorkstation(ctx, ws2, macB, "20", "ws2")

	serveConsole(ctx, router, *base+0, "router — 10.10.0.1 (vlan10) / 10.20.0.1 (vlan20)")
	serveConsole(ctx, ws1, *base+1, "workstation-1 — vlan10, DHCP from the router")
	serveConsole(ctx, ws2, *base+2, "workstation-2 — vlan20, DHCP from the router")

	url := func(n int) string { return fmt.Sprintf("http://localhost:%d/", *base+n) }
	fmt.Printf(`
  ┌─ wash net demo — 3 microVMs live ───────────────────────────────────
  │  router         %s
  │  workstation-1  %s
  │  workstation-2  %s
  └─────────────────────────────────────────────────────────────────────
  Open the URLs and type in the consoles. Try, on a workstation:
      ip -4 addr            # the DHCP lease from the router
      ping 10.10.0.1        # the VLAN gateway
      nslookup nas.lan 10.10.0.1   # inside DNS (static record)
      ping <other-workstation-ip>   # inter-VLAN, via the router's firewall
  Ctrl-C to shut everything down.
`, url(0), url(1), url(2))

	<-ctx.Done()
	fmt.Println("\nshutting down the VMs …")
}

func launch(ctx context.Context, image, name, group string, port int, mac string) *vm.OpenWRT {
	w, err := vm.LaunchOpenWRT(ctx, vm.OpenWRTOpts{Disk: image, Extra: vm.MCastLAN("seg", group, port, mac)})
	if err != nil {
		die("launch %s: %v", name, err)
	}
	fmt.Printf("  • %s booted\n", name)
	return w
}

func serveConsole(ctx context.Context, w *vm.OpenWRT, port int, title string) *vm.Console {
	// Bind 0.0.0.0 so the consoles are reachable from the LAN (wash-vm convention).
	c, err := w.ServeConsole(ctx, fmt.Sprintf("0.0.0.0:%d", port), title)
	if err != nil {
		die("serve console on :%d: %v", port, err)
	}
	return c
}

func configureRouter(ctx context.Context, router *vm.OpenWRT, mac string) {
	nic := resolveNIC(ctx, router, mac)
	if err := router.WriteFile(ctx, "/tmp/demo.uci", routerUCI(nic)); err != nil {
		die("stage router config: %v", err)
	}
	out, err := router.Run(ctx, "WASH_NETD_BACKEND=uci washnet-apply /tmp/demo.uci")
	if err != nil || !strings.Contains(out, "APPLIED") {
		die("router apply failed: %v\n%s", err, out)
	}
	// fw4 + dnsmasq bind to devices at load time; restart them once netifd has
	// created the VLAN sub-interfaces (the applier reloads them too early on a
	// fresh interface — the reload-ordering nit the M3 test also works around).
	if _, err := router.Run(ctx, "sleep 3; /etc/init.d/firewall restart; /etc/init.d/dnsmasq restart; sleep 1"); err != nil {
		die("router service restart: %v", err)
	}
	fmt.Println("  • router configured (vlan10=10.10.0.1/24, vlan20=10.20.0.1/24)")
}

func dhcpWorkstation(ctx context.Context, w *vm.OpenWRT, mac, vid, host string) {
	nic := resolveNIC(ctx, w, mac)
	vif := nic + "." + vid
	// Strip the stock OpenWRT to a bare client (see the M2/M3 tests), tag into its
	// VLAN, then lease via a standalone udhcpc script (the OpenWRT default ties
	// into the netifd we stopped).
	if _, err := w.Run(ctx, "/etc/init.d/network stop; /etc/init.d/dnsmasq stop; /etc/init.d/firewall stop; /etc/init.d/odhcpd stop 2>/dev/null; killall udhcpc odhcpd 2>/dev/null; sleep 1; "+
		"ip link set "+nic+" nomaster 2>/dev/null; ip addr flush dev "+nic+"; ip link set "+nic+" up; "+
		"modprobe 8021q 2>/dev/null; ip link add link "+nic+" name "+vif+" type vlan id "+vid+" 2>/dev/null; ip link set "+vif+" up"); err != nil {
		die("prep %s: %v", host, err)
	}
	if err := w.WriteFile(ctx, "/tmp/udhcpc.sh", udhcpcScript); err != nil {
		die("stage udhcpc script on %s: %v", host, err)
	}
	out, _ := w.Run(ctx, "chmod +x /tmp/udhcpc.sh; udhcpc -i "+vif+" -s /tmp/udhcpc.sh -x hostname:"+host+" -t 12 -T 2 -n -q 2>&1 | tail -2; ip -4 addr show "+vif)
	fmt.Printf("  • %s leased %s\n", host, firstInet(out))
}

// resolveNIC maps a known MAC to its in-guest interface name (robust against
// eth0/eth1 enumeration order). Sentinel split so the console echo can't match.
func resolveNIC(ctx context.Context, w *vm.OpenWRT, mac string) string {
	out, err := w.Run(ctx, `D=; for i in /sys/class/net/*; do [ "$(cat $i/address)" = "`+mac+`" ] && D=${i##*/}; done; echo "NIC""=$D"`)
	if err != nil {
		die("resolve nic for %s: %v", mac, err)
	}
	i := strings.Index(out, "NIC=")
	if i < 0 {
		die("no interface with mac %s:\n%s", mac, out)
	}
	dev := strings.TrimSpace(strings.SplitN(out[i+len("NIC="):], "\n", 2)[0])
	if dev == "" {
		die("no interface with mac %s", mac)
	}
	return dev
}

func firstInet(out string) string {
	if i := strings.Index(out, "inet "); i >= 0 {
		rest := out[i+len("inet "):]
		if j := strings.IndexAny(rest, " /\n"); j >= 0 {
			return strings.TrimSpace(rest[:j])
		}
	}
	return "(no lease)"
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "washnet-demo: "+format+"\n", a...)
	os.Exit(1)
}

// udhcpcScript applies a lease (the segments are /24) directly to the NIC,
// bypassing OpenWRT's netifd-tied default script.
const udhcpcScript = `#!/bin/sh
case "$1" in
  bound|renew)
    ip addr flush dev "$interface"
    ip addr add "$ip/24" dev "$interface"
    [ -n "$router" ] && ip route add default via "$router" 2>/dev/null
    ;;
esac
`

// routerUCI is the config wash applies to the router: a tagged sub-interface per
// VLAN on the trunk NIC, each with a static gateway + DHCP pool + firewall zone,
// the inside dnsmasq with a static DNS record, and vlan10<->vlan20 forwarding
// open so the two workstations can reach each other through the router. nic is
// the router's trunk NIC. (Mirrors the M3 test's routerVLANs.)
func routerUCI(nic string) string {
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

config forwarding
	option src 'vlan10'
	option dest 'vlan20'

config forwarding
	option src 'vlan20'
	option dest 'vlan10'

# ==== dhcp ====
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
