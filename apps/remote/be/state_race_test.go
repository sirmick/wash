package remote

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// raceMutator reproduces StateService.Mutate's hazard exactly: fn runs under
// the lock, then the resulting SHALLOW snapshot (slice headers copied, backing
// arrays shared) is marshaled OUTSIDE the lock — as the real service does when
// it fans state to subscribers. If a publisher mutates a slice element in place
// (or compacts with s[:0]), that write races this marshal. Driving the real
// setHost/removeHost/setMount/removeMount through it under `go test -race`
// proves they rebuild copy-on-write instead.
type raceMutator struct {
	mu sync.Mutex
	st State
}

func (m *raceMutator) Mutate(fn func(*State)) {
	m.mu.Lock()
	fn(&m.st)
	snap := m.st // shallow copy — shares backing arrays, like StateService
	m.mu.Unlock()
	_, _ = json.Marshal(snap) // the racing read a subscriber send performs
}

// TestStatePublishersAreCopyOnWrite hammers the published-state mutators from
// several goroutines while snapshots are concurrently marshaled. It passes only
// if setHost/removeHost/setMount/removeMount never write a slice a prior
// snapshot still shares — a regression guard for the in-place `s[i] =` / `s[:0]`
// footgun (would fail under -race before the copy-on-write fix).
func TestStatePublishersAreCopyOnWrite(t *testing.T) {
	rm := &raceMutator{}
	sup := &supervisor{svc: rm}
	mm := &mountManager{svc: rm}

	const goroutines, iters = 8, 500
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			// Overlapping keys so upserts exercise the in-place-update path
			// (the one that used to overwrite st.Hosts[i]) as well as append.
			host := fmt.Sprintf("h%d", g%3)
			mp := fmt.Sprintf("/mnt/%d", g%3)
			for i := 0; i < iters; i++ {
				sup.setHost(host, HostState{Host: host, Origin: host, Status: StatusUp})
				mm.setMount(MountState{MountPoint: mp, Status: MountUp})
				sup.removeHost(host)
				mm.removeMount(mp)
			}
		}(g)
	}
	wg.Wait()
}
