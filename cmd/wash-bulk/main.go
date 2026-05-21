// wash-bulk — singleton app that processes long-running file
// operations (delete / move / copy) for other apps. fm and any
// future "Files" UI ask wash-bulk to do recursive deletes and
// bulk moves/copies; wash-bulk runs them on a queue and pushes
// progress updates to its own FE.
//
// Architecturally:
//   - instancing: "singleton" — there's only ever one wash-bulk;
//     other apps address it by app_id sentinel (see [[wash wire]]).
//   - The work happens in internal/bulkops; this binary is a thin
//     wire-and-progress shim around it.
//   - fs.watch (in fm) provides the live-tree refresh — wash-bulk
//     emits no fs notifications of its own.
package main

import (
	"embed"
	"io/fs"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/bulkops"
	"github.com/sirmick/wash/internal/sdk"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.0.0"

var mgr *bulkops.Manager
var conn *sdk.Conn

// pendingConflicts tracks active conflict prompts: the worker
// goroutine writes a request and blocks on its channel; the FE's
// conflict_resolve message looks up the channel by job_id and
// delivers the user's action. Cancel-driven cleanup happens in
// the onUpdate handler when the job reaches a terminal state.
var pendingConflicts = struct {
	sync.Mutex
	m map[string]chan bulkops.ConflictAction
}{m: map[string]chan bulkops.ConflictAction{}}

func main() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("wash-bulk: assets: %v", err)
	}
	sdk.Main(&sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.bulk",
			Name:            "Bulk Ops",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-bulk",
			Surface:         sdk.SurfaceWindow,
			Icon:            bulkIcon,
			Instancing:      sdk.InstancingSingleton,
			Window:          &sdk.WindowHints{DefaultWidth: 480, DefaultHeight: 360},
		},
		Assets:   sub,
		OnReady:  onReady,
		OnAppMsg: onAppMsg,
	})
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	conn = c
	log.Printf("wash-bulk ready instance=%s window=%d", instanceID, windowID)
	opts := []bulkops.Option{
		bulkops.WithOnUpdate(jobUpdateHandler(c)),
		bulkops.WithOnConflict(conflictHandler(c)),
	}
	// WASH_BULKOPS_ITEM_DELAY_MS slows the worker between items —
	// off in production (zero), set to e.g. 200 for demos so the
	// FE's progress bar can actually be watched advance.
	if s := os.Getenv("WASH_BULKOPS_ITEM_DELAY_MS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			opts = append(opts, bulkops.WithItemDelay(time.Duration(n)*time.Millisecond))
			log.Printf("wash-bulk: item delay = %dms", n)
		}
	}
	mgr = bulkops.New(opts...)
}

// conflictHandler is the BE-side glue between the library's
// askConflict (which BLOCKS the worker until the user chooses) and
// the FE's prompt UI. We register a per-job channel, push a
// `job.conflict` event to the FE, and wait. The matching
// `conflict_resolve` app_msg writes into the channel.
func conflictHandler(c *sdk.Conn) func(bulkops.Job, string, string) bulkops.ConflictAction {
	return func(job bulkops.Job, src, dst string) bulkops.ConflictAction {
		ch := make(chan bulkops.ConflictAction, 1)
		pendingConflicts.Lock()
		pendingConflicts.m[job.ID] = ch
		pendingConflicts.Unlock()
		defer func() {
			pendingConflicts.Lock()
			delete(pendingConflicts.m, job.ID)
			pendingConflicts.Unlock()
		}()
		_ = c.SendAppMsg(map[string]any{
			"kind":   "job.conflict",
			"job_id": job.ID,
			"src":    src,
			"dst":    dst,
		})
		return <-ch
	}
}

