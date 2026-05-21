// Package bulkops is the queue + worker library behind the
// wash-bulk app. It handles recursive delete / move / copy of
// arbitrary path sets with progress reporting and cancellation.
//
// Design notes
//
//   - One Queue per process, one worker goroutine (v1). Adding
//     parallel workers later is a small refactor; the queue keeps
//     a slice of pending jobs and the worker dequeues sequentially.
//   - Jobs are append-only — once added they live for the manager's
//     lifetime (so the FE can see the full history). Cancelled and
//     done jobs are tagged with status; clients filter / auto-clear.
//   - Progress is reported per-job as (Done, Total) item counts. We
//     count entries (files + dirs) rather than bytes — predictable
//     across mixed-content trees and survives moves (where renames
//     don't have a byte count).
//   - Operations are best-effort, not transactional. If a recursive
//     delete fails halfway through, the items already removed stay
//     removed — the job reports failed with the first error.
//
// Why a library, not the bulk-ops BE inline: makes unit testing
// straightforward. The BE is then a thin wire-and-progress shim.
package bulkops

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Op identifies the kind of work a job represents.
type Op string

const (
	OpDelete Op = "delete"
	OpMove   Op = "move"
	OpCopy   Op = "copy"
)

// Status is the lifecycle state of a single job.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// ConflictAction is what the user (via the BE's onConflict
// callback) chose to do about an overwrite. The *_All variants
// are sticky for the remainder of THIS job — subsequent conflicts
// in the same job apply the choice automatically.
type ConflictAction string

const (
	ConflictReplace    ConflictAction = "replace"
	ConflictReplaceAll ConflictAction = "replace_all"
	ConflictSkip       ConflictAction = "skip"
	ConflictSkipAll    ConflictAction = "skip_all"
	ConflictCancel     ConflictAction = "cancel"
)

// Job is one unit of work in the queue. Fields are mutated by the
// worker under m.mu; callers should not write to them directly.
type Job struct {
	ID     string
	Op     Op
	Paths  []string // source(s). For Move/Copy these are moved/copied INTO Dest.
	Dest   string   // destination dir, for Move/Copy. Empty for Delete.
	Status Status
	Done   int    // items processed so far
	Total  int    // items the job will process (computed at start)
	Error  string // populated on Failed
	cancel atomic.Bool
}

// snapshot returns a copy of the job safe to hand to callbacks
// without exposing the live struct.
func (j *Job) snapshot() Job {
	out := Job{
		ID:     j.ID,
		Op:     j.Op,
		Paths:  append([]string(nil), j.Paths...),
		Dest:   j.Dest,
		Status: j.Status,
		Done:   j.Done,
		Total:  j.Total,
		Error:  j.Error,
	}
	return out
}

// Cancel marks the job for cancellation. The worker checks the flag
// between items; an in-flight per-file operation is not interrupted
// mid-syscall, but the next iteration will see the flag and stop.
// Safe to call from any goroutine.
func (j *Job) Cancel() { j.cancel.Store(true) }

// Manager owns the queue + worker. Construct with New; tear down
// with Close (idempotent). All public methods are safe from
// multiple goroutines.
type Manager struct {
	mu      sync.Mutex
	jobs    []*Job
	pending []*Job // workers process from front to back
	closed  bool

	wakeup chan struct{}
	stop   chan struct{}
	done   chan struct{}

	// onUpdate is invoked under m.mu after any job state change.
	// The library doesn't dictate WHERE updates are delivered —
	// the BE wires this to its outbound EvtAppMsg push.
	onUpdate func(Job)

	// onConflict is consulted when a copy/move would overwrite an
	// existing path. The callback may block while it asks the user
	// (the BE round-trips through the FE). When nil, the library
	// defaults to ConflictSkip — safe for unit tests that don't
	// care about prompting.
	onConflict func(job Job, src, dst string) ConflictAction

	// itemDelay slows the worker by this much between items —
	// useful for demos and live testing of the progress UI.
	// Zero (the production default) disables the sleep entirely
	// and keeps the worker tight.
	itemDelay time.Duration

	nextID atomic.Uint64
}

// Option configures a Manager.
type Option func(*Manager)

// WithOnUpdate sets the callback for state changes.
func WithOnUpdate(fn func(Job)) Option {
	return func(m *Manager) { m.onUpdate = fn }
}

