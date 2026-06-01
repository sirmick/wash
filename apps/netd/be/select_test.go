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
		{"forced fake", BackendFake, detections{}, BackendFake},
		{"unknown mode → fake", "bogus", detections{nm: active}, BackendFake},

		// auto: a RUNNING NM means coexist — it wins even when networkd is present
		// (the Fedora own-it image masks NM so this branch doesn't fire there).
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
