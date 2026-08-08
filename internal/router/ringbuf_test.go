package router

import (
	"bytes"
	"testing"
)

func TestRingBufferShortWrite(t *testing.T) {
	r := newRingBuffer(16)
	r.Write([]byte("hello"))
	if got := r.Snapshot(); !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("snapshot = %q", got)
	}
	if got := r.Len(); got != 5 {
		t.Fatalf("len = %d", got)
	}
}

func TestRingBufferWrap(t *testing.T) {
	r := newRingBuffer(8)
	r.Write([]byte("abcdefgh")) // exactly full
	if got := r.Snapshot(); !bytes.Equal(got, []byte("abcdefgh")) {
		t.Fatalf("snapshot after full = %q", got)
	}
	r.Write([]byte("12")) // wraps
	if got := r.Snapshot(); !bytes.Equal(got, []byte("cdefgh12")) {
		t.Fatalf("snapshot after wrap = %q", got)
	}
}

func TestRingBufferOversize(t *testing.T) {
	r := newRingBuffer(4)
	r.Write([]byte("1234567890")) // 10 bytes, cap 4
	if got := r.Snapshot(); !bytes.Equal(got, []byte("7890")) {
		t.Fatalf("snapshot = %q", got)
	}
}

func TestRingBufferMultipleWrites(t *testing.T) {
	r := newRingBuffer(6)
	r.Write([]byte("abc"))
	r.Write([]byte("def"))
	r.Write([]byte("ghi"))
	if got := r.Snapshot(); !bytes.Equal(got, []byte("defghi")) {
		t.Fatalf("snapshot = %q", got)
	}
}

func TestRingBufferTruncated(t *testing.T) {
	r := newRingBuffer(4)
	r.Write([]byte("ab"))
	if r.Truncated() {
		t.Fatal("truncated before wrap")
	}
	r.Write([]byte("cde")) // wraps
	if !r.Truncated() {
		t.Fatal("not truncated after wrap")
	}
}