// WithItemDelay slows the worker by d between items. Production
// callers leave this at zero; demos / manual UI testing of the
// progress bar set a sub-second value via env (see wash-bulk's
// WASH_BULKOPS_ITEM_DELAY_MS).
func WithItemDelay(d time.Duration) Option {
	return func(m *Manager) { m.itemDelay = d }
}

// WithOnConflict sets the callback consulted on overwrites in
// copy / move. The callback may block. Used by wash-bulk's BE to
// ferry the prompt to the FE and wait for the user's pick.
func WithOnConflict(fn func(job Job, src, dst string) ConflictAction) Option {
	return func(m *Manager) { m.onConflict = fn }
}

// New constructs a Manager and starts its worker goroutine. Close
// it when the owning process is exiting.
func New(opts ...Option) *Manager {
	m := &Manager{
		wakeup: make(chan struct{}, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	for _, opt := range opts {
		opt(m)
	}
	go m.run()
	return m
}

// Close stops the worker goroutine, waits for it to drain any
// in-flight work, and returns. Idempotent.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	close(m.stop)
	<-m.done
	return nil
}

// Enqueue adds a job to the queue and returns the assigned id.
// Validation is minimal — we trust the caller; errors surface as
// the job's Failed status.
func (m *Manager) Enqueue(op Op, paths []string, dest string) string {
	id := "j-" + strconv.FormatUint(m.nextID.Add(1), 10)
	job := &Job{
		ID:     id,
		Op:     op,
		Paths:  append([]string(nil), paths...),
		Dest:   dest,
		Status: StatusQueued,
	}
	m.mu.Lock()
	m.jobs = append(m.jobs, job)
	m.pending = append(m.pending, job)
	snap := job.snapshot()
	m.mu.Unlock()
	m.emit(snap)
	select {
	case m.wakeup <- struct{}{}:
	default:
	}
	return id
}

// Cancel marks job id for cancellation. Returns true if found.
// A still-queued job that is cancelled jumps straight to Cancelled
// without ever entering Running.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	var found *Job
	for _, j := range m.jobs {
		if j.ID == id {
			found = j
			break
		}
	}
	m.mu.Unlock()
	if found == nil {
		return false
	}
	found.Cancel()
	return true
}

// Jobs returns a snapshot of every job in insertion order. Used by
// the FE for initial-state queries.
func (m *Manager) Jobs() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j.snapshot())
	}
	return out
}

// run is the worker goroutine. Drains the pending queue, one job
// at a time, until Close.
func (m *Manager) run() {
	defer close(m.done)
	for {
		// Pull the next job (FIFO).
		m.mu.Lock()
		var job *Job
		if len(m.pending) > 0 {
			job = m.pending[0]
			m.pending = m.pending[1:]
		}
		m.mu.Unlock()
		if job == nil {
			select {
			case <-m.stop:
				return
			case <-m.wakeup:
				continue
			}
		}
		m.runJob(job)
		// Yield to wakeup signals so a freshly-enqueued job is
		// picked up promptly.
		select {
		case <-m.stop:
			return
		default:
		}
	}
}

// runJob executes one job synchronously. Updates status + emits at
// every meaningful state transition.
func (m *Manager) runJob(job *Job) {
	if job.cancel.Load() {
		m.transition(job, StatusCancelled, "")
		return
	}
	m.transition(job, StatusRunning, "")
	var err error
	switch job.Op {
	case OpDelete:
		err = m.runDelete(job)
	case OpMove:
		err = m.runMove(job)
	case OpCopy:
		err = m.runCopy(job)
	default:
		err = fmt.Errorf("unknown op %q", job.Op)
	}
	if job.cancel.Load() {
		m.transition(job, StatusCancelled, "")
		return
	}
	if err != nil {
		m.transition(job, StatusFailed, err.Error())
		return
	}
	m.transition(job, StatusDone, "")
}

// transition updates job state under the lock and fires onUpdate.
func (m *Manager) transition(job *Job, st Status, errMsg string) {
	m.mu.Lock()
	job.Status = st
	if errMsg != "" {
		job.Error = errMsg
	}
	snap := job.snapshot()
	m.mu.Unlock()
	m.emit(snap)
}

