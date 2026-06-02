package netd

import (
	"context"
	"errors"
	"testing"

	"github.com/sirmick/wash/apps/netd/be/wifi"
)

// fakeWifi is a stub WifiRuntime for the handler-core tests.
type fakeWifi struct {
	aps   []wifi.AP
	conns []wifi.WifiConn
	err   error
}

func (f fakeWifi) Detect() (bool, bool)                            { return true, true }
func (f fakeWifi) Scan(context.Context) ([]wifi.AP, error)         { return f.aps, f.err }
func (f fakeWifi) Status(context.Context) ([]wifi.WifiConn, error) { return f.conns, f.err }
func (f fakeWifi) Connect(context.Context, string, string, string, bool) (string, error) {
	return "", nil
}
func (f fakeWifi) Forget(context.Context, string) (string, error) { return "", nil }

func TestWifiScanHandler(t *testing.T) {
	ctx := context.Background()

	// not live → empty, no error, non-nil slice (FE shows nothing).
	if r, err := wifiScan(ctx, fakeWifi{aps: []wifi.AP{{SSID: "x"}}}, false); err != nil || r.APs == nil || len(r.APs) != 0 {
		t.Fatalf("not-live scan = (%+v, %v), want empty non-nil", r, err)
	}
	// nil runtime → empty, no error.
	if r, err := wifiScan(ctx, nil, true); err != nil || len(r.APs) != 0 {
		t.Fatalf("nil-rt scan = (%+v, %v), want empty", r, err)
	}
	// live → APs pass through.
	if r, err := wifiScan(ctx, fakeWifi{aps: []wifi.AP{{SSID: "home", Signal: 80}}}, true); err != nil || len(r.APs) != 1 || r.APs[0].SSID != "home" {
		t.Fatalf("live scan = (%+v, %v), want [home]", r, err)
	}
	// nil aps from runtime → non-nil empty.
	if r, err := wifiScan(ctx, fakeWifi{aps: nil}, true); err != nil || r.APs == nil {
		t.Fatalf("nil-aps scan = (%+v, %v), want non-nil empty", r, err)
	}
	// runtime error propagates.
	if _, err := wifiScan(ctx, fakeWifi{err: errors.New("boom")}, true); err == nil {
		t.Fatal("scan error should propagate")
	}
}

func TestWifiStatusHandler(t *testing.T) {
	ctx := context.Background()
	if r, err := wifiStatus(ctx, nil, true); err != nil || r.Conns == nil || len(r.Conns) != 0 {
		t.Fatalf("nil-rt status = (%+v, %v), want empty non-nil", r, err)
	}
	if r, err := wifiStatus(ctx, fakeWifi{conns: []wifi.WifiConn{{Name: "home", Device: "wlan0"}}}, true); err != nil || len(r.Conns) != 1 || r.Conns[0].Device != "wlan0" {
		t.Fatalf("live status = (%+v, %v), want [home/wlan0]", r, err)
	}
	if _, err := wifiStatus(ctx, fakeWifi{err: errors.New("boom")}, true); err == nil {
		t.Fatal("status error should propagate")
	}
}
