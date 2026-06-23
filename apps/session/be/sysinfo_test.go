package session

import (
	"net"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestSkipInterface(t *testing.T) {
	cases := []struct {
		name string
		skip bool
	}{
		// Real interfaces — keep.
		{"eth0", false},
		{"enp3s0", false},
		{"enp5s0f0np0", false},
		{"wlan0", false},
		{"wlp2s0", false},
		// User-named bridges (router-style boxes) — keep. The bug
		// that prompted this test had br-lan getting skipped because
		// the BE had a blanket `br-` prefix filter.
		{"br-lan", false},
		{"br-wan", false},
		{"br-kids", false},
		{"br0", false},
		// Docker / virt / overlay — drop.
		{"docker0", true},
		{"docker_gwbridge", true},
		{"veth1234", true},
		{"virbr0", true},
		{"virbr1-nic", true},
		{"vmnet0", true},
		{"tun0", true},
		{"tap0", true},
		{"wg0", true},
		{"tailscale0", true},
		{"zt0", true},
		{"cilium_host", true},
		{"kube-bridge", true},
		{"flannel.1", true},
		{"cni0", true},
		// Docker's auto-bridges: br- + 12 hex chars.
		{"br-1a2b3c4d5e6f", true},
		{"br-ffffffffffff", true},
		// Lookalikes that are NOT docker bridges (11 or 13 chars,
		// non-hex character).
		{"br-12345678901", false},   // 11 hex chars
		{"br-1234567890123", false}, // 13 hex chars
		{"br-1a2b3c4d5e6Z", false},  // non-hex
		{"br-12345678901g", false},  // 'g' is not a hex digit
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := skipInterface(tc.name); got != tc.skip {
				t.Fatalf("skipInterface(%q)=%v, want %v", tc.name, got, tc.skip)
			}
		})
	}
}

