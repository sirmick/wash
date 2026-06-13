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

// Truncated reports whether the buffer has wrapped — i.e. the oldest
// bytes have been overwritten and a Snapshot no longer starts at the
// true beginning of the stream.
func (r *ringBuffer) Truncated() bool { return r.full }

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
