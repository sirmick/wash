package sdk

import (
	"context"

	"github.com/sirmick/wash/pkg/wire"
)

// Observation is an app's own export of what it is doing, for the
// router's observe verb (docs/COMMANDER.md §4.5): bounded, synchronous,
// side-effect free. Empty Content means "nothing to say" and the router
// falls back to what it holds itself.
type Observation struct {
	// ContentType is text/plain or application/json; empty means text.
	ContentType string
	Content     string
	// Revision changes whenever Content would; any string, opaque.
	Revision string
}

// HandleObserve installs the export the router asks for when this app's
// manifest says observation=export. fn runs off the SDK goroutine and
// must answer promptly — the router waits a quarter second, then falls
// back. Only one export per connection; the last installed wins.
//
//	sdk.HandleObserve(bus, func() sdk.Observation {
//		return sdk.Observation{Content: term.Summary(), Revision: term.Rev()}
//	})
func HandleObserve(bus *Bus, fn func() Observation) {
	c := bus.conn
	c.observeMu.Lock()
	c.observeFn = fn
	c.observeMu.Unlock()
}

// answerObserve serves one observe.request. The export runs on its own
// goroutine so an app whose export takes a lock never stalls dispatch;
// a missing export answers empty at once.
func (c *Conn) answerObserve(m wire.EvtObserveRequest) {
	c.observeMu.Lock()
	fn := c.observeFn
	c.observeMu.Unlock()
	if fn == nil {
		_ = c.writeEvt(wire.NewEvtObserveReply(m.ReqID, "", "", ""))
		return
	}
	go func() {
		o := fn()
		if len(o.Content) > wire.ObserveMaxBytes {
			o.Content = o.Content[:wire.ObserveMaxBytes]
		}
		_ = c.writeEvt(wire.NewEvtObserveReply(m.ReqID, o.ContentType, o.Content, o.Revision))
	}()
}

type observeResult struct {
	obs wire.Observation
	err error
}

// Observe asks the router for one observation of another instance
// (docs/COMMANDER.md §4.1). Requires the "observe" capability. maxBytes
// bounds the content (0 = the router's default tail). Like ClipboardGet,
// MUST NOT be called from an SDK dispatch callback — the answer comes
// back on the same read goroutine.
func (c *Conn) Observe(ctx context.Context, instanceID string, maxBytes int) (wire.Observation, error) {
	reqID := c.nextReqID.Add(1)
	wait := c.pendingObserve.register(reqID)
	if err := c.writeEvt(wire.NewEvtObserveGet(reqID, instanceID, maxBytes)); err != nil {
		c.pendingObserve.cancel(reqID)
		return wire.Observation{}, err
	}
	select {
	case <-ctx.Done():
		c.pendingObserve.cancel(reqID)
		return wire.Observation{}, ctx.Err()
	case r := <-wait:
		return r.obs, r.err
	}
}
