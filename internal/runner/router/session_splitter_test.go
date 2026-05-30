package routerrun

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
)

// fakeRaw is a controllable FrameTransport: tests push frames it should yield
// from ReadFrame and read back what was written via WriteFrame.
type fakeRaw struct {
	in     chan wire.Frame
	out    chan wire.Frame
	closed chan struct{}
}

func newFakeRaw() *fakeRaw {
	return &fakeRaw{in: make(chan wire.Frame, 16), out: make(chan wire.Frame, 16), closed: make(chan struct{})}
}

func (f *fakeRaw) ReadFrame() (wire.Frame, error) {
	select {
	case fr := <-f.in:
		return fr, nil
	case <-f.closed:
		return wire.Frame{}, io.EOF
	}
}
func (f *fakeRaw) WriteFrame(fr wire.Frame) error { f.out <- fr; return nil }
func (f *fakeRaw) Close() error                   { close(f.closed); return nil }

func ctrlFrame(t string) wire.Frame {
	p, _ := json.Marshal(map[string]string{"t": t})
	return wire.Frame{Flags: wire.FlagEnd, Channel: 0, Payload: p}
}
func dataFrame(ch uint32, s string) wire.Frame {
	return wire.Frame{Flags: wire.FlagEnd, Channel: ch, Payload: []byte(s)}
}

func TestSessionSplitterBasic(t *testing.T) {
	raw := newFakeRaw()
	s := newSessionSplitter(raw, func(string, ...any) {})
	go s.run()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw.in <- ctrlFrame(wire.TSessionOpen)
	v := s.nextSession(ctx)
	if v == nil {
		t.Fatal("expected a viewer session after SessionOpen")
	}

	// A data frame reaches the viewer.
	raw.in <- dataFrame(0, "hello")
	fr, err := v.ReadFrame()
	if err != nil || string(fr.Payload) != "hello" {
		t.Fatalf("ReadFrame = (%q, %v)", fr.Payload, err)
	}

	// The active viewer's write reaches the raw stream.
	go func() { _ = v.WriteFrame(dataFrame(0, "reply")) }()
	if w := <-raw.out; string(w.Payload) != "reply" {
		t.Fatalf("WriteFrame reached raw as %q", w.Payload)
	}

	// SessionClose ends the session: ReadFrame returns EOF (the browser left).
	raw.in <- ctrlFrame(wire.TSessionClose)
	if _, err := v.ReadFrame(); err != io.EOF {
		t.Fatalf("after SessionClose, ReadFrame err = %v, want EOF", err)
	}
}

func TestSessionSplitterTakeover(t *testing.T) {
	raw := newFakeRaw()
	s := newSessionSplitter(raw, func(string, ...any) {})
	go s.run()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw.in <- ctrlFrame(wire.TSessionOpen)
	a := s.nextSession(ctx)
	if a == nil {
		t.Fatal("expected viewer A")
	}

	// A second SessionOpen with no intervening close is a takeover: A is EOF'd,
	// B becomes the active viewer.
	raw.in <- ctrlFrame(wire.TSessionOpen)
	if _, err := a.ReadFrame(); err != io.EOF {
		t.Fatalf("A should EOF on takeover, got %v", err)
	}
	b := s.nextSession(ctx)
	if b == nil || b == a {
		t.Fatal("expected a fresh viewer B after takeover")
	}

	// A is no longer active: its stale teardown writes are dropped (return nil,
	// nothing reaches raw). B's write goes through.
	if err := a.WriteFrame(dataFrame(0, "stale")); err != nil {
		t.Fatalf("stale write returned err %v", err)
	}
	go func() { _ = b.WriteFrame(dataFrame(0, "fresh")) }()
	if w := <-raw.out; string(w.Payload) != "fresh" {
		t.Fatalf("active viewer B write reached raw as %q", w.Payload)
	}
}

func TestSessionSplitterStreamClose(t *testing.T) {
	raw := newFakeRaw()
	s := newSessionSplitter(raw, func(string, ...any) {})
	go s.run()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw.in <- ctrlFrame(wire.TSessionOpen)
	v := s.nextSession(ctx)

	// Serial dies → active viewer EOFs and nextSession returns nil.
	raw.Close()
	if _, err := v.ReadFrame(); err != io.EOF {
		t.Fatalf("viewer should EOF when stream closes, got %v", err)
	}
	if got := s.nextSession(ctx); got != nil {
		t.Fatalf("nextSession after stream close = %v, want nil", got)
	}
}
