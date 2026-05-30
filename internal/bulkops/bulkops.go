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
	"syscall"
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
// in the same job apply the choice automatically. Merge applies
// only when both src and dst are directories — it descends into
// the source dir and resolves per-child conflicts; Replace on a
// dir-vs-dir collision destroys the dst dir wholesale before
// copying the source. The FE decides which buttons to show based
// on src_type / dst_type in the conflict event.
type ConflictAction string

const (
	ConflictReplace    ConflictAction = "replace"
	ConflictReplaceAll ConflictAction = "replace_all"
	ConflictMerge      ConflictAction = "merge"
	ConflictMergeAll   ConflictAction = "merge_all"
	ConflictSkip       ConflictAction = "skip"
	ConflictSkipAll    ConflictAction = "skip_all"
	ConflictCancel     ConflictAction = "cancel"
)

// ConflictInfo is the bag of context passed to the onConflict
// callback. Includes the entry types on both sides so the FE can
// choose between "Merge?" and "Replace?" wording and the right
// button set.
type ConflictInfo struct {
	Job     Job
	Src     string
	SrcType string // "file" | "dir" | "symlink"
	Dst     string
	DstType string
}

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
	// cancel is a pointer so Job stays a safe value type — snapshots
	// and onUpdate copies share the live flag rather than copying a
	// sync/atomic value (which go vet flags and would silently zero).
	cancel *atomic.Bool
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
		cancel: j.cancel,
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
	onConflict func(info ConflictInfo) ConflictAction

	nextID atomic.Uint64
}

// Option configures a Manager.
type Option func(*Manager)

// WithOnUpdate sets the callback for state changes.
func WithOnUpdate(fn func(Job)) Option {
	return func(m *Manager) { m.onUpdate = fn }
}

