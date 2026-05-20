package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		channel uint32
		payload []byte
	}{
		{"empty channel 0", 0, nil},
		{"empty channel 1", 1, nil},
		{"small channel 0", 0, []byte(`{"t":"identity","app_id":"a"}`)},
		{"max channel id", MaxChannel, []byte{0x01, 0x02, 0x03}},
		{"large 1 MiB", 7, bytes.Repeat([]byte{0xAB}, 1<<20)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := Frame{Flags: FlagEnd, Channel: tc.channel, Payload: tc.payload}
			var buf bytes.Buffer
			if err := EncodeFrame(&buf, in); err != nil {
				t.Fatalf("encode: %v", err)
			}
			out, err := DecodeFrame(&buf)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if out.Flags != in.Flags || out.Channel != in.Channel {
				t.Fatalf("header mismatch: got %+v want %+v", out, in)
			}
			if !bytes.Equal(out.Payload, in.Payload) {
				t.Fatalf("payload mismatch (len got=%d want=%d)", len(out.Payload), len(in.Payload))
			}
			if buf.Len() != 0 {
				t.Fatalf("trailing bytes in stream: %d", buf.Len())
			}
		})
	}
}

func TestEncodeRejectsBadFrames(t *testing.T) {
	cases := []struct {
		name string
		f    Frame
		want error
	}{
		{"channel out of range", Frame{Flags: FlagEnd, Channel: MaxChannel + 1}, ErrChannelTooLarge},
		{"reserved flag set", Frame{Flags: FlagEnd | 0x02, Channel: 0}, ErrReservedFlag},
		{"END not set", Frame{Flags: 0, Channel: 0}, ErrEndNotSet},
		{"payload too large", Frame{Flags: FlagEnd, Channel: 0, Payload: make([]byte, MaxPayload+1)}, ErrOversizeFrame},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := EncodeFrame(io.Discard, tc.f)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got err %v, want %v", err, tc.want)
			}
		})
	}
}

// TestDecodeOversizeRejectedBeforeAlloc proves the decoder rejects a
// frame whose announced length is past the cap without first
// allocating that many bytes.
func TestDecodeOversizeRejectedBeforeAlloc(t *testing.T) {
	var hdr [headerSize]byte
	hdr[0] = FlagEnd
	binary.BigEndian.PutUint32(hdr[4:8], MaxPayload+1)
	r := bytes.NewReader(hdr[:])
	_, err := DecodeFrame(r)
	if !errors.Is(err, ErrOversizeFrame) {
		t.Fatalf("got err %v, want ErrOversizeFrame", err)
	}
	// Reader should still have all the unread bytes (there were none
	// after the header) — what matters is we didn't try to ReadFull
	// the giant payload. If we had, we'd have errored on EOF first.
}

func TestDecodeMaxPayloadAccepted(t *testing.T) {
	in := Frame{Flags: FlagEnd, Channel: 0, Payload: make([]byte, MaxPayload)}
	var buf bytes.Buffer
	if err := EncodeFrame(&buf, in); err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeFrame(&buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Payload) != MaxPayload {
		t.Fatalf("payload size mismatch: %d", len(out.Payload))
	}
}

func TestDecodeRejectsEndNotSet(t *testing.T) {
	hdr := []byte{0, 0, 0, 0, 0, 0, 0, 0}
	_, err := DecodeFrame(bytes.NewReader(hdr))
	if !errors.Is(err, ErrEndNotSet) {
		t.Fatalf("got %v, want ErrEndNotSet", err)
	}
}

func TestDecodeRejectsReservedFlag(t *testing.T) {
	hdr := []byte{FlagEnd | 0x80, 0, 0, 0, 0, 0, 0, 0}
	_, err := DecodeFrame(bytes.NewReader(hdr))
	if !errors.Is(err, ErrReservedFlag) {
		t.Fatalf("got %v, want ErrReservedFlag", err)
	}
}

func TestDecodeShortHeader(t *testing.T) {
	_, err := DecodeFrame(bytes.NewReader([]byte{FlagEnd, 0, 0, 0}))
	if !errors.Is(err, ErrShortHeader) {
		t.Fatalf("got %v, want ErrShortHeader", err)
	}
}

func TestDecodeShortPayload(t *testing.T) {
	hdr := []byte{FlagEnd, 0, 0, 0, 0, 0, 0, 10}
	stream := append(hdr, []byte("abc")...) // says 10, gives 3
	_, err := DecodeFrame(bytes.NewReader(stream))
	if !errors.Is(err, ErrShortPayload) {
		t.Fatalf("got %v, want ErrShortPayload", err)
	}
}

func TestDecodeEOF(t *testing.T) {
	_, err := DecodeFrame(bytes.NewReader(nil))
	if err != io.EOF {
		t.Fatalf("got %v, want io.EOF", err)
	}
}

// TestChannelIDIsBigEndian24 hand-builds a frame and asserts the
// channel-id bytes are in network byte order.
func TestChannelIDIsBigEndian24(t *testing.T) {
	in := Frame{Flags: FlagEnd, Channel: 0x010203, Payload: nil}
	var buf bytes.Buffer
	if err := EncodeFrame(&buf, in); err != nil {
		t.Fatalf("encode: %v", err)
	}
	b := buf.Bytes()
	if b[1] != 0x01 || b[2] != 0x02 || b[3] != 0x03 {
		t.Fatalf("channel-id bytes %02x %02x %02x, want 01 02 03", b[1], b[2], b[3])
	}
}
