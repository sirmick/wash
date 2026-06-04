package netd

import (
	"context"
	"os"
	"sync"

	"github.com/sirmick/wash/apps/netd/be/wifi"
)

// selectWifi returns the wifi runtime: the real nmcli-backed one in production,
// or a deterministic fake under WASH_NETD_WIFI ("off"/"on") for the host-side
// e2e. Precedence mirrors selectApplier's WASH_NETD_BACKEND gate.
func selectWifi() wifi.WifiRuntime {
	switch os.Getenv("WASH_NETD_WIFI") {
	case "off":
		return &fakeWifiRuntime{enabled: false}
	case "on":
		return &fakeWifiRuntime{
			enabled: true,
			aps:     []wifi.AP{{SSID: "wash-e2e", Signal: 72, Security: "WPA2"}},
		}
	default:
		return wifi.New()
	}
}

// fakeWifiRuntime is a deterministic wifi.WifiRuntime for the host-side e2e
// (net-app.spec.ts), mirroring the fake config backend: the real nmcli-backed
// runtime is non-deterministic on a CI host (it depends on whatever radio +
// NetworkManager state that box happens to have), so the windowed wifi path
// (FE → com.wash.net → com.wash.netd) had no deterministic test — which is how
// the relay regression that dropped every wifi_* message went unnoticed.
//
// Selected via WASH_NETD_WIFI (selectWifi): "off" presents a radio that is
// present but switched off (scan reports Enabled:false → the FE must render the
// "Turn on Wi-Fi" toggle), "on" presents an enabled radio with one AP (the
// picker must list it). Both states are reachable ONLY if wifi_scan actually
// relays end to end, so each is a tight regression for the dropped-relay bug.
type fakeWifiRuntime struct {
	mu      sync.Mutex
	enabled bool
	aps     []wifi.AP
}

var _ wifi.WifiRuntime = (*fakeWifiRuntime)(nil)

// Detect always reports a present radio on a live NM — the box has wifi, so the
// FE shows the live scan/connect UI rather than the manual-only backend path.
func (f *fakeWifiRuntime) Detect() (radioPresent, nmLive bool) { return true, true }

func (f *fakeWifiRuntime) Enabled(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

func (f *fakeWifiRuntime) SetEnabled(_ context.Context, on bool) (string, error) {
	f.mu.Lock()
	f.enabled = on
	f.mu.Unlock()
	return "", nil
}

// Scan returns the canned APs only while enabled — matching real nmcli, which
// reports nothing while the radio is off.
func (f *fakeWifiRuntime) Scan(context.Context) ([]wifi.AP, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.enabled {
		return nil, nil
	}
	return append([]wifi.AP(nil), f.aps...), nil
}

func (f *fakeWifiRuntime) Status(context.Context) ([]wifi.WifiConn, error) { return nil, nil }

func (f *fakeWifiRuntime) Connect(context.Context, string, string, string, bool) (string, error) {
	return "", nil
}

func (f *fakeWifiRuntime) Forget(context.Context, string) (string, error) { return "", nil }
