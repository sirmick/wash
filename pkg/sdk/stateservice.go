// StateService is the canonical subscribe-with-snapshot pattern for
// wash background services (wash-bulk, wash-priv, com.wash.notify,
// future audio/clipboard/network agents).
//
// A service holds some opaque state S and publishes it to cross-app
// subscribers. Subscribers send {kind:"subscribe"} and receive a
// {kind:"state", state:<S>} reply with the current snapshot, plus the
// same shape on every subsequent Mutate. Subscribers can later send
// {kind:"unsubscribe"} when they no longer care.
//
// The wire shape is fixed by this helper so every service that uses
// it speaks the same protocol — consumer code can write one bus
// subscriber and reuse it across every background service.
//
// Lifecycle:
//
//	bus := sdk.NewBus(conn)
//	svc := sdk.NewStateService(bus, JobsState{Jobs: nil})
//	// ...later, on a job change:
//	svc.Mutate(func(s *JobsState) { s.Jobs[id] = job })
//
// Mutate fans the current snapshot to every registered subscriber via
// SendAppMsgTo. Cooperative subscribers send unsubscribe when they stop
// watching; services that opt into AppDef.OnInstanceGone can also call
// ForgetSubscriber to remove a crashed or disconnected instance as soon as
// the router tears it down.

package sdk

import (
	"sync"

	"github.com/sirmick/wash/internal/wire"
)

// StateServiceKind* constants are the wire vocabulary. Exported so
// the consumer side can use the same strings without duplicating.
const (
	StateServiceKindSubscribe   = "subscribe"
	StateServiceKindUnsubscribe = "unsubscribe"
	StateServiceKindState       = "state"
)

// StateService publishes state of type S to cross-app subscribers.
// Construct via NewStateService.
type StateService[S any] struct {
	bus *Bus

	mu    sync.RWMutex
	state S
	// subs is the set of subscriber instance ids. Map-as-set so add
	// is idempotent (a subscriber that double-subscribes after losing
	// track is one entry, not two).
	subs map[string]struct{}
}

// NewStateService installs subscribe/unsubscribe handlers on bus and
// returns a service holding the initial state. The handlers register
// for cross-app messages only (own-FE subscribe is meaningless — the
// service's own FE has direct access via the BE process).
//
// Panics if "subscribe" or "unsubscribe" is already registered on bus
// — programmer error to double-install StateService for one Bus.
func NewStateService[S any](bus *Bus, initial S) *StateService[S] {
	s := &StateService[S]{
		bus:   bus,
		state: initial,
		subs:  map[string]struct{}{},
	}
	HandleFromVoid(bus, StateServiceKindSubscribe, func(c *Conn, _ string, _ struct{}, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil // router never delivers cross-app msgs without InstanceID; defensive
		}
		s.mu.Lock()
		s.subs[from.InstanceID] = struct{}{}
		snap := s.state
		s.mu.Unlock()
		// Reply with the current snapshot. Subscribers rely on this
		// to recover quiescent state — e.g. a pending priv approval
		// that won't produce another update on its own.
		return c.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, statePayload(snap))
	})
	HandleFromVoid(bus, StateServiceKindUnsubscribe, func(_ *Conn, _ string, _ struct{}, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		s.mu.Lock()
		delete(s.subs, from.InstanceID)
		s.mu.Unlock()
		return nil
	})
	return s
}

// Snapshot returns a copy of the current state. The returned value
// is a shallow copy — fields of S that hold pointers or slices
// reference the same underlying memory, so a caller that mutates
// those will corrupt the service's view. Treat the snapshot as
// read-only; use Mutate to modify.
//
// Subtler hazard: because the backing storage is shared, even a
// read-only snapshot holder DATA-RACES a concurrent Mutate that
// writes a slice/map element IN PLACE (e.g. s.Items[i].Field = x).
// Mutate fns must therefore be copy-on-write for any slice/map a
// snapshot may outlive — rebuild the slice rather than poking an
// element — OR all Snapshot/Mutate calls must stay on one goroutine.
// `make test-race` catches violations.
func (s *StateService[S]) Snapshot() S {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Mutate applies fn under the write lock, then fans the resulting
// snapshot to every subscriber. fn must NOT call any other method on
// the service — that would re-enter the lock and deadlock.
//
// The snapshot shipped to subscribers is the state AFTER fn returns.
// Multiple concurrent Mutate calls serialize; subscribers see one
// state event per Mutate, in the order Mutate returned.
func (s *StateService[S]) Mutate(fn func(*S)) {
	s.mu.Lock()
	fn(&s.state)
	snap := s.state
	subs := make([]string, 0, len(s.subs))
	for k := range s.subs {
		subs = append(subs, k)
	}
	s.mu.Unlock()

	payload := statePayload(snap)
	conn := s.bus.Conn()
	for _, inst := range subs {
		// SendAppMsgTo's error path is "could not write to local
		// socket" — a transport failure that affects everything, not
		// per-recipient. The router reports gone instances through
		// AppDef.OnInstanceGone; services that install that callback can
		// drop stale subscribers there.
		_ = conn.SendAppMsgTo(wire.Recipient{InstanceID: inst}, payload)
	}
}

// ForgetSubscriber removes a subscriber instance without changing the
// service's state. It is for router lifecycle cleanup: a dead viewer should
// stop receiving pushes, but the background service's server-side buffer or
// roster stays intact.
func (s *StateService[S]) ForgetSubscriber(instanceID string) {
	if instanceID == "" {
		return
	}
	s.mu.Lock()
	delete(s.subs, instanceID)
	s.mu.Unlock()
}

// SubscriberCount returns the number of currently-registered
// subscribers. Useful for service diagnostics and tests.
func (s *StateService[S]) SubscriberCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.subs)
}

// statePayload wraps an S value in the {kind:"state", state:<S>}
// envelope. Kept as a single chokepoint so the wire shape is
// definitely consistent across subscribe-reply and Mutate-broadcast.
func statePayload[S any](s S) map[string]any {
	return map[string]any{
		"kind":  StateServiceKindState,
		"state": s,
	}
}
