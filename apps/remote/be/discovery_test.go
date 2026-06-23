package remote

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/mdns"
)

// fakeMutator captures the latest published State.
type fakeMutator struct {
	mu    sync.Mutex
	state State
	calls int
}

func (f *fakeMutator) Mutate(fn func(*State)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.state)
	f.calls++
}

func (f *fakeMutator) candidates() []Candidate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Candidate(nil), f.state.Candidates...)
}

func TestDiscovererObserveAndDedup(t *testing.T) {
	fm := &fakeMutator{}
	d := newDiscoverer(fm)

	d.observe(Candidate{Host: "zeta", Addr: "192.168.1.9", Source: "mdns", Wash: true}, false)
	d.observe(Candidate{Host: "alpha", Addr: "192.168.1.2", Source: "mdns", Wash: true}, false)
	// Re-observing the same addr must not add a duplicate or republish.
	callsBefore := fm.calls
	d.observe(Candidate{Host: "alpha", Addr: "192.168.1.2", Source: "mdns", Wash: true}, false)
	if fm.calls != callsBefore {
		t.Errorf("re-observe republished (calls %d -> %d)", callsBefore, fm.calls)
	}

	got := fm.candidates()
	if len(got) != 2 {
		t.Fatalf("want 2 candidates, got %d: %+v", len(got), got)
	}
	// Sorted by host: alpha before zeta.
	if got[0].Host != "alpha" || got[1].Host != "zeta" {
		t.Errorf("not sorted by host: %+v", got)
	}
}

func TestDiscovererPrune(t *testing.T) {
	fm := &fakeMutator{}
	d := newDiscoverer(fm)
	d.observe(Candidate{Host: "stale", Addr: "10.0.0.5", Source: "mdns"}, false)
	d.observe(Candidate{Host: "fresh", Addr: "10.0.0.6", Source: "mdns"}, false)

	// Backdate "stale" past the TTL, then prune.
	d.mu.Lock()
	sc := d.cands["mdns|10.0.0.5"]
	sc.seen = time.Now().Add(-candidateTTL - time.Minute)
	d.cands["mdns|10.0.0.5"] = sc
	d.mu.Unlock()

	if !d.pruneBefore(time.Now().Add(-candidateTTL)) {
		t.Fatal("prune reported no change, expected one drop")
	}
	d.publish()
	got := fm.candidates()
	if len(got) != 1 || got[0].Host != "fresh" {
		t.Fatalf("want only [fresh], got %+v", got)
	}
}

func TestOnMDNSMapping(t *testing.T) {
	fm := &fakeMutator{}
	d := newDiscoverer(fm)

	// Default SSH port is elided from the wire; non-default is kept.
	d.onMDNS(mdns.Entry{Instance: "box1", Host: "box1.local", Addrs: []net.IP{net.IPv4(192, 168, 0, 4)}, Port: 22, Text: []string{"wash=1"}})
	d.onMDNS(mdns.Entry{Instance: "box2", Host: "box2.local", Addrs: []net.IP{net.IPv4(192, 168, 0, 5)}, Port: 2222})
	// No address → not connectable → dropped.
	d.onMDNS(mdns.Entry{Instance: "ghost", Host: "ghost.local"})

	got := fm.candidates()
	if len(got) != 2 {
		t.Fatalf("want 2 candidates, got %d: %+v", len(got), got)
	}
	if got[0].Host != "box1" || got[0].Addr != "192.168.0.4" || got[0].Port != 0 {
		t.Errorf("box1 mapping wrong: %+v", got[0])
	}
	if !got[0].Wash {
		t.Errorf("mDNS candidate should be flagged wash: %+v", got[0])
	}
	if got[1].Host != "box2" || got[1].Port != 2222 {
		t.Errorf("box2 mapping wrong: %+v", got[1])
	}
}

func TestStaticProvider(t *testing.T) {
	t.Setenv("WASH_DISCOVERY_STATIC", "lab=10.42.0.9:2222, 10.42.0.10 ,dflt=10.42.0.11:22")
	fm := &fakeMutator{}
	d := newDiscoverer(fm)
	d.startStatic()

	got := fm.candidates()
	if len(got) != 3 {
		t.Fatalf("want 3 static candidates, got %d: %+v", len(got), got)
	}
	byAddr := map[string]Candidate{}
	for _, c := range got {
		byAddr[c.Addr] = c
		if c.Source != "static" {
			t.Errorf("source = %q, want static: %+v", c.Source, c)
		}
	}
	if c := byAddr["10.42.0.9"]; c.Host != "lab" || c.Port != 2222 {
		t.Errorf("named host:port parsed wrong: %+v", c)
	}
	if c := byAddr["10.42.0.10"]; c.Host != "10.42.0.10" || c.Port != 0 {
		t.Errorf("bare addr parsed wrong (host should default to addr, no port): %+v", c)
	}
	if c := byAddr["10.42.0.11"]; c.Port != 0 {
		t.Errorf("explicit default port 22 should be elided to 0: %+v", c)
	}

	// Static candidates are sticky: a prune well past the TTL must not drop
	// them (they have no announcement to refresh their seen time).
	if d.pruneBefore(time.Now().Add(time.Hour)) {
		t.Error("prune dropped a sticky static candidate")
	}
	if len(fm.candidates()) != 3 {
		t.Errorf("sticky candidates vanished after prune: %+v", fm.candidates())
	}
}