// jobUpdateHandler builds the onUpdate callback that fans every
// queue transition to both the router log (for e2e waitForLog
// assertions) and the FE (which renders the queue UI).
func jobUpdateHandler(c *sdk.Conn) func(bulkops.Job) {
	return func(j bulkops.Job) {
		log.Printf("bulk-ops job=%s op=%s status=%s done=%d total=%d err=%q",
			j.ID, j.Op, j.Status, j.Done, j.Total, j.Error)
		_ = c.SendAppMsg(map[string]any{
			"kind":   "job.update",
			"job_id": j.ID,
			"op":     string(j.Op),
			"status": string(j.Status),
			"paths":  j.Paths,
			"dest":   j.Dest,
			"done":   j.Done,
			"total":  j.Total,
			"error":  j.Error,
		})
		// Terminal-state cleanup: release any pending conflict
		// channel for this job so a still-blocked worker (e.g.
		// after a user-cancel) doesn't leak the goroutine
		// indefinitely. Cancel wins over any FE answer that
		// might still be in flight.
		switch j.Status {
		case bulkops.StatusCancelled, bulkops.StatusFailed, bulkops.StatusDone:
			pendingConflicts.Lock()
			ch, ok := pendingConflicts.m[j.ID]
			delete(pendingConflicts.m, j.ID)
			pendingConflicts.Unlock()
			if ok {
				select {
				case ch <- bulkops.ConflictCancel:
				default:
				}
			}
		}
	}
}

func onAppMsg(c *sdk.Conn, _ uint32, data any) {
	m, ok := data.(map[any]any)
	if !ok {
		return
	}
	kind, _ := m["kind"].(string)
	id, _ := m["id"].(string)
	switch kind {
	case "enqueue":
		op, _ := m["op"].(string)
		paths := toStringSlice(m["paths"])
		dest, _ := m["dest"].(string)
		if len(paths) == 0 {
			_ = c.SendAppMsg(map[string]any{
				"kind": "enqueue_err", "id": id,
				"code": "bad_request", "msg": "paths is empty",
			})
			return
		}
		jobID := mgr.Enqueue(bulkops.Op(op), paths, dest)
		_ = c.SendAppMsg(map[string]any{
			"kind":   "enqueue_ok",
			"id":     id,
			"job_id": jobID,
		})
	case "cancel":
		jobID, _ := m["job_id"].(string)
		if !mgr.Cancel(jobID) {
			_ = c.SendAppMsg(map[string]any{
				"kind": "cancel_err", "id": id,
				"code": "not_found", "msg": jobID,
			})
			return
		}
		_ = c.SendAppMsg(map[string]any{"kind": "cancel_ok", "id": id, "job_id": jobID})
	case "conflict_resolve":
		jobID, _ := m["job_id"].(string)
		action, _ := m["action"].(string)
		pendingConflicts.Lock()
		ch, ok := pendingConflicts.m[jobID]
		pendingConflicts.Unlock()
		if !ok {
			_ = c.SendAppMsg(map[string]any{
				"kind": "conflict_resolve_err", "id": id,
				"code": "not_found", "msg": "no pending conflict for " + jobID,
			})
			return
		}
		select {
		case ch <- bulkops.ConflictAction(action):
		default:
		}
		_ = c.SendAppMsg(map[string]any{"kind": "conflict_resolve_ok", "id": id, "job_id": jobID})
	case "list":
		// FE-side caller asks for the current state of the queue.
		// Used on mount to seed the UI.
		jobs := mgr.Jobs()
		out := make([]map[string]any, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, map[string]any{
				"job_id": j.ID,
				"op":     string(j.Op),
				"status": string(j.Status),
				"paths":  j.Paths,
				"dest":   j.Dest,
				"done":   j.Done,
				"total":  j.Total,
				"error":  j.Error,
			})
		}
		_ = c.SendAppMsg(map[string]any{"kind": "list_ok", "id": id, "jobs": out})
	}
}

// toStringSlice converts a CBOR-decoded array-of-strings (which
// comes through as []any) into []string. Silently drops non-string
// entries — defensive but not strict.
func toStringSlice(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	}
	return nil
}

// bulkIcon — Lucide sprite symbol name. Same delivery as other apps
// (shell renders via <svg><use href="/icons.svg#layers"/></svg>).
const bulkIcon = "layers"
