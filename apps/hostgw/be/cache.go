package hostgw

import "sort"

import "sync"

// stateCache holds the latest snapshot per service name.
//
// Concurrency shape matters here, and it is the one that bit before
// (docs/SIDEBAR.md M1 gates; the StateService shallow-snapshot footgun):
// put REPLACES a whole entry and all() hands back a fresh slice of
// (service, state) pairs. The `state` values themselves are opaque
// JSON-decoded trees we never reach into, so nothing a reader holds is
// ever written in place — copy-on-write by construction rather than by
// discipline.
type stateCache struct {
	mu sync.RWMutex
	m  map[string]any
}

// cacheEntry is one (service, state) pair, as handed to a replaying
// subscriber.
type cacheEntry struct {
	service string
	state   any
}

func newStateCache() *stateCache {
	return &stateCache{m: map[string]any{}}
}

// put records the newest snapshot for a service, replacing any prior
// one. Snapshots are whole-state, so there is no merge to do — the last
// one told to us IS the truth (SIDEBAR.md §3.2(4): recompute from
// snapshots, never increment from events).
func (c *stateCache) put(service string, state any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[service] = state
}

// all returns every held snapshot, ordered by service name so a replay
// is deterministic (which keeps the tests readable and makes a captured
// router log diffable across runs).
func (c *stateCache) all() []cacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	names := make([]string, 0, len(c.m))
	for k := range c.m {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]cacheEntry, 0, len(names))
	for _, n := range names {
		out = append(out, cacheEntry{service: n, state: c.m[n]})
	}
	return out
}

// len reports how many services we hold state for. Diagnostics + tests.
func (c *stateCache) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}

// reset drops every entry. Test-only seam: OnReady's package-level cache
// outlives a single test in the same package, exactly as notify's svc
// does (apps/notify/be/app_test.go cleanup).
func (c *stateCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = map[string]any{}
}
