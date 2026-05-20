package wire

import (
	"bytes"
	"testing"
)

// FuzzDecodeFrame feeds arbitrary bytes to DecodeFrame; it must
// never panic and must reject anything that does not parse as a
// valid v0.0 frame.
func FuzzDecodeFrame(f *testing.F) {
	// Seeds: known-good frames + known-bad inputs.
	good := []Frame{
		{Flags: FlagEnd, Channel: 0, Payload: nil},
		{Flags: FlagEnd, Channel: 1, Payload: []byte("hello")},
		{Flags: FlagEnd, Channel: MaxChannel, Payload: bytes.Repeat([]byte{0xAA}, 7)},
	}
	for _, g := range good {
		var buf bytes.Buffer
		if err := EncodeFrame(&buf, g); err != nil {
			f.Fatal(err)
		}
		f.Add(buf.Bytes())
	}
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
	f.Add([]byte{FlagEnd, 0, 0, 0, 0x7f, 0xff, 0xff, 0xff}) // announces near-2GiB

	f.Fuzz(func(t *testing.T, in []byte) {
		// We just need to not panic; either decoding succeeds or we
		// get an error. Either is fine.
		r := bytes.NewReader(in)
		_, _ = DecodeFrame(r)
	})
}

// FuzzDecodeCtrl feeds arbitrary bytes to DecodeCtrl; it must never
// panic.
func FuzzDecodeCtrl(f *testing.F) {
	for _, m := range []any{
		NewIdentity("com.wash.about", 1, "0.0.1"),
		NewError(ErrCodeBadFrame, "x"),
		NewAssetData(7, "aGVsbG8=", true),
	} {
		b, _ := EncodeCtrl(m)
		f.Add(b)
	}
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"t":42}`))
	f.Add([]byte(`not json`))

	f.Fuzz(func(t *testing.T, in []byte) {
		_, _ = DecodeCtrl(in)
	})
}

// FuzzDecodeEvt feeds arbitrary bytes to DecodeEvt; it must never
// panic.
func FuzzDecodeEvt(f *testing.F) {
	for _, m := range []any{
		NewEvtWindowMapped(1),
		NewEvtSpawnRequest("com.wash.about"),
		NewEvtAppMsg(1, "x"),
	} {
		b, _ := EncodeEvt(m)
		f.Add(b)
	}
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, in []byte) {
		_, _ = DecodeEvt(in)
	})
}
