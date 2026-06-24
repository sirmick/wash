package router

import (
	"bytes"
	"io"
	"testing"
)

// ringBuffer.Write must report len(p) consumed even after the buffer
// is full — it never rejects input, just drops the oldest bytes. A
// wrong count makes io.MultiWriter (how spawn wires stdout/stderr into
// the crash log) raise ErrShortWrite once the ring fills.
func TestRingBufWriteReportsFullConsumption(t *testing.T) {
	r := newRingBuffer(8)
	if n, err := r.Write([]byte("0123456789")); err != nil || n != 10 {
		t.Fatalf("oversized write: got (%d,%v), want (10,nil)", n, err)
	}
	if n, err := r.Write([]byte("ab")); err != nil || n != 2 {
		t.Fatalf("post-full write: got (%d,%v), want (2,nil)", n, err)
	}
	if got := r.String(); got != "456789ab" {
		t.Fatalf("tail: got %q, want %q", got, "456789ab")
	}
}

func TestRingBufThroughMultiWriter(t *testing.T) {
	var sink bytes.Buffer
	r := newRingBuffer(4)
	mw := io.MultiWriter(&sink, r)
	for _, s := range []string{"aa", "bb", "cc", "dd"} {
		if _, err := io.WriteString(mw, s); err != nil {
			t.Fatalf("MultiWriter write %q: %v", s, err)
		}
	}
	if got := r.String(); got != "ccdd" {
		t.Fatalf("ring tail: got %q, want %q", got, "ccdd")
	}
	if got := sink.String(); got != "aabbccdd" {
		t.Fatalf("passthrough sink: got %q, want %q", got, "aabbccdd")
	}
}
