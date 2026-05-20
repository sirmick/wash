package wire

import (
	"bytes"
	"errors"
	"testing"
)

func TestMuxDispatch(t *testing.T) {
	m := NewMux()
	gotCh0 := false
	gotCh1 := false
	m.On(0, func(f Frame) error { gotCh0 = true; return nil })
	m.On(1, func(f Frame) error { gotCh1 = true; return nil })

	if err := m.Dispatch(Frame{Flags: FlagEnd, Channel: 0}); err != nil {
		t.Fatal(err)
	}
	if err := m.Dispatch(Frame{Flags: FlagEnd, Channel: 1}); err != nil {
		t.Fatal(err)
	}
	if !gotCh0 || !gotCh1 {
		t.Fatalf("dispatch missed: ch0=%v ch1=%v", gotCh0, gotCh1)
	}
}

func TestMuxFallback(t *testing.T) {
	m := NewMux()
	fallbackCalled := 0
	m.SetFallback(func(f Frame) error { fallbackCalled++; return nil })
	if err := m.Dispatch(Frame{Flags: FlagEnd, Channel: 99}); err != nil {
		t.Fatal(err)
	}
	if fallbackCalled != 1 {
		t.Fatalf("fallback called %d times, want 1", fallbackCalled)
	}
}

func TestMuxNoHandlerNoFallback(t *testing.T) {
	m := NewMux()
	if err := m.Dispatch(Frame{Flags: FlagEnd, Channel: 7}); err != nil {
		t.Fatalf("dispatch with no handler should be silently dropped, got %v", err)
	}
}

func TestMuxServeReadsToEOF(t *testing.T) {
	var buf bytes.Buffer
	for ch := uint32(0); ch < 3; ch++ {
		if err := EncodeFrame(&buf, Frame{Flags: FlagEnd, Channel: ch, Payload: []byte{byte(ch)}}); err != nil {
			t.Fatal(err)
		}
	}
	m := NewMux()
	seen := map[uint32]int{}
	m.SetFallback(func(f Frame) error { seen[f.Channel]++; return nil })
	if err := m.Serve(&buf); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("seen=%v want 3 channels", seen)
	}
}

func TestMuxServeStopsOnHandlerError(t *testing.T) {
	var buf bytes.Buffer
	for i := 0; i < 3; i++ {
		if err := EncodeFrame(&buf, Frame{Flags: FlagEnd, Channel: 0, Payload: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	myErr := errors.New("stop")
	m := NewMux()
	count := 0
	m.On(0, func(f Frame) error { count++; return myErr })
	err := m.Serve(&buf)
	if !errors.Is(err, myErr) {
		t.Fatalf("got %v, want stop", err)
	}
	if count != 1 {
		t.Fatalf("handler called %d times, want 1", count)
	}
}
