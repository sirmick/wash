package main

import (
	"runtime"
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
		{"br-12345678901", false},     // 11 hex chars
		{"br-1234567890123", false},   // 13 hex chars
		{"br-1a2b3c4d5e6Z", false},    // non-hex
		{"br-12345678901g", false},    // 'g' is not a hex digit
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
	for _, ip := range info.IPs {
		if strings.HasPrefix(ip, "127.") || strings.HasPrefix(ip, "::1") {
			t.Fatalf("loopback leaked: %s", ip)
		}
		if strings.HasPrefix(ip, "169.254.") || strings.HasPrefix(ip, "fe80:") {
			t.Fatalf("link-local leaked: %s", ip)
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

func TestResolveFQDNDottedHostnamePassthrough(t *testing.T) {
	// A hostname that already contains a dot is treated as the FQDN
	// without any lookup. This codepath is the deterministic one;
	// the reverse-DNS + /etc/resolv.conf branches depend on the host
	// and are exercised by the smoke test above.
	if got := resolveFQDN("box.example.com"); got != "box.example.com" {
		t.Fatalf("resolveFQDN(dotted)=%q, want pass-through", got)
	}
}
