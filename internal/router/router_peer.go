// Synthetic com.wash.router peer.
//
// Apps can address the router itself as a cross-app bus peer:
//
//   conn.SendAppMsgTo(wire.Recipient{AppID: "com.wash.router"},
//       map[string]any{"kind": "runtime.query", "id": "..."})
//
// The router short-circuits the cross-app relay (see
// relayAppMsgCrossInstance) and answers in-process, writing the
// reply back to the calling instance's event channel with
// From={AppID:"com.wash.router"} so the SDK bus's reply correlation
// picks it up like any other cross-app round-trip.
//
// Kinds today:
//   - runtime.query        → runtime.query_ok with the full table
//
// New kinds added here should keep the same id-echo discipline so
// the FE-side SDK bus's req_id/id correlation works without
// special-casing.

package router

import (
	"encoding/json"

	"github.com/sirmick/wash/pkg/wire"
)

// RouterPeerAppID is the synthetic app id apps address to reach the
// router as an in-process bus peer. Exported because the SDK and
// apps that want type-safety on the recipient string may want to
// reference it; keeping the magic string in one place beats sprinkling
// "com.wash.router" through callers.
const RouterPeerAppID = "com.wash.router"

// handleRouterPeerMsg dispatches an in-process bus call from
// inst → com.wash.router. data is the JSON-encoded payload; the
// envelope follows the SDK bus convention of {"kind": "...", "id":
// "..."} so the FE-side waiter correlates on the kind+id pair.
//
// All replies go back as cross-app EvtAppMsg with From set to a
// router-attested {AppID: "com.wash.router"}; the SDK's bus.Call
// matches on req_id (carried verbatim) and resolves the pending
// waiter on the originating instance.
func (r *Router) handleRouterPeerMsg(from *AppInstance, data json.RawMessage) error {
	var probe struct {
		Kind  string `json:"kind"`
		ID    string `json:"id,omitempty"`
		ReqID string `json:"req_id,omitempty"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		r.log("router peer: bad envelope from %s: %v", from.InstanceID, err)
		return nil
	}
	switch probe.Kind {
	case "runtime.query":
		return r.replyRuntimeQuery(from, probe.ID, probe.ReqID)
	}
	// Unknown kind — log and drop. App's SDK bus.Call will time out
	// rather than block forever.
	r.log("router peer: unknown kind %q from %s", probe.Kind, from.InstanceID)
	return nil
}

// replyRuntimeQuery builds the runtime table and writes it back to
// the calling instance as a runtime.query_ok envelope.
func (r *Router) replyRuntimeQuery(to *AppInstance, id, reqID string) error {
	table := r.collectRuntimeTable()
	out := map[string]any{
		"kind":            "runtime.query_ok",
		"rows":            table.Rows,
		"total_rss":       table.TotalRSS,
		"captured_at_sec": table.CapturedAtSec,
	}
	if id != "" {
		out["id"] = id
	}
	if reqID != "" {
		out["req_id"] = reqID
	}
	body, err := json.Marshal(out)
	if err != nil {
		r.log("router peer: marshal runtime.query_ok: %v", err)
		return nil
	}
	frame := wire.NewEvtAppMsgFrom(to.WindowID, json.RawMessage(body),
		wire.Sender{AppID: RouterPeerAppID})
	return to.WriteEvt(frame)
}
