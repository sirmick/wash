package pty

import "testing"

// WithExitHold: the raw channel outlives the pty when the caller says so,
// onClose sees it held, and ReleaseChannel lets go exactly once.
func TestExitHoldKeepsTheChannelUntilReleased(t *testing.T) {
	var sawHeld bool
	s := &Session{done: make(chan struct{})}
	WithExitHold(func(_ *Session, reason string) bool { return reason == "pty eof" })(s)
	s.onClose = func(s *Session, _ string) { sawHeld = s.ChannelHeld() }

	s.closeWithReason("pty eof")
	if !sawHeld {
		t.Fatal("onClose ran with the channel already let go; the hold decision must precede it")
	}
	if !s.ChannelHeld() {
		t.Fatal("channel not held after a hold=true close")
	}
	s.ReleaseChannel()
	if s.ChannelHeld() {
		t.Fatal("still held after ReleaseChannel")
	}
	// Idempotent — a second release (dismiss twice, window close after
	// dismiss) must not double-close anything.
	s.ReleaseChannel()
}

func TestExitHoldDeclinedClosesAsBefore(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	WithExitHold(func(_ *Session, reason string) bool { return reason == "pty eof" })(s)
	s.closeWithReason("user requested")
	if s.ChannelHeld() {
		t.Fatal("a close the hold declined left the channel held")
	}
	// No hold at all: the plain path.
	plain := &Session{done: make(chan struct{})}
	plain.closeWithReason("pty eof")
	if plain.ChannelHeld() {
		t.Fatal("a session without WithExitHold reports a held channel")
	}
}
