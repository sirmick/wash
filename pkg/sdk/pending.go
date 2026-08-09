package sdk

import "sync"

// pendingCalls is a req-id-keyed registry of in-flight request/response
// calls. It collapses the half-dozen near-identical correlation maps the
// SDK used to hand-roll (channel opens, clipboard gets, ingress publishes,
// app restarts, window creates, and the cross-app bus.Call) into one
// generic helper.
//
// Safe for concurrent use. Each waiter registers a buffered (cap 1)
// channel under a req id; a single reply resolves it. resolve, cancel, and
// the reply path all delete-under-lock first, so at most one value is ever
// delivered to a waiter's channel — and because the buffer is 1 the
// delivering send never blocks, even if the waiter already walked away on
// a timeout or context cancel.
//
// K is the correlation-key type (uint64 for the writeEvt paths, string for
// bus.Call's minted req_id). T is the value delivered on the reply.
type pendingCalls[K comparable, T any] struct {
	mu      sync.Mutex
	waiters map[K]chan T
}

// newPendingCalls returns an initialised registry.
func newPendingCalls[K comparable, T any]() *pendingCalls[K, T] {
	return &pendingCalls[K, T]{waiters: make(map[K]chan T)}
}

// register installs a fresh cap-1 waiter channel under id and returns it.
// The caller blocks on the returned channel for the reply and must call
// cancel(id) on any abandon path (write failure, ctx cancel) so a never-
// arriving reply doesn't leak the entry.
func (p *pendingCalls[K, T]) register(id K) chan T {
	ch := make(chan T, 1)
	p.mu.Lock()
	p.waiters[id] = ch
	p.mu.Unlock()
	return ch
}

// resolve delivers v to the waiter registered under id and removes the
// entry. Returns true if a waiter was present; a resolve for an unknown or
// already-resolved id is a no-op returning false. The send is non-blocking
// against a cap-1 buffer that the single delete-under-lock guarantees is
// empty, so resolve never blocks and never panics on a closed channel.
func (p *pendingCalls[K, T]) resolve(id K, v T) bool {
	p.mu.Lock()
	ch, ok := p.waiters[id]
	if ok {
		delete(p.waiters, id)
	}
	p.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- v:
	default:
	}
	return true
}

// cancel removes the waiter registered under id without delivering a
// value. Idempotent — safe to call after a reply already resolved the id
// (the bus.Call path always defers a cancel for exactly this reason).
func (p *pendingCalls[K, T]) cancel(id K) {
	p.mu.Lock()
	delete(p.waiters, id)
	p.mu.Unlock()
}
