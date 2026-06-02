package netd

import (
	"testing"

	"github.com/sirmick/wash/internal/washnet/backend"
)

func TestChooseBackend(t *testing.T) {
	active := backend.Detection{Available: true, Active: true}
	avail := backend.Detection{Available: true}
	absent := backend.Detection{}

	cases := []struct {
		name string
		mode string
		d    detections
		want string
	}{
		{"forced nm", BackendNM, detections{}, BackendNM},
		{"forced networkd", BackendNetworkd, detections{}, BackendNetworkd},
		{"forced netplan", BackendNetplan, detections{}, BackendNetplan},
		{"forced ifupdown", BackendIfupdown, detections{}, BackendIfupdown},
		{"forced fake", BackendFake, detections{}, BackendFake},
		{"unknown mode → fake", "bogus", detections{nm: active}, BackendFake},

		// auto: a RUNNING NM is the active manager (no netplan present), it wins
		// even when networkd is also present.
		{"auto desktop: NM active wins over networkd", BackendAuto, detections{nm: active, networkd: active}, BackendNM},
		{"auto: NM active", BackendAuto, detections{nm: active, networkd: absent}, BackendNM},

		// auto own-it: NM masked/absent, networkd active → networkd (the Fedora image).
		{"auto router: networkd active, NM masked", BackendAuto, detections{nm: absent, networkd: active}, BackendNetworkd},
		// an ACTIVE networkd beats a merely-available (installed, not running) NM:
		// the Active checks precede the Available fallbacks.
		{"auto: networkd active beats NM available", BackendAuto, detections{nm: avail, networkd: active}, BackendNetworkd},
		{"auto: only NM available", BackendAuto, detections{nm: avail, networkd: absent}, BackendNM},
		{"auto: only networkd available", BackendAuto, detections{nm: absent, networkd: avail}, BackendNetworkd},
		{"auto: nothing → fake", BackendAuto, detections{}, BackendFake},

		// netplan is the authority layer: an active netplan wins over an active
		// NM/networkd (they are its render targets). This is the buzz / Ubuntu
		// case — NM is "active" but netplan is the real config source.
		{"auto: netplan active beats NM active", BackendAuto, detections{netplan: active, nm: active}, BackendNetplan},
		{"auto: netplan active beats networkd active", BackendAuto, detections{netplan: active, networkd: active}, BackendNetplan},
		{"auto: netplan active beats everything", BackendAuto, detections{netplan: active, nm: active, networkd: active, ifupdown: active}, BackendNetplan},
		// netplan only installed (not driving) does NOT override an active NM.
		{"auto: netplan available, NM active → NM", BackendAuto, detections{netplan: avail, nm: active}, BackendNM},
		{"auto: netplan available only", BackendAuto, detections{netplan: avail}, BackendNetplan},

		// ifupdown (classic Debian): active when it's the manager; loses to
		// active NM/networkd/netplan but beats the available fallbacks.
		{"auto: ifupdown active alone", BackendAuto, detections{ifupdown: active}, BackendIfupdown},
		{"auto: ifupdown active beats networkd available", BackendAuto, detections{networkd: avail, ifupdown: active}, BackendIfupdown},
		{"auto: NM active beats ifupdown active", BackendAuto, detections{nm: active, ifupdown: active}, BackendNM},
		{"auto: ifupdown available only", BackendAuto, detections{ifupdown: avail}, BackendIfupdown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := chooseBackend(c.mode, c.d)
			if got != c.want {
				t.Errorf("chooseBackend(%q, %+v) = %q (%s), want %q", c.mode, c.d, got, reason, c.want)
			}
		})
	}
}
