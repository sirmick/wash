package ucibuf

import "testing"

func TestRoundTrip(t *testing.T) {
	in := map[string]string{
		"network":  "config interface 'lan'\n\toption proto 'dhcp'\n",
		"wireless": "config wifi-iface\n\toption ssid 'hz'\n",
	}
	buf := Marshal(in)
	if !HasMarkers(buf) {
		t.Fatal("marshalled buffer should have markers")
	}
	out := Unmarshal(buf)
	for k, v := range in {
		if out[k] != v {
			t.Errorf("round-trip mismatch for %q:\n got: %q\nwant: %q", k, out[k], v)
		}
	}
	if len(out) != len(in) {
		t.Errorf("package count: got %d want %d", len(out), len(in))
	}
}

func TestRawHasNoMarkers(t *testing.T) {
	if HasMarkers("config interface 'lan'\n") {
		t.Error("a raw single-package .uci must not look like a marshalled buffer")
	}
}
