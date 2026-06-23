// Package bulk is wash-bulk — the background-surface singleton that
// runs queued file operations (delete/move/copy) for fm and any
// future Files UI.
//
// Architecturally:
//   - surface: background. No window, no FE bundle, no launcher entry.
//     Autoboot on first shell connect. Consumers (fm, the sidebar
//     BulkWidget) address it cross-app via the singleton app_id.
//   - State is published via sdk.StateService — the canonical
//     subscribe-with-snapshot pattern. Subscribers receive the full
//     queue + active conflict list on subscribe and on every change.
//   - The work happens in internal/bulkops; this binary is a thin
//     wire shim plus the conflict-resolution coordination.
//   - fs.watch (in fm) provides the live-tree refresh — wash-bulk
//     emits no fs notifications of its own.
package bulk

import (
	"context"
	"errors"
	"fmt"
	"github.com/sirmick/wash/internal/version"
	"log"
	"sync"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/bulkops"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

// AppID is the reserved app id consumers address. Exported so other
// apps (fm, the session BE gateway) can reference it without
// duplicating the string literal.
const AppID = "com.wash.bulk"

// State is the public state shape published to subscribers. Two
// parallel lists:
//
//   - Jobs:      every job the manager is currently tracking. The
//     FE renders one row per entry; terminal-state jobs
//     stick briefly (manager auto-evicts) then drop.
//   - Conflicts: every pending conflict prompt blocking a worker.
//     The FE renders the FIRST one as a modal overlay;
//     resolving it advances the worker.
type State struct {
	Jobs      []JobView      `json:"jobs"`
	Conflicts []ConflictView `json:"conflicts"`
}

// JobView mirrors the public bulkops.Job fields the FE renders.
type JobView struct {
	JobID  string   `json:"job_id"`
	Op     string   `json:"op"`
	Status string   `json:"status"`
	Paths  []string `json:"paths"`
	Dest   string   `json:"dest"`
	Done   int      `json:"done"`
	Total  int      `json:"total"`
	Error  string   `json:"error"`
}

// ConflictView is one entry in State.Conflicts. The worker for this
// job is BLOCKED on the user's choice; a `resolve_conflict` for the
// matching JobID unblocks it.
type ConflictView struct {
	JobID   string `json:"job_id"`
	Src     string `json:"src"`
	SrcType string `json:"src_type"`
	Dst     string `json:"dst"`
	DstType string `json:"dst_type"`
}

var (
	def *sdk.AppDef
	mgr *bulkops.Manager
	svc *sdk.StateService[State]

	// external holds jobs driven by another app (fm uploads) that bulk
	// mirrors but doesn't execute. See external.go.
	external = newExternalStore()

	// pendingConflicts maps job_id → the channel the worker
	// goroutine blocks on. Resolve_conflict writes to that channel
	// and removes the entry; terminal-state cleanup releases it
	// with ConflictCancel.
	pendingConflictsMu sync.Mutex
	pendingConflicts   = map[string]chan bulkops.ConflictAction{}
)

func init() {
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Bulk Ops",
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Surface:         sdk.SurfaceBackground,
			Instancing:      sdk.InstancingSingleton,
		},
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-bulk",
		Manifest: def.Manifest,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

func onReady(c *sdk.Conn, instanceID string, _ uint32) {
	log.Printf("wash-bulk ready instance=%s", instanceID)
	bus := sdk.NewBus(c)
	svc = sdk.NewStateService(bus, State{})
	mgr = bulkops.New(
		bulkops.WithOnUpdate(jobUpdateHandler(c)),
		bulkops.WithOnConflict(conflictHandler(c)),
	)
	registerHandlers(bus)
}

// ----- request types -----

type enqueueReq struct {
	Op    string   `json:"op"`
	Paths []string `json:"paths"`
	Dest  string   `json:"dest"`
}

type enqueueResp struct {
	JobID string `json:"job_id"`
}

type cancelReq struct {
	JobID string `json:"job_id"`
}

type resolveConflictReq struct {
	JobID  string `json:"job_id"`
	Action string `json:"action"`
}

// jobReportReq is the upsert another app sends to mirror a job it runs
// itself (fm's uploads). Repeated for progress; the terminal status
// (done/failed/cancelled) is the last report. Done/Total are byte
// counts for uploads — the BulkWidget renders the fraction op-agnostic.
type jobReportReq struct {
	JobID    string   `json:"job_id"`
	Instance string   `json:"instance"`
	Op       string   `json:"op"`
	Paths    []string `json:"paths"`
	Dest     string   `json:"dest"`
	Status   string   `json:"status"`
	Done     int      `json:"done"`
	Total    int      `json:"total"`
	Error    string   `json:"error"`
}

func registerHandlers(b *sdk.Bus) {
	c := b.Conn()
	// enqueue: callable from any sender (fm's shell-originated send
	// has no From attestation, so we use plain Handle which doesn't
	// require it). Returns the new job_id in the reply envelope so the
	// caller can correlate.
	sdk.Handle(b, "enqueue", func(_ *sdk.Conn, _ string, req enqueueReq) (enqueueResp, error) {
		if len(req.Paths) == 0 {
			return enqueueResp{}, sdk.Errf(sdk.ErrBadRequest, "paths is empty")
		}
		return enqueueResp{JobID: mgr.Enqueue(bulkops.Op(req.Op), req.Paths, req.Dest)}, nil
	})
	// job_report: upsert an externally-driven job (fm uploads). The
	// reporting app does the work + streams progress; we mirror it into
	// state. Fire-and-forget; the fan-out is the acknowledgement.
	sdk.HandleVoid(b, "job_report", func(_ *sdk.Conn, _ string, req jobReportReq) error {
		if req.JobID == "" {
			return nil
		}
		// Same "bulk-ops job=" prefix the worker jobs log under, so e2e
		// waitForLog assertions observe upload transitions identically.
		log.Printf("bulk-ops job=%s op=%s status=%s done=%d total=%d err=%q",
			req.JobID, req.Op, req.Status, req.Done, req.Total, req.Error)
		external.upsert(JobView{
			JobID: req.JobID, Op: req.Op, Status: req.Status,
			Paths: req.Paths, Dest: req.Dest,
			Done: req.Done, Total: req.Total, Error: req.Error,
		}, req.Instance)
		publishJobs()
		return nil
	})
	// cancel + resolve_conflict are fire-and-forget. Both are
	// addressable from any sender (the session BE gateway forwards on
	// behalf of the sidebar widget; nothing else legitimately calls
	// them). HandleVoid because no reply is meaningful — the state
	// fan-out reflects the outcome.
	sdk.HandleVoid(b, "cancel", func(_ *sdk.Conn, _ string, req cancelReq) error {
		// External (upload) jobs aren't in the worker queue — relay the
		// cancel to the owning fm window, which tears down the transfer
		// and reports the cancelled status back via job_report.
		if owner, ok := external.ownerOf(req.JobID); ok {
			return c.SendAppMsgTo(wire.Recipient{InstanceID: owner}, map[string]any{
				"kind": "upload_cancel", "upload_id": req.JobID,
			})
		}
		if !mgr.Cancel(req.JobID) {
			log.Printf("wash-bulk: cancel for unknown job %q", req.JobID)
		}
		return nil
	})
	sdk.HandleVoid(b, "resolve_conflict", func(_ *sdk.Conn, _ string, req resolveConflictReq) error {
		pendingConflictsMu.Lock()
		ch, ok := pendingConflicts[req.JobID]
		pendingConflictsMu.Unlock()
		if !ok {
			log.Printf("wash-bulk: resolve_conflict for unknown job %q", req.JobID)
			return nil
		}
		select {
		case ch <- bulkops.ConflictAction(req.Action):
		default:
		}
		return nil
	})
}

// conflictHandler is the BE-side glue between the library's
// askConflict (which BLOCKS the worker until the user chooses) and
// the StateService. Adds the pending conflict to state so subscribers
// render the overlay, blocks on the worker's per-job channel, and
// removes the entry on resolution.
func conflictHandler(c *sdk.Conn) func(bulkops.ConflictInfo) bulkops.ConflictAction {
	return func(info bulkops.ConflictInfo) bulkops.ConflictAction {
		ch := make(chan bulkops.ConflictAction, 1)
		pendingConflictsMu.Lock()
		pendingConflicts[info.Job.ID] = ch
		pendingConflictsMu.Unlock()
		// Add to public state so the sidebar widget pops its overlay.
		svc.Mutate(func(s *State) {
			s.Conflicts = append(s.Conflicts, ConflictView{
				JobID: info.Job.ID, Src: info.Src, SrcType: info.SrcType,
				Dst: info.Dst, DstType: info.DstType,
			})
		})
		// Block until the FE answers (or jobUpdateHandler cancels us
		// from a terminal-state transition).
		action := <-ch
		// Remove from pending + state on resolution.
		pendingConflictsMu.Lock()
		delete(pendingConflicts, info.Job.ID)
		pendingConflictsMu.Unlock()
		svc.Mutate(func(s *State) {
			out := s.Conflicts[:0]
			for _, c := range s.Conflicts {
				if c.JobID != info.Job.ID {
					out = append(out, c)
				}
			}
			s.Conflicts = out
		})
		return action
	}
}

// jobUpdateHandler builds the onUpdate callback that pushes every
// queue transition into the StateService state. The router log emit
// is what e2e waitForLog assertions observe transitions through.
func jobUpdateHandler(c *sdk.Conn) func(bulkops.Job) {
	return func(j bulkops.Job) {
		log.Printf("bulk-ops job=%s op=%s status=%s done=%d total=%d err=%q",
			j.ID, j.Op, j.Status, j.Done, j.Total, j.Error)
		publishJobs()
		// Terminal-state cleanup: release any pending conflict
		// channel so a still-blocked worker (e.g. after a user-cancel)
		// doesn't leak the goroutine indefinitely. Cancel wins over
		// any FE answer that might still be in flight.
		switch j.Status {
		case bulkops.StatusCancelled, bulkops.StatusFailed, bulkops.StatusDone:
			pendingConflictsMu.Lock()
			ch, ok := pendingConflicts[j.ID]
			pendingConflictsMu.Unlock()
			if ok {
				select {
				case ch <- bulkops.ConflictCancel:
				default:
				}
			}
		}
		// User-facing toast on terminal states. Cancelled is omitted —
		// the user just initiated it, a toast would be noise.
		switch j.Status {
		case bulkops.StatusDone:
			c.Info(opVerb(j.Op, true), opSummary(j))
		case bulkops.StatusFailed:
			c.Fail(opVerb(j.Op, false), errors.New(j.Error))
		}
	}
}

// opVerb renders the toast title for a bulk op outcome, e.g.
// ("move", true) -> "Move complete", ("copy", false) -> "Copy failed".
func opVerb(op bulkops.Op, ok bool) string {
	name := "Operation"
	switch op {
	case bulkops.OpDelete:
		name = "Delete"
	case bulkops.OpMove:
		name = "Move"
	case bulkops.OpCopy:
		name = "Copy"
	}
	if ok {
		return name + " complete"
	}
	return name + " failed"
}

// opSummary renders the toast body: item count plus destination for
// move/copy. "3 items" / "3 items → /home/mick/dst".
func opSummary(j bulkops.Job) string {
	noun := "item"
	if len(j.Paths) != 1 {
		noun = "items"
	}
	s := fmt.Sprintf("%d %s", len(j.Paths), noun)
	if j.Dest != "" {
		s += " → " + j.Dest
	}
	return s
}

// publishJobs republishes State.Jobs as the worker-driven jobs
// followed by the externally-driven (upload) jobs. Both the manager's
// onUpdate and job_report route through here so neither path clobbers
// the other's rows — State.Jobs is set wholesale on every mutate.
func publishJobs() {
	ext := external.views()
	svc.Mutate(func(s *State) {
		jobs := jobsToViews(mgr.Jobs())
		s.Jobs = append(jobs, ext...)
	})
}

// jobsToViews maps the bulkops public Job type to the wire JobView
// shape. Kept as a helper so jobUpdateHandler stays focused.
func jobsToViews(jobs []bulkops.Job) []JobView {
	out := make([]JobView, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, JobView{
			JobID: j.ID, Op: string(j.Op), Status: string(j.Status),
			Paths: j.Paths, Dest: j.Dest, Done: j.Done, Total: j.Total, Error: j.Error,
		})
	}
	return out
}