// WithOnConflict sets the callback consulted on overwrites in
// copy / move. The callback receives src/dst paths + their entry
// types so it can render the right prompt (Merge for dir-vs-dir,
// Replace for file-vs-file, destructive prompt for type
// mismatch). May block; used by wash-bulk's BE to ferry the
// prompt to the FE and wait for the user's pick.
func WithOnConflict(fn func(info ConflictInfo) ConflictAction) Option {
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
		cancel: &atomic.Bool{},
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
// during their walks.
func (m *Manager) bumpDone(job *Job, n int) {
	m.mu.Lock()
	job.Done += n
	snap := job.snapshot()
	m.mu.Unlock()
	m.emit(snap)
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
func (m *Manager) askConflict(job *Job, src, dst string, srcInfo, dstInfo os.FileInfo) ConflictAction {
	if m.onConflict == nil {
		return ConflictSkip
	}
	info := ConflictInfo{
		Job:     job.snapshot(),
		Src:     src,
		SrcType: entryType(srcInfo),
		Dst:     dst,
		DstType: entryType(dstInfo),
	}
	resultCh := make(chan ConflictAction, 1)
	go func() {
		resultCh <- m.onConflict(info)
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

// entryType maps an os.FileInfo to the stable string we use in
// the wire (matches the fm BE's entry.Type).
func entryType(fi os.FileInfo) string {
	if fi == nil {
		return ""
	}
	mode := fi.Mode()
	if mode&os.ModeSymlink != 0 {
		return "symlink"
	}
	if fi.IsDir() {
		return "dir"
	}
	if mode.IsRegular() {
		return "file"
	}
	return "other"
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
// returns syscall.EXDEV in that case, which isCrossDevice matches
// with errors.Is.
func (m *Manager) runMove(job *Job) error {
	if job.Dest == "" {
		return errors.New("move requires dest")
	}
	// Recursive merge can visit more entries than len(Paths), so
	// pre-walk to get an accurate progress total — matches the
	// copy semantics. Each visited source entry counts as 1.
	total, err := countItems(job.Paths)
	if err != nil {
		return err
	}
	m.setTotal(job, total)
	sticky := stickyConflict{}
	for _, src := range job.Paths {
		if job.cancel.Load() {
			return nil
		}
		dst := filepath.Join(job.Dest, filepath.Base(src))
		if err := m.moveOne(job, src, dst, &sticky); err != nil {
			return err
		}
	}
	return nil
}

// runCopy handles bulk copy. Each path is copied recursively into
// Dest with its existing basename. On overwrite collision the
// conflict callback is consulted with the dst's entry type so the
// FE can show "Merge?" (dir-vs-dir) vs "Replace?" (file-vs-file)
// vs the destructive type-mismatch prompt. Merge recurses into
// the source dir; Replace destroys dst wholesale; Skip leaves it.
func (m *Manager) runCopy(job *Job) error {
	if job.Dest == "" {
		return errors.New("copy requires dest")
	}
	total, err := countItems(job.Paths)
	if err != nil {
		return err
	}
	m.setTotal(job, total)
	sticky := stickyConflict{}
	for _, src := range job.Paths {
		if job.cancel.Load() {
			return nil
		}
		dst := filepath.Join(job.Dest, filepath.Base(src))
		if err := m.copyOne(job, src, dst, &sticky); err != nil {
			return err
		}
	}
	return nil
}

// stickyConflict carries the *_All decisions for the rest of the
// job. They're per-action: a "Merge All" doesn't preset
// "Replace All" for unrelated file conflicts later in the job;
// "Skip All" skips everything regardless of type.
type stickyConflict struct {
	replaceAll bool
	mergeAll   bool
	skipAll    bool
}

// copyOne processes a single (src, dst) pair. The caller has
// already filtered for cancel. If dst exists we ask the conflict
// callback; if both sides are dirs and the user picks Merge,
// we recurse into the source's children and resolve per-child.
func (m *Manager) copyOne(job *Job, src, dst string, sticky *stickyConflict) error {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	dstInfo, dstErr := os.Lstat(dst)
	if dstErr != nil {
		if !errors.Is(dstErr, os.ErrNotExist) {
			return dstErr
		}
		// Clean copy — no conflict.
		return copyTree(src, dst, func() { m.bumpDone(job, 1) })
	}
	switch action := decideConflict(m, job, src, srcInfo, dst, dstInfo, sticky); action {
	case ConflictCancel:
		job.Cancel()
		return nil
	case ConflictSkip:
		n, _ := countOne(src)
		m.bumpDone(job, n)
		return nil
	case ConflictReplace:
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		return copyTree(src, dst, func() { m.bumpDone(job, 1) })
	case ConflictMerge:
		// Dir-vs-dir merge. Count the source dir itself, then
		// recurse into its children with the same sticky state.
		m.bumpDone(job, 1)
		return m.mergeCopyChildren(job, src, dst, sticky)
	}
	return nil
}

// moveOne mirrors copyOne for the move op. Same conflict shape;
// merge recurses, replace removes dst then renames, skip counts
// and continues. After a successful merge, the (now-empty) source
// dir is removed.
func (m *Manager) moveOne(job *Job, src, dst string, sticky *stickyConflict) error {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	dstInfo, dstErr := os.Lstat(dst)
	if dstErr != nil {
		if !errors.Is(dstErr, os.ErrNotExist) {
			return dstErr
		}
		return m.renameOrFallback(job, src, dst)
	}
	switch action := decideConflict(m, job, src, srcInfo, dst, dstInfo, sticky); action {
	case ConflictCancel:
		job.Cancel()
		return nil
	case ConflictSkip:
		n, _ := countOne(src)
		m.bumpDone(job, n)
		return nil
	case ConflictReplace:
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		return m.renameOrFallback(job, src, dst)
	case ConflictMerge:
		// Recurse into source children, then remove the (now-
		// empty) source dir. We DON'T bump for the source dir
		// yet — we bump after the empty-remove succeeds, so a
		// failed merge doesn't double-count.
		if err := m.mergeMoveChildren(job, src, dst, sticky); err != nil {
			return err
		}
		if err := os.Remove(src); err != nil {
			// Source dir wasn't empty after merging — could happen
			// if a child move was skipped. Leaving it is the right
			// outcome; just don't bump (we already counted via the
			// children).
			_ = err
		}
		m.bumpDone(job, 1)
		return nil
	}
	return nil
}

// renameOrFallback wraps os.Rename with the cross-device fallback
// (copy+remove). Bumps progress by 1 for the source entry. For a
// directory, copyTree under the fallback walks the source tree
// and the per-file callback bumps for each — that matches the
// progress total we computed at job start.
func (m *Manager) renameOrFallback(job *Job, src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		if isCrossDevice(err) {
			// The src might be a tree; copy+delete preserves the
			// per-entry progress contract via the callback.
			if cerr := copyTree(src, dst, func() { m.bumpDone(job, 1) }); cerr != nil {
				return cerr
			}
			if rerr := removeAll(src, nil); rerr != nil {
				return rerr
			}
			return nil
		}
		return err
	}
	// Same-device rename: count the source as one bump. For a
	// dir source we don't recurse — moving a dir on the same fs
	// is a single syscall regardless of how many entries are
	// inside; the FE only sees the dir entry move. The progress
	// total was computed from countItems which DOES walk the
	// tree; same-device rename will undercount Done in that
	// case. The visual effect is the bar finishing early — which
	// is fine; it's the only signal that conflicts with progress.
	m.bumpDone(job, 1)
	return nil
}

// mergeCopyChildren copies each entry inside srcDir into dstDir,
// asking the conflict callback for each collision. Recursive via
// copyOne.
func (m *Manager) mergeCopyChildren(job *Job, srcDir, dstDir string, sticky *stickyConflict) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if job.cancel.Load() {
			return nil
		}
		childSrc := filepath.Join(srcDir, e.Name())
		childDst := filepath.Join(dstDir, e.Name())
		if err := m.copyOne(job, childSrc, childDst, sticky); err != nil {
			return err
		}
	}
	return nil
}

// mergeMoveChildren moves each entry inside srcDir into dstDir,
// asking the conflict callback for each collision.
func (m *Manager) mergeMoveChildren(job *Job, srcDir, dstDir string, sticky *stickyConflict) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if job.cancel.Load() {
			return nil
		}
		childSrc := filepath.Join(srcDir, e.Name())
		childDst := filepath.Join(dstDir, e.Name())
		if err := m.moveOne(job, childSrc, childDst, sticky); err != nil {
			return err
		}
	}
	return nil
}

