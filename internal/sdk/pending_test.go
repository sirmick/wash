package sdk

import (
	"sync"
	"testing"
)

// register → resolve delivers the value on the waiter's channel.
func TestPendingCallsResolveDelivers(t *testing.T) {
	p := newPendingCalls[uint64, int]()
	ch := p.register(7)
	if !p.resolve(7, 42) {
		t.Fatal("resolve of a registered id returned false")
	}
	if got := <-ch; got != 42 {
		t.Fatalf("delivered value = %d, want 42", got)
	}
}

// resolve of an id that was never registered (or already resolved) is a
// no-op returning false, and does not block.
func TestPendingCallsResolveUnknown(t *testing.T) {
	p := newPendingCalls[uint64, int]()
	if p.resolve(99, 1) {
		t.Fatal("resolve of an unknown id returned true")
	}
	// Double-resolve: the second must report false.
	p.register(1)
	if !p.resolve(1, 10) {
		t.Fatal("first resolve returned false")
	}
	if p.resolve(1, 20) {
		t.Fatal("second resolve of the same id returned true")
	}
}

// cancel removes the waiter so a later resolve is a no-op — it neither
// blocks nor panics, and nothing is delivered. This is the timeout / ctx
// abandon path.
func TestPendingCallsCancelThenLateResolve(t *testing.T) {
	p := newPendingCalls[uint64, int]()
	ch := p.register(3)
	p.cancel(3)
	if p.resolve(3, 5) {
		t.Fatal("resolve after cancel returned true")
	}
	select {
	case v := <-ch:
		t.Fatalf("cancelled waiter received a value: %d", v)
	default:
	}
	// cancel is idempotent — a second cancel is safe.
	p.cancel(3)
}

// A string-keyed registry behaves identically — this is the bus.Call shape.
func TestPendingCallsStringKey(t *testing.T) {
	p := newPendingCalls[string, map[string]any]()
	ch := p.register("c-abc")
	if !p.resolve("c-abc", map[string]any{"ok": true}) {
		t.Fatal("resolve returned false")
	}
	m := <-ch
	if m["ok"] != true {
		t.Fatalf("delivered map = %v", m)
	}
}

// Concurrent register/resolve/cancel under -race: each id is resolved or
// cancelled by exactly one goroutine, and no send ever blocks (cap-1 buffer
// + delete-under-lock guarantee a single delivery per id).
func TestPendingCallsConcurrent(t *testing.T) {
	p := newPendingCalls[uint64, int]()
	const n = 500
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		id := uint64(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch := p.register(id)
			// Race a resolver against a cancel from another goroutine.
			done := make(chan struct{})
			go func() {
				p.resolve(id, int(id))
				close(done)
			}()
			select {
			case v := <-ch:
				if v != int(id) {
					t.Errorf("id %d got %d", id, v)
				}
			case <-done:
				// resolver finished but the value may already be buffered.
				select {
				case v := <-ch:
					if v != int(id) {
						t.Errorf("id %d got %d", id, v)
					}
				default:
				}
			}
		}()
	}
	wg.Wait()
	// Registry must be empty afterwards — every id was resolved.
	p.mu.Lock()
	left := len(p.waiters)
	p.mu.Unlock()
	if left != 0 {
		t.Fatalf("registry leaked %d waiters", left)
	}
}

// A resolve racing a cancel must leave the registry empty regardless of
// which won, and must never block. Exercises the abandon-vs-reply race that
// the real timeout paths hit.
func TestPendingCallsResolveCancelRace(t *testing.T) {
	const n = 1000
	for i := 0; i < n; i++ {
		p := newPendingCalls[uint64, int]()
		p.register(1)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); p.resolve(1, 1) }()
		go func() { defer wg.Done(); p.cancel(1) }()
		wg.Wait()
		p.mu.Lock()
		left := len(p.waiters)
		p.mu.Unlock()
		if left != 0 {
			t.Fatalf("iter %d: registry leaked %d waiters", i, left)
		}
	}
}
