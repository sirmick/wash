package router

// ringBuffer is a fixed-capacity FIFO of bytes used for PTY scrollback
// on raw channels. Once full, new writes overwrite the oldest bytes.
//
// Not goroutine-safe — the channel binding's shellMu serializes access.
type ringBuffer struct {
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
// otherwise exceed capacity. Always succeeds.
func (r *ringBuffer) Write(p []byte) {
	cap := len(r.data)
	if len(p) >= cap {
		// p alone fills (or overfills) the buffer: copy only its tail.
		copy(r.data, p[len(p)-cap:])
		r.head = 0
		r.full = true
		return
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
}

// Snapshot returns the current contents in chronological order. Caller
// owns the returned slice.
func (r *ringBuffer) Snapshot() []byte {
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

// Len reports the number of bytes currently buffered.
func (r *ringBuffer) Len() int {
	if r.full {
		return len(r.data)
	}
	return r.head
}