func TestRealignReplay(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Torn UTF-8: leading continuation bytes are dropped.
		{"utf8 tail", "\x9c\x93done", "done"},
		{"utf8 single cont", "\xa0hello", "hello"},
		// Torn CSI: parameter bytes then a final byte, all dropped.
		{"torn sgr", "8;2;120mhello", "hello"},
		{"torn cursor", "12;40Htext", "text"},
		// Plain text is left alone.
		{"plain", "hello world", "hello world"},
		{"leading digits then space", "123 items", "123 items"},
		{"empty", "", ""},
		// A final byte with no parameter prefix is indistinguishable
		// from text — left alone.
		{"bare letter", "Mhello", "Mhello"},
		// Torn UTF-8 directly followed by a torn CSI tail.
		{"utf8 then csi", "\x80\x800;31mred", "red"},
		// Parameter run longer than the scan bound: untouched.
		{"long digits", "12345678901234567890m", "12345678901234567890m"},
		// Newline within the bound: cut through it — heals tears the
		// heuristics can't see (torn OSC titles, a cut between ESC and
		// '[', a lone final byte).
		{"newline cut", "tail of torn line\nnext line", "next line"},
		{"torn osc", "user@host: ~/src\x07$ ls\nclean", "clean"},
		{"torn csi bracket head", "[38;2;120mtext\nok", "ok"},
		{"newline first byte", "\nafter", "after"},
	}
	for _, c := range cases {
		if got := realignReplay([]byte(c.in)); string(got) != c.want {
			t.Errorf("%s: realignReplay(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// A newline beyond the scan bound must NOT be cut to — alt-screen
// output that redraws with \r or cursor motion could push the first
// \n arbitrarily deep, and swallowing that much replay would trade a
// garbage line for missing content. The fallback heuristics apply.
func TestRealignReplayNewlineOutOfBound(t *testing.T) {
	in := append(bytes.Repeat([]byte{'x'}, realignScanBytes), []byte("\nrest")...)
	if got := realignReplay(in); !bytes.Equal(got, in) {
		t.Errorf("out-of-bound newline: got %d bytes, want untouched %d", len(got), len(in))
	}
}

// ---- dynamic capacity (detached channels keep more history) ----

func TestRingGrowForPreservesOrderAndCaps(t *testing.T) {
	r := newRingBuffer(8)
	r.Write([]byte("abcdefgh"))

	// Growing keeps what's there, in order.
	if !r.GrowFor(8, 64) {
		t.Fatal("GrowFor did not grow a full buffer")
	}
	if got := string(r.Snapshot()); got != "abcdefgh" {
		t.Errorf("after grow: %q, want abcdefgh", got)
	}
	if r.Cap() < 16 {
		t.Errorf("cap = %d, want at least 16", r.Cap())
	}
	// A grown buffer is no longer truncated: nothing was lost.
	if r.Truncated() {
		t.Error("grown buffer still reports truncated")
	}
	r.Write([]byte("ijklmnop"))
	if got := string(r.Snapshot()); got != "abcdefghijklmnop" {
		t.Errorf("after grow+write: %q", got)
	}

	// It never exceeds the ceiling, and at the ceiling it goes back to
	// overwriting rather than refusing writes.
	for i := 0; i < 20; i++ {
		r.GrowFor(64, 64)
		r.Write(make([]byte, 64))
	}
	if r.Cap() != 64 {
		t.Errorf("cap = %d, want the 64-byte ceiling", r.Cap())
	}
	if !r.Truncated() {
		t.Error("a buffer at its ceiling should overwrite (and report truncated)")
	}
	if got := r.Len(); got != 64 {
		t.Errorf("len = %d, want 64", got)
	}
}

func TestRingGrowForNoopWhenItFits(t *testing.T) {
	r := newRingBuffer(64)
	r.Write([]byte("hello"))
	if r.GrowFor(10, 1024) {
		t.Error("grew for bytes that already fit")
	}
	if r.Cap() != 64 {
		t.Errorf("cap = %d, want 64", r.Cap())
	}
	// Already at the ceiling: nothing to do, no panic.
	r2 := newRingBuffer(64)
	if r2.GrowFor(1000, 64) {
		t.Error("grew past the ceiling")
	}
}

func TestRingShrinkKeepsTheTail(t *testing.T) {
	r := newRingBuffer(4)
	r.GrowFor(64, 64)
	r.Write([]byte("0123456789abcdefghij"))

	if !r.Shrink(8) {
		t.Fatal("Shrink did not shrink")
	}
	if r.Cap() != 8 {
		t.Errorf("cap = %d, want 8", r.Cap())
	}
	// The RECENT bytes are what a terminal needs; the old head goes.
	if got := string(r.Snapshot()); got != "cdefghij" {
		t.Errorf("kept %q, want the last 8 bytes", got)
	}
	// Still a working ring afterwards.
	r.Write([]byte("KL"))
	if got := string(r.Snapshot()); got != "efghijKL" {
		t.Errorf("after post-shrink write: %q", got)
	}
	if r.Shrink(8) {
		t.Error("shrink to the current size reported a change")
	}
}

// The whole point: a channel nobody is reading keeps more than the
// steady-state buffer, and gives the memory back once someone does.
func TestRingGrowShrinkCycleMatchesTheChannelLifecycle(t *testing.T) {
	base, max := 1024, 16*1024
	r := newRingBuffer(base)
	chunk := make([]byte, 512)
	for i := range chunk {
		chunk[i] = byte('a' + i%26)
	}
	// Detached: 32 KiB written into a 1 KiB steady-state buffer.
	for i := 0; i < 64; i++ {
		r.GrowFor(len(chunk), max)
		r.Write(chunk)
	}
	if r.Cap() != max {
		t.Errorf("cap = %d, want the %d ceiling", r.Cap(), max)
	}
	if r.Len() != max {
		t.Errorf("len = %d, want a full %d buffer", r.Len(), max)
	}
	// …which is 16× what the old fixed buffer would have retained.
	if r.Len() <= base {
		t.Errorf("detached buffer kept %d bytes, no better than the base %d", r.Len(), base)
	}
	// Reattach: the shell takes delivery, the memory goes back.
	r.Shrink(base)
	if r.Cap() != base {
		t.Errorf("cap after replay = %d, want %d", r.Cap(), base)
	}
}
