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
	"log"
	"sync"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/bulkops"
	"github.com/sirmick/wash/internal/sdk"
)

const version = "0.0.0"

// AppID is the reserved app id consumers address. Exported so other
// apps (fm, the session BE gateway) can reference it without
// duplicating the string literal.
const AppID = "com.wash.bulk"

// State is the public state shape published to subscribers. Two
// parallel lists:
//
//   - Jobs:      every job the manager is currently tracking. The
//                FE renders one row per entry; terminal-state jobs
//                stick briefly (manager auto-evicts) then drop.
//   - Conflicts: every pending conflict prompt blocking a worker.
//                The FE renders the FIRST one as a modal overlay;
//                resolving it advances the worker.
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
			Version:         version,
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
		bulkops.WithOnUpdate(jobUpdateHandler()),
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

func registerHandlers(b *sdk.Bus) {
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
	// cancel + resolve_conflict are fire-and-forget. Both are
	// addressable from any sender (the session BE gateway forwards on
	// behalf of the sidebar widget; nothing else legitimately calls
	// them). HandleVoid because no reply is meaningful — the state
	// fan-out reflects the outcome.
	sdk.HandleVoid(b, "cancel", func(_ *sdk.Conn, _ string, req cancelReq) error {
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
// (used by e2e waitForLog assertions) stays so existing tests can
// observe transitions.
func jobUpdateHandler() func(bulkops.Job) {
	return func(j bulkops.Job) {
		log.Printf("bulk-ops job=%s op=%s status=%s done=%d total=%d err=%q",
			j.ID, j.Op, j.Status, j.Done, j.Total, j.Error)
		svc.Mutate(func(s *State) {
			s.Jobs = jobsToViews(mgr.Jobs())
		})
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
	}
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

