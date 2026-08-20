// fm's view of its host's bulk queue (docs/SIDEBAR.md M3a).
//
// Long-running file operations belong to com.wash.bulk, not to the window
// that started one: closing fm has never killed a copy, and the job keeps
// running with nothing pointing at it. That is precisely why the queue
// could not simply live in the fm window — and why the desktop rail grew a
// cancel button instead.
//
// The rail's cancel could only ever reach the LOCAL host, because a
// shell-originated cross-app send carries no router-attested sender and so
// has to route through the session BE gateway, which resolves inside its
// own router. fm has no such problem: it is an app, its sends are attested
// by construction, and `launchOn(origin, "com.wash.fm")` therefore gives
// working job control on any host with no new addressing.
//
// So fm subscribes to the bulk singleton and renders whatever it holds.
// Any fm window on host X lists — and can cancel — every one of X's jobs,
// including jobs whose originating window is long gone.
//
// Wire shape:
//
//	fm → bulk   {kind:"subscribe"}                     (at ready)
//	bulk → fm   {kind:"state", state:{jobs,conflicts}} (on change)
//	fm → FE     {kind:"bulk.state", state:…}           (verbatim)
//	FE → fm     {kind:"bulk_cancel", job_id}           → bulk {kind:"cancel"}
package fm

import (
	"log"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// subscribeToBulk asks the bulk singleton for its queue. Addressed by app
// id, which resolveRecipient spawns on demand — so an fm window is enough
// to bring the service up, exactly as the session BE's subscribe was.
func subscribeToBulk(c *sdk.Conn) {
	if err := c.SendAppMsgTo(wire.Recipient{AppID: bulkAppID}, map[string]any{
		"kind": sdk.StateServiceKindSubscribe,
	}); err != nil {
		log.Printf("wash-fm: bulk subscribe: %v", err)
	}
}

// registerJobHandlers wires the queue in both directions.
func registerJobHandlers(b *sdk.Bus) {
	// bulk → fm: the queue changed. Forwarded to the FE under a
	// service-specific kind so the FE's dispatch stays unambiguous — the
	// same re-branding the session BE does, for the same reason.
	//
	// HandleFromVoid, and the sender is checked: `state` is a generic kind
	// (every StateService publishes it), so without the guard any service
	// fm ever subscribes to would land in the jobs strip.
	sdk.HandleFromVoid(b, sdk.StateServiceKindState, func(c *sdk.Conn, _ string, req bulkStateMsg, from wire.Sender) error {
		if from.AppID != bulkAppID {
			return nil
		}
		return c.SendAppMsg(map[string]any{"kind": "bulk.state", "state": req.State})
	})

	// FE → fm → bulk. fm adds no policy of its own: it is the app speaking
	// for the person looking at the window, and its only job is to make
	// the sender attestation correct.
	sdk.HandleVoid(b, "bulk_cancel", func(c *sdk.Conn, _ string, req bulkJobReq) error {
		if req.JobID == "" {
			return nil
		}
		log.Printf("wash-fm: cancel job=%s", req.JobID)
		return c.SendAppMsgTo(wire.Recipient{AppID: bulkAppID}, map[string]any{
			"kind":   "cancel",
			"job_id": req.JobID,
		})
	})

	// A conflict BLOCKS its worker until somebody answers, which is what
	// makes answering it a file manager's job rather than the desktop's.
	// Idempotent by job id and first-answer-wins on the service side, so
	// two fm windows racing on one conflict is safe.
	sdk.HandleVoid(b, "bulk_resolve_conflict", func(c *sdk.Conn, _ string, req bulkResolveReq) error {
		if req.JobID == "" || req.Action == "" {
			return nil
		}
		log.Printf("wash-fm: resolve conflict job=%s action=%s", req.JobID, req.Action)
		return c.SendAppMsgTo(wire.Recipient{AppID: bulkAppID}, map[string]any{
			"kind":   "resolve_conflict",
			"job_id": req.JobID,
			"action": req.Action,
		})
	})
}

// bulkStateMsg captures the `state` field of a StateService push. Opaque
// and forwarded verbatim: `any`, never json.RawMessage/[]byte, because the
// router base64-encodes byte strings on the FE-bound hop.
type bulkStateMsg struct {
	State any `json:"state"`
}

type bulkJobReq struct {
	JobID string `json:"job_id"`
}

type bulkResolveReq struct {
	JobID  string `json:"job_id"`
	Action string `json:"action"`
}
