package bulk

import "sync"

// externalStore holds jobs that wash-bulk does NOT execute itself —
// they're driven by another app (today: fm's uploads) which streams
// the bytes and reports progress via the job_report message. We mirror
// them into the published State so they render in the sidebar
// BulkWidget alongside the worker-driven move/copy/delete jobs.
//
// Why bulk doesn't run them: an upload's bytes originate in the
// browser, so the single bulk worker goroutine can't perform it
// without blocking the whole queue waiting on FE chunks. fm owns the
// confined fs and does the writing; bulk is purely the progress
// surface + the cancel relay point.
//
// Insertion order is preserved so progress updates (repeated upserts
// of the same job_id) don't reshuffle the rows. owner records the
// reporting app's instance id so a sidebar cancel can be relayed back.
type externalStore struct {
	mu    sync.Mutex
	order []string
	jobs  map[string]JobView
	owner map[string]string
}

func newExternalStore() *externalStore {
	return &externalStore{
		jobs:  map[string]JobView{},
		owner: map[string]string{},
	}
}

// upsert inserts or updates a job. First sight of a job_id appends it
// to the render order; later updates replace its view in place. A
// non-empty owner is recorded (progress updates may omit it).
func (e *externalStore) upsert(jv JobView, owner string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.jobs[jv.JobID]; !ok {
		e.order = append(e.order, jv.JobID)
	}
	e.jobs[jv.JobID] = jv
	if owner != "" {
		e.owner[jv.JobID] = owner
	}
}

// views returns the external jobs in insertion order.
func (e *externalStore) views() []JobView {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]JobView, 0, len(e.order))
	for _, id := range e.order {
		out = append(out, e.jobs[id])
	}
	return out
}

// ownerOf returns the reporting instance for a job id, if known.
func (e *externalStore) ownerOf(id string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	o, ok := e.owner[id]
	return o, ok
}
