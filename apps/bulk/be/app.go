// Package bulk is the compiled-in form of wash-bulk — the singleton
// app that runs queued file operations (delete/move/copy) for fm and
// any future Files UI.
//
// Architecturally:
//   - instancing: "singleton" — there's only ever one wash-bulk;
//     other apps address it by app_id sentinel (see [[wash wire]]).
//   - The work happens in internal/bulkops; this binary is a thin
//     wire-and-progress shim around it.
//   - fs.watch (in fm) provides the live-tree refresh — wash-bulk
//     emits no fs notifications of its own.
package bulk

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"sync"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/bulkops"
	"github.com/sirmick/wash/internal/sdk"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.0.0"

// bulkIcon — Lucide sprite symbol name.
const bulkIcon = "list-checks"

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

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-bulk: assets: " + err.Error())
	}
	def = &sdk.AppDef{
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
		Assets:  sub,
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-bulk",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

var bus *sdk.Bus

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	conn = c
	log.Printf("wash-bulk ready instance=%s window=%d", instanceID, windowID)
	mgr = bulkops.New(
		bulkops.WithOnUpdate(jobUpdateHandler(c)),
		bulkops.WithOnConflict(conflictHandler(c)),
	)
	bus = sdk.NewBus(c)
	registerHandlers(bus)
}

// ----- request/response types -----

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

type cancelResp struct {
	JobID string `json:"job_id"`
}

type conflictResolveReq struct {
	JobID  string `json:"job_id"`
	Action string `json:"action"`
}

type conflictResolveResp struct {
	JobID string `json:"job_id"`
}

type listResp struct {
	Jobs []jobView `json:"jobs"`
}

type jobView struct {
	JobID  string   `json:"job_id"`
	Op     string   `json:"op"`
	Status string   `json:"status"`
	Paths  []string `json:"paths"`
	Dest   string   `json:"dest"`
	Done   int      `json:"done"`
	Total  int      `json:"total"`
	Error  string   `json:"error"`
}

func registerHandlers(b *sdk.Bus) {
	sdk.Handle(b, "enqueue", func(_ *sdk.Conn, _ string, req enqueueReq) (enqueueResp, error) {
		if len(req.Paths) == 0 {
			return enqueueResp{}, sdk.Errf(sdk.ErrBadRequest, "paths is empty")
		}
		return enqueueResp{JobID: mgr.Enqueue(bulkops.Op(req.Op), req.Paths, req.Dest)}, nil
	})
	sdk.Handle(b, "cancel", func(_ *sdk.Conn, _ string, req cancelReq) (cancelResp, error) {
		if !mgr.Cancel(req.JobID) {
			return cancelResp{}, sdk.Err{Code: sdk.ErrNotFound, Msg: req.JobID}
		}
		return cancelResp{JobID: req.JobID}, nil
	})
	sdk.Handle(b, "conflict_resolve", func(_ *sdk.Conn, _ string, req conflictResolveReq) (conflictResolveResp, error) {
		pendingConflicts.Lock()
		ch, ok := pendingConflicts.m[req.JobID]
		pendingConflicts.Unlock()
		if !ok {
			return conflictResolveResp{}, sdk.Err{Code: sdk.ErrNotFound, Msg: "no pending conflict for " + req.JobID}
		}
		select {
		case ch <- bulkops.ConflictAction(req.Action):
		default:
		}
		return conflictResolveResp{JobID: req.JobID}, nil
	})
	sdk.Handle(b, "list", func(_ *sdk.Conn, _ string, _ struct{}) (listResp, error) {
		jobs := mgr.Jobs()
		out := make([]jobView, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, jobView{
				JobID: j.ID, Op: string(j.Op), Status: string(j.Status),
				Paths: j.Paths, Dest: j.Dest, Done: j.Done, Total: j.Total, Error: j.Error,
			})
		}
		return listResp{Jobs: out}, nil
	})
}

// conflictHandler is the BE-side glue between the library's
// askConflict (which BLOCKS the worker until the user chooses) and
// the FE's prompt UI. We register a per-job channel, push a
// `job.conflict` event to the FE (with src/dst types so the FE can
// render Merge vs Replace vs destructive prompts), and wait. The
// matching `conflict_resolve` app_msg writes into the channel.
func conflictHandler(c *sdk.Conn) func(bulkops.ConflictInfo) bulkops.ConflictAction {
	return func(info bulkops.ConflictInfo) bulkops.ConflictAction {
		ch := make(chan bulkops.ConflictAction, 1)
		pendingConflicts.Lock()
		pendingConflicts.m[info.Job.ID] = ch
		pendingConflicts.Unlock()
		defer func() {
			pendingConflicts.Lock()
			delete(pendingConflicts.m, info.Job.ID)
			pendingConflicts.Unlock()
		}()
		_ = c.SendAppMsg(map[string]any{
			"kind":     "job.conflict",
			"job_id":   info.Job.ID,
			"src":      info.Src,
			"src_type": info.SrcType,
			"dst":      info.Dst,
			"dst_type": info.DstType,
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
