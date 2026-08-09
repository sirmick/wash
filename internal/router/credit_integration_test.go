package router

import (
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

// TestCreditBulkRawBlocksWithoutGrant — a Bulk raw write that would
// exceed the channel's current credit window blocks until the FE
// grants more bytes. This is the producer-side enforcement that
// makes ShellChannelCredit meaningful end-to-end.
func TestCreditBulkRawBlocksWithoutGrant(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(format string, args ...any) { t.Logf("router: "+format, args...) })

	sess, fe, cleanup := newTestShellSession(t)
	sess.router = r
	defer cleanup()

	// Drain FE side so the scheduler can flush.
	go func() {
		for {
			if _, err := fe.ReadFrame(); err != nil {
				return
			}
		}
	}()

	// Register a credit-tracked channel with a small initial window.
	const channelID = 5
	const initialCredit uint64 = 256
	b := &channelBinding{
		channelID: channelID,
		credit:    NewChannelCredit(initialCredit),
	}
	r.registerChannel(b)
	defer r.closeChannel(channelID, "test done")

	// First write of 256 bytes consumes the entire window.
	if err := sess.WriteRawFrame(channelID, make([]byte, 256)); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Second 256-byte write must block on the credit gate.
	done := make(chan error, 1)
	go func() {
		done <- sess.WriteRawFrame(channelID, make([]byte, 256))
	}()
	select {
	case err := <-done:
		t.Fatalf("second write did not block on credit gate; returned %v", err)
	case <-time.After(50 * time.Millisecond):
		// good — still blocked
	}
	// Grant credit; second write should now succeed.
	if err := sess.handleChannelCredit(wire.NewShellChannelCredit(channelID, 256)); err != nil {
		t.Fatalf("credit grant: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("post-grant write: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("credit grant did not unblock the producer")
	}
}

// TestCreditInteractiveRawBypassesCredit — Interactive class raw
// writes (bundle/replay transactional flows) MUST NOT consult
// credit. A bundle is allowed to download regardless of FE credit
// emission.
func TestCreditInteractiveRawBypassesCredit(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(format string, args ...any) { t.Logf("router: "+format, args...) })

	sess, fe, cleanup := newTestShellSession(t)
	sess.router = r
	defer cleanup()

	go func() {
		for {
			if _, err := fe.ReadFrame(); err != nil {
				return
			}
		}
	}()

	const channelID = 5
	// Credit window of zero — every Bulk write would block forever.
	b := &channelBinding{
		channelID: channelID,
		credit:    NewChannelCredit(0),
	}
	r.registerChannel(b)
	defer r.closeChannel(channelID, "test done")

	// Interactive raw write should pass through.
	done := make(chan error, 1)
	go func() {
		done <- sess.WriteRawFrameClass(channelID, make([]byte, 1024), wire.ClassInteractive)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("interactive write: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("interactive write should not block on credit (got stuck)")
	}
}

// TestCreditChannelCloseUnblocksProducer — when the channel is
// closed while a producer is waiting on the credit gate, the
// producer returns ErrCreditChannelClosed instead of leaking.
func TestCreditChannelCloseUnblocksProducer(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(format string, args ...any) { t.Logf("router: "+format, args...) })

	sess, fe, cleanup := newTestShellSession(t)
	sess.router = r
	defer cleanup()

	go func() {
		for {
			if _, err := fe.ReadFrame(); err != nil {
				return
			}
		}
	}()

	const channelID = 5
	b := &channelBinding{
		channelID: channelID,
		credit:    NewChannelCredit(0),
	}
	r.registerChannel(b)

	done := make(chan error, 1)
	go func() {
		done <- sess.WriteRawFrame(channelID, make([]byte, 100))
	}()
	time.Sleep(20 * time.Millisecond)
	r.closeChannel(channelID, "test")
	select {
	case err := <-done:
		// Accept any error — Close + scheduler-close races
		// can surface either as ErrCreditChannelClosed or as a
		// transport-level failure depending on timing. What
		// matters is the producer didn't leak.
		if err == nil {
			t.Fatalf("expected error after channel close, got nil")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("channel close did not unblock the producer")
	}
}

// TestCreditOverflowClosesChannel — a malicious / buggy FE that
// grants past uint64 max gets its channel closed rather than
// tearing down the whole shell session.
func TestCreditOverflowClosesChannel(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(format string, args ...any) { t.Logf("router: "+format, args...) })

	sess, fe, cleanup := newTestShellSession(t)
	sess.router = r
	defer cleanup()

	go func() {
		for {
			if _, err := fe.ReadFrame(); err != nil {
				return
			}
		}
	}()

	const channelID = 5
	b := &channelBinding{
		channelID: channelID,
		credit:    NewChannelCredit(^uint64(0) - 5),
	}
	r.registerChannel(b)

	// Grant 100 more bytes: overflows the granted counter.
	if err := sess.handleChannelCredit(wire.NewShellChannelCredit(channelID, 100)); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	// Channel should be removed from the router's channel map.
	if r.lookupChannel(channelID) != nil {
		t.Fatalf("channel still bound after overflow; want closed")
	}
}

// TestCreditUnknownChannelGrantIgnored — a credit frame for a
// channel that's already been closed is silently ignored. The FE
// can race a closed channel between its credit emission and the
// router's unbind; we don't want to treat that as an error.
func TestCreditUnknownChannelGrantIgnored(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(format string, args ...any) { t.Logf("router: "+format, args...) })

	sess, fe, cleanup := newTestShellSession(t)
	sess.router = r
	defer cleanup()

	go func() {
		for {
			if _, err := fe.ReadFrame(); err != nil {
				return
			}
		}
	}()

	if err := sess.handleChannelCredit(wire.NewShellChannelCredit(9999, 1024)); err != nil {
		t.Fatalf("handler returned error for unknown channel: %v", err)
	}
}
