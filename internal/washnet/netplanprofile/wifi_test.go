package netplanprofile

import (
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/washnet/model"
)

// TestWifiRoundTrip checks that the three station encryptions survive a
// Render → Parse cycle WITHOUT degrading — in particular WPA3-SAE must not
// collapse to WPA2-PSK (the gap the bare-password form had).
func TestWifiRoundTrip(t *testing.T) {
	in := model.Config{
		Radios: []model.WifiDevice{{Name: "wlan0"}},
		SSIDs: []model.WifiIface{
			{Device: "wlan0", SSID: "open-net", Mode: "sta", Network: "wlan0", Encryption: model.EncNone{}},
			{Device: "wlan0", SSID: "psk-net", Mode: "sta", Network: "wlan0", Encryption: model.EncPSK2{Key: "pass1234"}},
			{Device: "wlan0", SSID: "sae-net", Mode: "sta", Network: "wlan0", Hidden: true, Encryption: model.EncSAE{Key: "pass5678"}},
		},
	}

	files, err := Render(in)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	yaml := files[FileName]
	if !strings.Contains(yaml, "key-management: sae") {
		t.Errorf("SAE network should render an explicit sae key-management, got:\n%s", yaml)
	}

	out, err := Parse(files)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got := map[string]model.WifiIface{}
	for _, s := range out.SSIDs {
		got[s.SSID] = s
	}
	if len(got) != 3 {
		t.Fatalf("want 3 SSIDs back, got %d: %+v", len(got), out.SSIDs)
	}

	if _, ok := got["open-net"].Encryption.(model.EncNone); !ok {
		t.Errorf("open-net should be EncNone, got %#v", got["open-net"].Encryption)
	}
	if e, ok := got["psk-net"].Encryption.(model.EncPSK2); !ok || e.Key != "pass1234" {
		t.Errorf("psk-net should be EncPSK2{pass1234}, got %#v", got["psk-net"].Encryption)
	}
	if e, ok := got["sae-net"].Encryption.(model.EncSAE); !ok || e.Key != "pass5678" {
		t.Errorf("sae-net should round-trip as EncSAE{pass5678}, got %#v", got["sae-net"].Encryption)
	}
	if !got["sae-net"].Hidden {
		t.Error("sae-net should stay hidden")
	}
}
