// Motion compression for session patches.
//
// A drag emits a geometry patch per pointer move. When the writer is
// keeping up, each one goes out immediately and this file does nothing.
// When it is not — a bundle downloading, a slow link — the queue fills
// with successive positions of the same window, every one of them
// already stale by the time it reaches the browser. The window then
// crawls through a backlog of where the pointer used to be, which is
// what "jerky" looks like from the far side.
//
// So a patch supersedes the pending patch for the same target instead of
// queueing behind it: X11 called this motion compression, and it is only
// safe because a window upsert carries the COMPLETE SessionWindow rather
// than a delta (wmstate.go builds each from a fresh copy). Last writer
// wins is therefore lossless — the intermediate positions carry no
// information the final one lacks.
//
// Delete shares the window's key space deliberately. A pending upsert
// followed by a delete must not resurrect the window, and keying both on
// the window id makes the delete replace the upsert rather than race it.

package router

import (
	"strconv"
	"sync"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

// patchRetryDelay is how long to wait before retrying a flush that the
// scheduler refused. Short enough that the last patch of a drag lands
// without a visible settle, long enough that a wedged queue is not spun
// on. Only ever armed while the Interactive queue is full.
const patchRetryDelay = 8 * time.Millisecond

// patchCoalescer holds at most one pending patch per target for one
// shell. Safe for concurrent producers: every router path that mutates
// window state can call add.
type patchCoalescer struct {
	mu      sync.Mutex
	keys    []string // insertion order, so a flush is deterministic
	pending map[string]wire.SessionPatch
	timer   *time.Timer
	// flush is the actual send. Returns false when the scheduler refused
	// (queue full), which is the signal to keep the batch pending.
	flush func([]wire.SessionPatch) bool
}

func newPatchCoalescer(flush func([]wire.SessionPatch) bool) *patchCoalescer {
	return &patchCoalescer{pending: map[string]wire.SessionPatch{}, flush: flush}
}

// patchKey is the identity a patch collapses on. Window upserts and
// deletes share one key space so the later of the two wins; app state is
// keyed per instance; anything else gets a unique key and never merges,
// which is the safe default for an op this file has not been taught.
func patchKey(p wire.SessionPatch, seq int) string {
	switch p.Op {
	case wire.SessionPatchWindowUpsert:
		if p.Window != nil {
			return "w:" + strconv.FormatUint(uint64(p.Window.WindowID), 10)
		}
	case wire.SessionPatchWindowDelete:
		return "w:" + strconv.FormatUint(uint64(p.WindowID), 10)
	case wire.SessionPatchAppState:
		if p.InstanceID != "" {
			return "a:" + p.InstanceID
		}
	}
	return "?:" + strconv.Itoa(seq)
}

// add merges patches into the pending set and tries to send. Sending is
// attempted synchronously, so an uncongested link behaves exactly as it
// did before this existed: one batch in, one batch straight out.
func (pc *patchCoalescer) add(patches []wire.SessionPatch) {
	if len(patches) == 0 {
		return
	}
	pc.mu.Lock()
	for i, p := range patches {
		k := patchKey(p, len(pc.keys)+i)
		if _, seen := pc.pending[k]; !seen {
			pc.keys = append(pc.keys, k)
		}
		pc.pending[k] = p
	}
	pc.mu.Unlock()
	pc.tryFlush()
}

// tryFlush sends the pending batch if the scheduler will take it. On
// refusal the batch stays pending — further updates keep collapsing into
// it — and a retry is armed so the tail of a drag is never stranded.
func (pc *patchCoalescer) tryFlush() {
	pc.mu.Lock()
	if len(pc.keys) == 0 {
		pc.mu.Unlock()
		return
	}
	batch := make([]wire.SessionPatch, 0, len(pc.keys))
	for _, k := range pc.keys {
		batch = append(batch, pc.pending[k])
	}
	keys, pending := pc.keys, pc.pending
	pc.keys, pc.pending = nil, map[string]wire.SessionPatch{}
	pc.mu.Unlock()

	if pc.flush(batch) {
		pc.cancelRetry()
		return
	}

	// Refused. Put the batch back UNDER anything that arrived while we
	// were sending — those are newer and must win.
	pc.mu.Lock()
	for i := len(keys) - 1; i >= 0; i-- {
		k := keys[i]
		if _, newer := pc.pending[k]; newer {
			continue
		}
		pc.pending[k] = pending[k]
		pc.keys = append([]string{k}, pc.keys...)
	}
	if pc.timer == nil {
		pc.timer = time.AfterFunc(patchRetryDelay, pc.tryFlush)
	} else {
		pc.timer.Reset(patchRetryDelay)
	}
	pc.mu.Unlock()
}

func (pc *patchCoalescer) cancelRetry() {
	pc.mu.Lock()
	if pc.timer != nil {
		pc.timer.Stop()
		pc.timer = nil
	}
	pc.mu.Unlock()
}

// stop releases the retry timer on shell teardown.
func (pc *patchCoalescer) stop() { pc.cancelRetry() }
