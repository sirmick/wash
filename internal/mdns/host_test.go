package mdns

import "testing"

func TestHostServiceInfoOptOut(t *testing.T) {
	t.Setenv("WASH_DISCOVERY_NO_ADVERTISE", "1")
	if got := HostServiceInfo(22); got != nil {
		t.Fatalf("WASH_DISCOVERY_NO_ADVERTISE set: want nil advertisement, got %+v", got)
	}
}

func TestHostServiceInfoAdvertises(t *testing.T) {
	t.Setenv("WASH_DISCOVERY_NO_ADVERTISE", "")
	got := HostServiceInfo(2222)
	// CI/containers may have no routable IPv4 — then nil is correct and
	// there's nothing more to assert. When we DO advertise, the shape must
	// be right: a port we passed and the wash TXT marker.
	if got == nil {
		t.Skip("no routable IPv4 on this host; nil advertisement is expected")
	}
	if got.Port != 2222 {
		t.Errorf("port: want 2222, got %d", got.Port)
	}
	if got.Instance == "" {
		t.Error("instance (hostname) is empty")
	}
	if len(got.IPv4) == 0 {
		t.Error("advertising but no IPv4 addresses")
	}
	found := false
	for _, txt := range got.Text {
		if txt == "wash=1" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing wash=1 TXT marker: %v", got.Text)
	}
}

func TestDockerIface(t *testing.T) {
	cases := map[string]bool{
		"docker0":          true,
		"veth1234":         true,
		"br-0123456789ab":  true,  // "br-" + 12 hex = len 15
		"br-lan":           false, // wash/user bridge, not docker
		"br0":              false,
		"eth0":             false,
		"eth0.10":          false,
		"enp5s0f0np0":      false,
	}
	for name, want := range cases {
		if got := dockerIface(name); got != want {
			t.Errorf("dockerIface(%q) = %v, want %v", name, got, want)
		}
	}
}