// bumpDone increments Done by n and fires onUpdate. Used by ops
// during their walks. When itemDelay is non-zero the worker
// sleeps AFTER the update so the FE shows the new progress before
// the worker moves on — that's what makes the progress bar feel
// responsive in demos.
func (m *Manager) bumpDone(job *Job, n int) {
	m.mu.Lock()
	job.Done += n
	snap := job.snapshot()
	delay := m.itemDelay
	m.mu.Unlock()
	m.emit(snap)
	if delay > 0 {
		time.Sleep(delay)
	}
}

// setTotal records the computed total + fires onUpdate.
func (m *Manager) setTotal(job *Job, total int) {
	m.mu.Lock()
	job.Total = total
	snap := job.snapshot()
	m.mu.Unlock()
	m.emit(snap)
}

func (m *Manager) emit(j Job) {
	if m.onUpdate != nil {
		m.onUpdate(j)
	}
}

// askConflict consults the registered conflict callback (or
// defaults to Skip). While the callback is running we poll the
// job's cancel flag so a user-cancel during the prompt unblocks
// the worker — the abandoned onConflict goroutine eventually
// returns into a buffered channel and is GC'd.
func (m *Manager) askConflict(job *Job, src, dst string) ConflictAction {
	if m.onConflict == nil {
		return ConflictSkip
	}
	resultCh := make(chan ConflictAction, 1)
	go func() {
		resultCh <- m.onConflict(job.snapshot(), src, dst)
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case r := <-resultCh:
			return r
		case <-ticker.C:
			if job.cancel.Load() {
				return ConflictCancel
			}
		}
	}
}

// ---- ops -----------------------------------------------------

// runDelete handles bulk delete. Each top-level path in Paths is
// walked depth-first, files removed bottom-up so dirs are empty by
// the time we Remove them. Cancellation is checked between items.
func (m *Manager) runDelete(job *Job) error {
	total, err := countItems(job.Paths)
	if err != nil {
		return err
	}
	m.setTotal(job, total)
	for _, p := range job.Paths {
		if err := m.deleteOne(job, p); err != nil {
			return err
		}
		if job.cancel.Load() {
			return nil
		}
	}
	return nil
}

// deleteOne removes path. If it's a dir, walks it bottom-up first
// (so each Remove is on an empty dir). Counts each entry as it goes.
func (m *Manager) deleteOne(job *Job, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Already gone — treat as a no-op, count it so progress
			// stays consistent with the pre-walk total.
			m.bumpDone(job, 1)
			return nil
		}
		return err
	}
	if !info.IsDir() {
		if err := os.Remove(path); err != nil {
			return err
		}
		m.bumpDone(job, 1)
		return nil
	}
	// Recurse children, then remove the dir itself.
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if job.cancel.Load() {
			return nil
		}
		if err := m.deleteOne(job, filepath.Join(path, e.Name())); err != nil {
			return err
		}
	}
	if job.cancel.Load() {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	m.bumpDone(job, 1)
	return nil
}

// runMove handles bulk move. Each path is renamed into Dest with
// its existing basename. On overwrite collision the conflict
// callback is consulted (Replace removes dst first; Skip moves on).
// Cross-device renames degrade to copy+delete — Go's os.Rename
// returns syscall.EXDEV in that case; we detect the error string
// and fall back.
func (m *Manager) runMove(job *Job) error {
	if job.Dest == "" {
		return errors.New("move requires dest")
	}
	m.setTotal(job, len(job.Paths))
	stickyReplace, stickySkip := false, false
	for _, src := range job.Paths {
		if job.cancel.Load() {
			return nil
		}
		dst := filepath.Join(job.Dest, filepath.Base(src))
		if _, err := os.Lstat(dst); err == nil {
			act := resolveConflict(m, job, src, dst, &stickyReplace, &stickySkip)
			switch act {
			case ConflictCancel:
				job.Cancel()
				return nil
			case ConflictSkip:
				m.bumpDone(job, 1) // count it so the bar fills
				continue
			case ConflictReplace:
				if err := os.RemoveAll(dst); err != nil {
					return err
				}
			}
		}
		if err := os.Rename(src, dst); err != nil {
			if isCrossDevice(err) {
				if cerr := copyTree(src, dst, nil); cerr != nil {
					return cerr
				}
				if rerr := removeAll(src, nil); rerr != nil {
					return rerr
				}
			} else {
				return err
			}
		}
		m.bumpDone(job, 1)
	}
	return nil
}

