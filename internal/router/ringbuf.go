package router

import "sync"

// ringBuffer is a fixed-capacity FIFO of bytes. Once full, new writes
// overwrite the oldest bytes; writes never block and never fail. It
// backs two unrelated consumers in this package:
//
//   - PTY scrollback on raw channels (Snapshot/Len/Truncated), accessed
//     under the channel binding's shellMu but also taking the internal
//     mutex (a redundant, uncontended lock on that path);
//   - per-spawn stdout+stderr crash-log tails (Write/String), wired
//     through io.MultiWriter from two concurrent pipes, which is why
//     Write satisfies io.Writer and the type is goroutine-safe.
//
// Write always reports len(p) consumed so io.MultiWriter never raises
// ErrShortWrite once the buffer fills.
type ringBuffer struct {
	mu   sync.Mutex
	data []byte
	head int  // index of the next byte to write
	full bool // wraparound flag — distinguishes empty from full when head==tail
}

func newRingBuffer(capacity int) *ringBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &ringBuffer{data: make([]byte, capacity)}
}

// Write appends p to the buffer, overwriting old bytes if it would
// otherwise exceed capacity. It always succeeds and reports len(p)
// consumed (satisfying io.Writer), so it never trips io.MultiWriter's
// ErrShortWrite check.
func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := len(p)
	cap := len(r.data)
	if len(p) >= cap {
		// p alone fills (or overfills) the buffer: copy only its tail.
		copy(r.data, p[len(p)-cap:])
		r.head = 0
		r.full = true
		return total, nil
	}
	first := cap - r.head
	if first > len(p) {
		first = len(p)
	}
	copy(r.data[r.head:], p[:first])
	r.head += first
	if r.head >= cap {
		r.head = 0
		r.full = true
	}
	if rem := len(p) - first; rem > 0 {
		copy(r.data, p[first:])
		r.head = rem
		r.full = true
	}
	return total, nil
}

// GrowFor raises capacity so that n more bytes fit without overwriting
// history, doubling until they do, and never past max. It is the
// "nobody is reading this right now" path: a detached (or wedged-behind)
// channel keeps more history than the steady-state buffer would, because
// the bytes have nowhere else to go and the process must not be stalled
// to preserve them (docs/PTY_ROBUST.md — writes never block).
//
// Returns true if the capacity changed. Contents are preserved in order.
func (r *ringBuffer) GrowFor(n, max int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	capacity := len(r.data)
	if capacity >= max {
		return false
	}
	used := r.head
	if r.full {
		used = capacity
	}
	need := used + n
	if need <= capacity {
		return false
	}
	next := capacity
	for next < need {
		next *= 2
	}
	if next > max {
		next = max
	}
	if next <= capacity {
		return false
	}
	kept := r.snapshotLocked()
	r.data = make([]byte, next)
	r.head = copy(r.data, kept)
	r.full = r.head >= next
	if r.full {
		r.head = 0
	}
	return true
}

// Shrink returns capacity to target, keeping the most recent bytes —
// called once a shell has taken delivery of the history (reattach replay
// or resync), so the grown buffer doesn't outlive the disconnection that
// justified it. A no-op when already at or below target.
func (r *ringBuffer) Shrink(target int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if target < 1 {
		target = 1
	}
	if len(r.data) <= target {
		return false
	}
	kept := r.snapshotLocked()
	if len(kept) > target {
		kept = kept[len(kept)-target:]
	}
	r.data = make([]byte, target)
	r.head = copy(r.data, kept)
	r.full = r.head >= target
	if r.full {
		r.head = 0
	}
	return true
}

// Cap reports the current capacity.
func (r *ringBuffer) Cap() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.data)
}

// Snapshot returns the current contents in chronological order. Caller
// owns the returned slice.
func (r *ringBuffer) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

func (r *ringBuffer) snapshotLocked() []byte {
	if !r.full {
		out := make([]byte, r.head)
		copy(out, r.data[:r.head])
		return out
	}
	cap := len(r.data)
	out := make([]byte, cap)
	n := copy(out, r.data[r.head:])
	copy(out[n:], r.data[:r.head])
	return out
}

// String returns the buffered bytes in arrival order, for log tails.
func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.snapshotLocked())
}

// Len reports the number of bytes currently buffered.
func (r *ringBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return len(r.data)
	}
	return r.head
}

// Truncated reports whether the buffer has wrapped — i.e. the oldest
// bytes have been overwritten and a Snapshot no longer starts at the
// true beginning of the stream.
func (r *ringBuffer) Truncated() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.full
}

// realignReplay trims the head of a TRUNCATED scrollback snapshot so
// the replay does not start mid-UTF-8 rune or mid-escape-sequence.
// A wrapped ring buffer cuts the stream at an arbitrary byte; feeding
// xterm a torn CSI tail (e.g. `8;2;120m`) or a dangling continuation
// byte renders as garbage. Only called after data loss has already
// happened, so dropping a few more leading bytes is strictly an
// improvement; never call it on an un-wrapped buffer.
//
// Two cheap repairs, no full VT parser:
//   - drop leading UTF-8 continuation bytes (0x80–0xBF)
//   - if the head reads as the tail of a torn CSI — parameter bytes
//     (digits ; : ?) immediately followed by a final byte — drop
//     through the final byte. Bounded scan; plain text bails out at
//     the first non-parameter, non-final character.
//
// Torn OSC tails (title strings ending in BEL/ST) are not detectable
// without real parsing and are left alone.
func realignReplay(b []byte) []byte {
	i := 0
	for i < len(b) && i < 3 && b[i]&0xC0 == 0x80 {
		i++
	}
	b = b[i:]
	const maxScan = 16
	for j := 0; j < len(b) && j < maxScan; j++ {
		c := b[j]
		if (c >= '0' && c <= '9') || c == ';' || c == ':' || c == '?' {
			continue
		}
		if j > 0 && c >= 0x40 && c <= 0x7E {
			return b[j+1:]
		}
		break
	}
	return b
}
