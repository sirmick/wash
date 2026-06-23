package mdns

import (
	"net"
	"os"
	"testing"
	"time"
)

// TestLoopbackDiscovery exercises the whole real pipeline on one host:
// open the multicast socket, advertise, browse, and confirm the
// announcement comes back through the kernel and parses into an Entry.
//
// Normally a server filters its own packets by source IP (so a box never
// discovers itself); this test disables that filter to turn the single
// host into both ends.
//
// It hits a real multicast socket, so it's an opt-in gate (set
// WASH_MDNS_INTEGRATION=1, e.g. `make mdns-test`) rather than part of the
// default unit tier: a sandbox may have a multicast-capable interface that
// nonetheless drops loopback delivery, which would otherwise hang/flake the
// gate. It also skips in -short mode and when no multicast socket is available.
func TestLoopbackDiscovery(t *testing.T) {
	if testing.Short() || os.Getenv("WASH_MDNS_INTEGRATION") == "" {
		t.Skip("multicast integration test: set WASH_MDNS_INTEGRATION=1 to run (make mdns-test)")
	}
	found := make(chan Entry, 8)
	srv, err := New(Options{
		Advertise: &ServiceInfo{
			Instance: "loopbacktest",
			Port:     22,
			Text:     []string{"wash=1"},
			IPv4:     []net.IP{net.IPv4(10, 11, 12, 13)},
		},
		OnEntry:       func(e Entry) { found <- e },
		QueryInterval: 500 * time.Millisecond,
	})
	if err != nil {
		t.Skipf("no multicast socket available: %v", err)
	}
	defer srv.Close()

	// Turn off the self-packet filter so our own announcement is parsed.
	srv.localIPs = map[string]bool{}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-found:
			if e.Instance == "loopbacktest" {
				if e.Port != 22 || e.Host != "loopbacktest.local" {
					t.Fatalf("entry fields wrong: %+v", e)
				}
				return // success
			}
		case <-deadline:
			t.Fatal("did not discover own advertisement within 5s")
		}
	}
}