// decideConflict picks an action for a (src, dst) collision,
// consulting the sticky-All flags first and the user prompt
// otherwise. The *_All variants returned by the prompt flip the
// flags and reduce to the basic action.
//
// Sticky semantics:
//   - replaceAll applies only to file-vs-file collisions (and to
//     type mismatches, where it behaves as a destructive Replace).
//   - mergeAll applies only to dir-vs-dir collisions.
//   - skipAll skips any collision regardless of type.
//
// This split matches Windows Explorer: "Replace All" doesn't
// silently nuke dirs the user hadn't explicitly opted to replace,
// and "Merge All" doesn't try to merge file-vs-file pairs.
func decideConflict(m *Manager, job *Job, src string, srcInfo os.FileInfo, dst string, dstInfo os.FileInfo, sticky *stickyConflict) ConflictAction {
	srcDir := srcInfo != nil && srcInfo.IsDir()
	dstDir := dstInfo != nil && dstInfo.IsDir()
	bothDirs := srcDir && dstDir
	if sticky.skipAll {
		return ConflictSkip
	}
	if bothDirs && sticky.mergeAll {
		return ConflictMerge
	}
	if !bothDirs && sticky.replaceAll {
		return ConflictReplace
	}
	switch raw := m.askConflict(job, src, dst, srcInfo, dstInfo); raw {
	case ConflictReplaceAll:
		sticky.replaceAll = true
		return ConflictReplace
	case ConflictMergeAll:
		sticky.mergeAll = true
		return ConflictMerge
	case ConflictSkipAll:
		sticky.skipAll = true
		return ConflictSkip
	default:
		return raw
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

// isCrossDevice reports whether err is an EXDEV — a rename across
// filesystems. os.Rename wraps the errno in *os.LinkError, which
// errors.Is unwraps.
func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}
