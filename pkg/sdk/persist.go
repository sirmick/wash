package sdk

import "log"

// HandlePersist installs the canonical "save_state" handler on b.
//
// wash keeps per-window view state on the BACKEND so reconnect and
// multi-stream reconstitute the window: the FE ships an opaque blob,
// the router stores it keyed by instance, and the shell redelivers it
// as a wash:state event on every (re)mount. The FE→router hop is a
// fixed wire shape — { kind:"save_state", state:<blob> } → Conn.SaveState
// — so every windowed app repeated the same three-line handler (and a
// few simply forgot, losing their view state on reconnect).
//
// HandlePersist is that handler as a one-liner. Pair it with the FE's
// createAppBus({ onState }).saveState(). The blob is the app's own
// schema; the router never inspects it.
//
//	bus := sdk.NewBus(c)
//	sdk.HandlePersist(bus)
//
// Panics (via the underlying dispatch table) if "save_state" is already
// registered on b — don't install it twice.
func HandlePersist(b *Bus) {
	HandleVoid(b, "save_state", func(c *Conn, _ string, req persistStateReq) error {
		if err := c.SaveState(req.State); err != nil {
			return err
		}
		// Audit trail of the FE→router persist (previously each app logged
		// its own ad-hoc line). Debounced FE-side, so not per-keystroke.
		log.Printf("bus: %s save_state persisted", b.appID())
		return nil
	})
}

// persistStateReq is the canonical { state: <blob> } shape the FE's
// AppBus.saveState() emits. State is decoded as an opaque value and
// re-marshalled by Conn.SaveState — the schema belongs to the app.
type persistStateReq struct {
	State any `json:"state"`
}
