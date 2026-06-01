package routerrun

import (
	"context"
	"io"
	"sync"

	"github.com/sirmick/wash/internal/wire"
)

// sessionSplitter turns one persistent frame stream (a virtio-console / serial
// port) into a sequence of virtual connections — one per browser viewer
// session, delimited by the wire.SessionOpen / wire.SessionClose frames the
// host-side proxy injects (docs/NET.md §8.3). Each virtual connection is fed to
// a fresh router.HandleShell, so the router sees per-browser connect/disconnect
// exactly as it would behind a TCP/WebSocket listener — no transport-specific
// code in the router core. "Takeover" is the natural behaviour: a new
// SessionOpen ends the current viewer (EOF) and starts the next.
//
// One viewer is active at a time (the proxy bridges one browser onto the serial
// at a time). The splitter owns the raw stream for its whole life; it never
// reopens the port, so the host side never gets orphaned.
type sessionSplitter struct {
	raw  wire.FrameTransport
	logf func(string, ...any)

	sessions chan *viewerConn // beginSession pushes; nextSession pops
	done     chan struct{}    // closed when raw errors (serial gone)
	doneErr  error

	mu     sync.Mutex
	active *viewerConn
}

func newSessionSplitter(raw wire.FrameTransport, logf func(string, ...any)) *sessionSplitter {
	return &sessionSplitter{
		raw:      raw,
		logf:     logf,
		sessions: make(chan *viewerConn, 1),
		done:     make(chan struct{}),
	}
}

type frameKind int

const (
	kFrame frameKind = iota
	kSessionOpen
	kSessionClose
)

func (s *sessionSplitter) classify(f wire.Frame) frameKind {
	if f.Channel != 0 {
		return kFrame
	}
	t, err := wire.PeekType(f.Payload)
	if err != nil {
		return kFrame
	}
	switch t {
	case wire.TSessionOpen:
		return kSessionOpen
	case wire.TSessionClose:
		return kSessionClose
	}
	return kFrame
}

// run reads the raw stream forever, routing data frames to the active viewer
// and acting on session boundaries. It exits when the raw stream errors (the
// serial closed / the VM is going away).
func (s *sessionSplitter) run() {
	for {
		f, err := s.raw.ReadFrame()
		if err != nil {
			s.shutdown(err)
			return
		}
		switch s.classify(f) {
		case kSessionOpen:
			s.beginSession()
		case kSessionClose:
			s.endSession()
		default:
			s.deliver(f)
		}
	}
}

func (s *sessionSplitter) beginSession() {
	s.mu.Lock()
	if s.active != nil {
		s.active.eof() // takeover: end the current viewer
	}
	v := newViewerConn(s)
	s.active = v
	s.mu.Unlock()

	// Hand the new viewer to nextSession, dropping any not-yet-consumed
	// pending one (latest browser wins). The proxy serialises browsers, so in
	// practice the channel is empty; the drain is just defensive.
	for {
		select {
		case s.sessions <- v:
			return
		case <-s.done:
			return
		default:
			select {
			case stale := <-s.sessions:
				stale.eof()
			default:
			}
		}
	}
}

func (s *sessionSplitter) endSession() {
	s.mu.Lock()
	if s.active != nil {
		s.active.eof()
		s.active = nil
	}
	s.mu.Unlock()
}

func (s *sessionSplitter) deliver(f wire.Frame) {
	s.mu.Lock()
	v := s.active
	s.mu.Unlock()
	if v == nil {
		return // frame outside any session — drop
	}
	v.feed(f)
}

func (s *sessionSplitter) shutdown(err error) {
	s.mu.Lock()
	s.doneErr = err
	if s.active != nil {
		s.active.eof()
		s.active = nil
	}
	s.mu.Unlock()
	close(s.done)
}

// nextSession blocks until a browser attaches (SessionOpen) or the stream dies.
// Returns nil when the splitter is shutting down or ctx is cancelled.
func (s *sessionSplitter) nextSession(ctx context.Context) *viewerConn {
	select {
	case v := <-s.sessions:
		return v
	case <-s.done:
		return nil
	case <-ctx.Done():
		return nil
	}
}

// writeFrom forwards a frame from viewer v to the raw stream, but only while v
// is the active session. Stale writes from a superseded HandleShell's teardown
// are dropped so they can't bleed into the next browser's stream.
func (s *sessionSplitter) writeFrom(v *viewerConn, f wire.Frame) error {
	s.mu.Lock()
	active := s.active == v
	s.mu.Unlock()
	if !active {
		return nil
	}
	return s.raw.WriteFrame(f)
}

// viewerConn is a wire.FrameTransport view of one browser session, carved out
// of the shared serial by the splitter. HandleShell consumes it like any other
// connection: it reads frames until EOF (the browser left or was taken over),
// then runs its normal teardown.
type viewerConn struct {
	s      *sessionSplitter
	in     chan wire.Frame
	closed chan struct{}
	once   sync.Once
}

func newViewerConn(s *sessionSplitter) *viewerConn {
	return &viewerConn{s: s, in: make(chan wire.Frame, 64), closed: make(chan struct{})}
}

func (v *viewerConn) feed(f wire.Frame) {
	select {
	case v.in <- f:
	case <-v.closed:
	}
}

func (v *viewerConn) eof() { v.once.Do(func() { close(v.closed) }) }

// ReadFrame returns the next frame for this session, or io.EOF once it ends.
func (v *viewerConn) ReadFrame() (wire.Frame, error) {
	select {
	case f := <-v.in:
		return f, nil
	case <-v.closed:
		return wire.Frame{}, io.EOF
	}
}

func (v *viewerConn) WriteFrame(f wire.Frame) error { return v.s.writeFrom(v, f) }

func (v *viewerConn) Close() error { v.eof(); return nil }
