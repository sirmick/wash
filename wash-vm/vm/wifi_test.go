package vm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestWifiConnectViaHwsim boots the NM image, creates two virtual radios with
// mac80211_hwsim, stands up a WPA2 access point on one with hostapd, then drives
// wash's real connect path — washnet-wifi -op connect, the exact CLI netd
// escalates to under wash-priv — and asserts the station associates at the OS
// level (iw dev wlan0 link). This exercises the whole nmcli wifi runtime end to
// end against a real (virtual) radio, the only way to e2e wifi without hardware.
//
// Skips (not fails) when the guest kernel lacks mac80211_hwsim, mirroring how
// the other VM tests skip on missing prerequisites.
func TestWifiConnectViaHwsim(t *testing.T) {
	kernel, initramfs := artifacts(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Bigger initramfs (lts modules) + hostapd/NM/wpa_supplicant — give it room.
	vm, err := Launch(ctx, Opts{Kernel: kernel, Initramfs: initramfs, Mem: "2048M"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer vm.Close()
	if err := vm.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	run := func(cmd string) string {
		t.Helper()
		r, err := vm.Exec(ctx, cmd)
		if err != nil {
			t.Fatalf("Exec %q: %v", cmd, err)
		}
		return r.Out
	}
	has := func(sub string) func(string) bool { return func(s string) bool { return strings.Contains(s, sub) } }
	waitFor := func(what string, d time.Duration, ok func(string) bool, cmd string) string {
		t.Helper()
		var out string
		for deadline := time.Now().Add(d); time.Now().Before(deadline); {
			out = run(cmd)
			if ok(out) {
				return out
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("%s not met within %s\nlast: %q\ndevice status:\n%s\nrfkill:\n%s\nhostapd.log:\n%s",
			what, d, out, run("nmcli device status 2>&1"), run("rfkill list 2>&1"), run("cat /tmp/hostapd.log 2>&1"))
		return out
	}

	// Wait for NM to be up (the wifi runtime needs it) before touching radios.
	waitFor("NetworkManager up", 40*time.Second, has("running"), "nmcli -t -f RUNNING general 2>&1")

	// 1. Two virtual radios. Skip if the module isn't in this kernel.
	run("modprobe mac80211_hwsim radios=2 2>&1; echo done")
	var nets string
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		nets = run("ls /sys/class/net 2>&1")
		if strings.Contains(nets, "wlan0") && strings.Contains(nets, "wlan1") {
			break
		}
		time.Sleep(time.Second)
	}
	if !(strings.Contains(nets, "wlan0") && strings.Contains(nets, "wlan1")) {
		t.Skipf("mac80211_hwsim radios did not appear (module absent in guest kernel?); /sys/class/net=%q", nets)
	}

	// Clear rfkill, set a permissive reg domain, and turn NM's wifi on now that
	// the radios exist (a wifi device sits "unavailable" until rfkill clears and
	// NM has spawned wpa_supplicant for it).
	// Clear rfkill + permissive reg domain; the global wpa_supplicant (-u) is
	// already running from NM's start_pre, so NM picks up wlan0 on its own.
	run("rfkill unblock all 2>&1; iw reg set US 2>&1; nmcli radio wifi on 2>&1; echo ok")

	// 2. Stand up a WPA2 AP on wlan1 with hostapd. wlan1 is unmanaged in the
	// image NM config, so neither NM nor the supplicant contend for it.
	run(`cat > /tmp/hostapd.conf <<'EOF'
interface=wlan1
driver=nl80211
ssid=wash-test-ap
hw_mode=g
channel=1
wpa=2
wpa_passphrase=testpass123
wpa_key_mgmt=WPA-PSK
rsn_pairwise=CCMP
EOF
echo ok`)
	run("nohup hostapd /tmp/hostapd.conf >/tmp/hostapd.log 2>&1 </dev/null & echo started")
	waitFor("hostapd AP enabled", 20*time.Second,
		func(s string) bool { return strings.Contains(s, "AP-ENABLED") || strings.Contains(s, "Setup of interface done") },
		"cat /tmp/hostapd.log 2>&1")
	// Give the AP subnet an address + DHCP so the station gets a real IP — the
	// connect isn't "done" for NM until L3 comes up, and this proves end-to-end
	// connectivity (associate → DHCP → IP), not just L2 association.
	run("ip addr add 192.168.66.1/24 dev wlan1 2>&1; echo ok")
	run("nohup dnsmasq -d -i wlan1 --bind-interfaces -F 192.168.66.10,192.168.66.100,255.255.255.0,12h >/tmp/dnsmasq.log 2>&1 </dev/null & echo started")

	// 3. Wait for NM to make wlan0 available (state leaves "unavailable" once
	// rfkill is clear and the supplicant is attached), then drive wash's connect.
	// nmcli's `device wifi connect` runs its own scan, so we don't gate on the
	// (timing-flaky) scan list — the AP is provably visible (iw sees the beacon).
	waitFor("wlan0 available", 30*time.Second,
		func(s string) bool { return strings.Contains(s, "wlan0:disconnected") || strings.Contains(s, "wlan0:connected") },
		"nmcli -t -f DEVICE,STATE device status 2>&1")
	connectOut := waitFor("wash connect ok", 45*time.Second, has("OK"),
		"washnet-wifi -op connect -ssid wash-test-ap -security psk2 -psk testpass123 2>&1")
	t.Logf("washnet-wifi: %s", strings.TrimSpace(connectOut))

	// 4. Assert association (L2) and a DHCP-assigned IP (L3) at the OS level,
	// independent of NM's own view.
	link := waitFor("wlan0 associated", 30*time.Second,
		func(s string) bool { return strings.Contains(s, "wash-test-ap") || strings.Contains(s, "Connected to") },
		"iw dev wlan0 link 2>&1")
	t.Logf("wlan0 link:\n%s", strings.TrimRight(link, "\n"))
	ipout := waitFor("wlan0 got a DHCP lease", 30*time.Second, has("192.168.66."),
		"ip -4 -o addr show wlan0 2>&1")
	t.Logf("wlan0 addr: %s", strings.TrimSpace(ipout))
}