// runCopy handles bulk copy. Each path is copied recursively into
// Dest with its existing basename. On overwrite collision the
// conflict callback is consulted (Replace removes the dst subtree
// first; Skip moves on; Cancel aborts the job). The conflict is
// checked at the TOP level of each src — descendants below a
// "replace" copy don't re-prompt.
func (m *Manager) runCopy(job *Job) error {
	if job.Dest == "" {
		return errors.New("copy requires dest")
	}
	total, err := countItems(job.Paths)
	if err != nil {
		return err
	}
	m.setTotal(job, total)
	stickyReplace, stickySkip := false, false
	for _, src := range job.Paths {
		if job.cancel.Load() {
			return nil
		}
		dst := filepath.Join(job.Dest, filepath.Base(src))
		if _, err := os.Lstat(dst); err == nil {
			act := resolveConflict(m, job, src, dst, &stickyReplace, &stickySkip)
			switch act {
			case ConflictCancel:
				job.Cancel()
				return nil
			case ConflictSkip:
				// Bump by the subtree's pre-walked count so the
				// progress bar stays consistent with Total.
				n, _ := countOne(src)
				m.bumpDone(job, n)
				continue
			case ConflictReplace:
				if err := os.RemoveAll(dst); err != nil {
					return err
				}
			}
		}
		if err := copyTree(src, dst, func() { m.bumpDone(job, 1) }); err != nil {
			return err
		}
	}
	return nil
}

// resolveConflict centralizes the conflict-resolution logic shared
// by runCopy and runMove. The two booleans are sticky-flags per
// job — Replace-All / Skip-All flip them and subsequent conflicts
// reuse the prior choice without re-prompting.
func resolveConflict(m *Manager, job *Job, src, dst string, stickyReplace, stickySkip *bool) ConflictAction {
	if *stickyReplace {
		return ConflictReplace
	}
	if *stickySkip {
		return ConflictSkip
	}
	switch a := m.askConflict(job, src, dst); a {
	case ConflictReplaceAll:
		*stickyReplace = true
		return ConflictReplace
	case ConflictSkipAll:
		*stickySkip = true
		return ConflictSkip
	default:
		return a
	}
}

// ---- helpers -------------------------------------------------

// countItems walks paths and counts every entry (files + dirs +
// symlinks). Used to compute the job Total up front so the FE can
// render a stable progress bar.
func countItems(paths []string) (int, error) {
	var total int
	for _, p := range paths {
		n, err := countOne(p)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

func countOne(p string) (int, error) {
	info, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 1, nil // count it; the op will no-op
		}
		return 0, err
	}
	if !info.IsDir() {
		return 1, nil
	}
	count := 1 // the dir itself
	entries, err := os.ReadDir(p)
	if err != nil {
		return 0, err
	}
	// Sort for determinism in tests.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		n, err := countOne(filepath.Join(p, e.Name()))
		if err != nil {
			return 0, err
		}
		count += n
	}
	return count, nil
}

// copyTree copies src to dst recursively. dst's parent must exist;
// dst itself must NOT (caller checks). onItem is invoked once per
// entry copied (file, dir, or symlink) — used for progress.
func copyTree(src, dst string, onItem func()) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, dst); err != nil {
			return err
		}
	case info.IsDir():
		if err := os.Mkdir(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		if onItem != nil {
			onItem()
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), onItem); err != nil {
				return err
			}
		}
		return nil
	default:
		if err := copyFile(src, dst, info.Mode().Perm()); err != nil {
			return err
		}
	}
	if onItem != nil {
		onItem()
	}
	return nil
}

// copyFile copies a regular file's contents. Atomic-ish: writes to
// a sibling temp file, then renames into place. Preserves mode.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".bulkops.tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// removeAll deletes path bottom-up, counting items via onItem.
// Mirror of os.RemoveAll with the progress callback.
func removeAll(path string, onItem func()) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := removeAll(filepath.Join(path, e.Name()), onItem); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if onItem != nil {
		onItem()
	}
	return nil
}

// isCrossDevice reports whether err is an EXDEV-equivalent. We
// inspect the message because syscall.EXDEV is platform-specific
// and the wash binary is CGO_ENABLED=0 / cross-compiled.
func isCrossDevice(err error) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "invalid cross-device link") || contains(err.Error(), "EXDEV")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