func TestIsDockerBridge(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"br-1a2b3c4d5e6f", true},
		{"br-000000000000", true},
		{"br-aaaaaaaaaaaa", true},
		{"br-ffffffffffff", true},
		// 12 chars, but a digit-only suffix is still valid hex.
		{"br-123456789012", true},
		// Wrong length.
		{"br-1a2b3c4d5e6", false},   // 11
		{"br-1a2b3c4d5e6f0", false}, // 13
		// Wrong prefix.
		{"docker-1a2b3c4d5e6f", false},
		{"br_1a2b3c4d5e6f", false},
		// Non-hex character.
		{"br-1a2b3c4d5e6g", false},
		{"br-1a2b3C4d5e6f", false}, // uppercase isn't matched
		// Real router-style names.
		{"br-lan", false},
		{"br-wan", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDockerBridge(tc.name); got != tc.want {
				t.Fatalf("isDockerBridge(%q)=%v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestDedupSort(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		// Short-circuit case: length < 2 returns without mutating.
		// We pass an empty slice and assert len(out)==0 separately
		// rather than via DeepEqual (nil vs []string{} differs).
		{"empty", nil, nil},
		{"single", []string{"a"}, []string{"a"}},
		{"already sorted", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"reverse", []string{"c", "b", "a"}, []string{"a", "b", "c"}},
		{"dupes", []string{"b", "a", "b", "c", "a"}, []string{"a", "b", "c"}},
		{"all same", []string{"x", "x", "x"}, []string{"x"}},
		// Multi-iface case from the field — same address bound on
		// two interfaces should collapse to one entry.
		{"vpn double-bind", []string{"10.0.0.5", "10.0.0.5"}, []string{"10.0.0.5"}},
		{"two pairs", []string{"b", "a", "b", "a"}, []string{"a", "b"}},
		// IPv4 + IPv6 mixed, with one IPv4 dupe to exercise the
		// dedup path on a realistic shape.
		{
			"realistic",
			[]string{"192.168.1.42", "10.0.0.5", "10.0.0.5", "fe80::1"},
			[]string{"10.0.0.5", "192.168.1.42", "fe80::1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := append([]string(nil), tc.in...)
			dedupSort(&in)
			if len(in) != len(tc.want) {
				t.Fatalf("dedupSort(%v) = %v (len=%d), want %v (len=%d)",
					tc.in, in, len(in), tc.want, len(tc.want))
			}
			for i := range in {
				if in[i] != tc.want[i] {
					t.Fatalf("dedupSort(%v) = %v, want %v", tc.in, in, tc.want)
				}
			}
		})
	}
}

func TestGatherSysInfoSelfConsistent(t *testing.T) {
	// Smoke test against the real /proc + net stack. Nothing here
	// can be predicted across machines, but the fields should be
	// internally consistent: CPUs > 0, hostname non-empty, IP list
	// containing no obvious junk (loopback / link-local).
	info := gatherSysInfo()
	if info.CPUs != runtime.NumCPU() {
		t.Fatalf("CPUs=%d, runtime.NumCPU()=%d", info.CPUs, runtime.NumCPU())
	}
	if info.Hostname == "" {
		t.Fatal("Hostname empty — os.Hostname() failed?")
	}
	// Username may be empty on stripped containers (no /etc/passwd);
	// don't assert on it.
	for _, g := range info.Interfaces {
		if g.Name == "" {
			t.Fatalf("empty interface name in group: %+v", g)
		}
		if len(g.IPs) == 0 {
			t.Fatalf("interface %s in output with no IPs — should have been filtered", g.Name)
		}
		if skipInterface(g.Name) {
			t.Fatalf("filtered iface %s leaked into output", g.Name)
		}
		for _, ip := range g.IPs {
			if strings.HasPrefix(ip, "127.") || strings.HasPrefix(ip, "::1") {
				t.Fatalf("loopback leaked: %s", ip)
			}
			if strings.HasPrefix(ip, "169.254.") || strings.HasPrefix(ip, "fe80:") {
				t.Fatalf("link-local leaked: %s", ip)
			}
		}
	}
	if info.MemBytes == 0 {
		// /proc/meminfo absent only on non-Linux or chrooted-without-/proc.
		// On any normal Linux dev box this should populate.
		if runtime.GOOS == "linux" {
			t.Fatalf("MemTotal returned 0 on linux — /proc/meminfo unreadable?")
		}
	}
}

func TestIsEUI64(t *testing.T) {
	cases := []struct {
		name string
		ip   string
		want bool
	}{
		// MAC 3c:a5:00:81:53:11 → EUI-64 inserts ff:fe →
		// 3ea5:00ff:fe81:5311 (also flips the universal/local bit
		// on the first byte: 3c ^ 02 = 3e).
		{"real MAC-derived", "2601:646:8700:aa64:3ea5:00ff:fe81:5311", true},
		// Random RFC 4941 privacy address — no ff:fe.
		{"privacy ext", "2601:646:8700:aa64:d6c1:f5f1:5deb:2e24", false},
		// Edge case: ff at byte 11 but not fe at 12.
		{"ff:00 false positive", "2001:db8::ff00:0:0:0", false},
		// Edge case: fe at 12 but not ff at 11.
		{"00:fe false positive", "2001:db8::1:00fe:0:0", false},
		// IPv4-mapped — not v6.
		{"v4-mapped", "::ffff:192.0.2.1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("bad input IP: %s", tc.ip)
			}
			if got := isEUI64(ip.To16()); got != tc.want {
				t.Fatalf("isEUI64(%s)=%v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

func TestDedupV6ByPrefix(t *testing.T) {
	t.Run("collapses SLAAC noise on one /64", func(t *testing.T) {
		// The screenshot's actual addresses: 3 privacy + 1 EUI-64,
		// all on the same /64. The EUI-64 one (ending …fe81:5311)
		// should be the survivor.
		ips := mustParseIPs(t,
			"2601:646:8700:aa64:d6c1:f5f1:5deb:2e24",
			"2601:646:8700:aa64:fa66:b30d:9d8d:5ac2",
			"2601:646:8700:aa64:6b69:9685:991a:cee8",
			"2601:646:8700:aa64:3ea5:00ff:fe81:5311",
		)
		got := dedupV6ByPrefix(ips)
		if len(got) != 1 {
			t.Fatalf("expected 1 address, got %d: %v", len(got), got)
		}
		if got[0] != "2601:646:8700:aa64:3ea5:ff:fe81:5311" && got[0] != "2601:646:8700:aa64:3ea5:00ff:fe81:5311" {
			t.Fatalf("expected EUI-64 address, got %s", got[0])
		}
	})

	t.Run("first one wins when no EUI-64 present", func(t *testing.T) {
		ips := mustParseIPs(t,
			"2001:db8::1",
			"2001:db8::2",
			"2001:db8::3",
		)
		got := dedupV6ByPrefix(ips)
		if len(got) != 1 {
			t.Fatalf("expected 1 address, got %d: %v", len(got), got)
		}
		// First-encountered wins. We can't assert which one
		// because the map iteration above the slot lookup is
		// deterministic only because there's a single key here.
		if got[0] != "2001:db8::1" {
			t.Fatalf("expected first input to win, got %s", got[0])
		}
	})

	t.Run("multiple /64 prefixes preserved", func(t *testing.T) {
		ips := mustParseIPs(t,
			"2001:db8:1::1",
			"2001:db8:1::ffff:fe00:0",
			"2001:db8:2::1",
			"2001:db8:2::ffff:fe00:0",
			"fdab::1",
		)
		got := dedupV6ByPrefix(ips)
		sort.Strings(got)
		if len(got) != 3 {
			t.Fatalf("expected 3 prefixes, got %d: %v", len(got), got)
		}
		// Each /64 should have contributed exactly one survivor;
		// where an EUI-64 exists, it wins.
		want := []string{
			"2001:db8:1::ffff:fe00:0",
			"2001:db8:2::ffff:fe00:0",
			"fdab::1",
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("dedup mismatch: got %v, want %v", got, want)
			}
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if got := dedupV6ByPrefix(nil); got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})

	t.Run("EUI-64 wins regardless of input order", func(t *testing.T) {
		// Same prefix, EUI-64 second in the input. Should still win.
		ips := mustParseIPs(t,
			"2001:db8::aaaa:bbbb:cccc:dddd",
			"2001:db8::1234:56ff:fe78:9abc",
		)
		got := dedupV6ByPrefix(ips)
		if len(got) != 1 {
			t.Fatalf("expected 1, got %v", got)
		}
		if got[0] != "2001:db8::1234:56ff:fe78:9abc" {
			t.Fatalf("EUI-64 should win order-independent; got %s", got[0])
		}
	})
}

func mustParseIPs(t *testing.T, ss ...string) []net.IP {
	t.Helper()
	out := make([]net.IP, 0, len(ss))
	for _, s := range ss {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad IP literal: %s", s)
		}
		out = append(out, ip)
	}
	return out
}

func TestGatherSysInfoRouterFromEnv(t *testing.T) {
	// gatherSysInfo populates RouterInfo from WASH_ROUTER_* env vars
	// set by cmd/wash-router/main.go at startup. Set them here and
	// confirm the values come through.
	t.Setenv("WASH_ROUTER_VERSION", "1.2.3")
	t.Setenv("WASH_ROUTER_COMMIT", "abcdef0")
	t.Setenv("WASH_ROUTER_BUILT", "2026-05-23T07:29:32Z")
	t.Setenv("WASH_ROUTER_DEV", "1")
	info := gatherSysInfo()
	if info.Router.Version != "1.2.3" {
		t.Fatalf("Version=%q, want %q", info.Router.Version, "1.2.3")
	}
	if info.Router.Commit != "abcdef0" {
		t.Fatalf("Commit=%q", info.Router.Commit)
	}
	if info.Router.Built != "2026-05-23T07:29:32Z" {
		t.Fatalf("Built=%q", info.Router.Built)
	}
	if !info.Router.Dev {
		t.Fatal("Dev should be true when WASH_ROUTER_DEV=1")
	}
	// Anything other than literal "1" reads as false.
	t.Setenv("WASH_ROUTER_DEV", "true")
	info = gatherSysInfo()
	if info.Router.Dev {
		t.Fatal(`Dev should be false unless WASH_ROUTER_DEV == "1"`)
	}
}

func TestResolveFQDNDottedHostnamePassthrough(t *testing.T) {
	// A hostname that already contains a dot is treated as the FQDN
	// without any lookup. This codepath is the deterministic one;
	// the reverse-DNS + /etc/resolv.conf branches depend on the host
	// and are exercised by the smoke test above.
	if got := resolveFQDN("box.example.com"); got != "box.example.com" {
		t.Fatalf("resolveFQDN(dotted)=%q, want pass-through", got)
	}
}
