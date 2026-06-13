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
	}
	for _, c := range cases {
		if got := realignReplay([]byte(c.in)); string(got) != c.want {
			t.Errorf("%s: realignReplay(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
