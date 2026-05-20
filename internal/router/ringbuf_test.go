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
	r.Write([]byte("abcdefgh"))                // exactly full
	if got := r.Snapshot(); !bytes.Equal(got, []byte("abcdefgh")) {
		t.Fatalf("snapshot after full = %q", got)
	}
	r.Write([]byte("12"))                       // wraps
	if got := r.Snapshot(); !bytes.Equal(got, []byte("cdefgh12")) {
		t.Fatalf("snapshot after wrap = %q", got)
	}
}

func TestRingBufferOversize(t *testing.T) {
	r := newRingBuffer(4)
	r.Write([]byte("1234567890"))               // 10 bytes, cap 4
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
