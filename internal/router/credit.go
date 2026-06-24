// Per-channel credit/flow-control accounting (docs/QOS.md §5).
//
// Each raw channel from router → shell gets a credit window. The
// router tracks bytes sent vs. bytes the shell has authorized. When
// they meet, producers (router-side raw writes) block until the FE
// grants more credit via channel.credit. This is the second backstop
// after class-based scheduling — class fairness keeps interactive
// frames moving past bulk congestion globally; per-channel credits
// let the FE pace individual streams ("pause wash-fm but not term").
//
// Only Bulk-class frames consume credit. Interactive-class flows
// (bundle delivery, scrollback replay, channel control) bypass
// credit accounting entirely — they're transactional, not chatty,
// and treating them as creditable would gate bundle download on
// the FE's credit emission, which it has no reason to send for a
// one-shot transaction.

package router

import (
	"context"
	"errors"
	"sync"
)

// DefaultChannelCredit is the initial per-raw-channel credit window
// in bytes (QOS.md §5.2). Tuned to be enough for a few seconds of
// terminal output at typical pty rates without any FE credit emission
// — by the time a normal stream exhausts it, the FE has had multiple
// render cycles to acknowledge bytes.
const DefaultChannelCredit uint64 = 64 * 1024

// ErrCreditOverflow is returned by ChannelCredit.Grant when the
// running grant total would overflow uint64.
var ErrCreditOverflow = errors.New("router: channel credit overflow")

// ErrCreditChannelClosed is returned by Reserve when the channel
// has been closed (closeChannel ran). Producers treat this as a
// graceful "channel is gone."
var ErrCreditChannelClosed = errors.New("router: channel credit closed")

// ChannelCredit is the per-channel credit ledger. Safe for concurrent
// use; one instance per channelBinding. Methods are correctness-
// over-performance — the contended path is mu + a chan signal,
// which is fine because Reserve only blocks when the FE is
// genuinely behind.
type ChannelCredit struct {
	mu      sync.Mutex
	sent    uint64
	granted uint64

	// notify is pinged (non-blocking) on every Grant so any
	// goroutine waiting in Reserve gets a chance to re-check.
	// Buffered=1 so a Grant arriving with no waiter doesn't lose
	// its wakeup.
	notify chan struct{}
	// done is closed when the channel binding is torn down,
	// unblocking any pending Reserve calls with
	// ErrCreditChannelClosed.
	done     chan struct{}
	closedMu sync.Mutex
	closed   bool
}

// NewChannelCredit returns a credit ledger primed with `initial`
// bytes granted.
func NewChannelCredit(initial uint64) *ChannelCredit {
	return &ChannelCredit{
		granted: initial,
		notify:  make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
}

// Reserve blocks ctx-cancellable until at least n bytes of credit
// are available, then debits the running counter. Returns
// ErrCreditChannelClosed if Close was called; ctx.Err() on cancel.
//
// Reserve does NOT split a large n across credit grants — it waits
// for the full n to be available in one go. Callers chunk their
// writes upstream if they want partial-progress behavior.
func (c *ChannelCredit) Reserve(ctx context.Context, n uint64) error {
	for {
		c.mu.Lock()
		if c.sent+n <= c.granted {
			c.sent += n
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		// Wait for a Grant signal or teardown.
		select {
		case <-c.notify:
			// New credit may have arrived — loop and re-check.
		case <-c.done:
			return ErrCreditChannelClosed
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// TryReserve is the non-blocking variant of Reserve: it debits n bytes
// and returns true only if the full n is available right now. It never
// blocks and never waits for a grant. Used by the terminal forward path
// (docs/PTY_ROBUST.md, Fix B) so a wedged FE — one that has stopped
// granting credit — can never block the producing goroutine and thereby
// stall the child shell. A false return means "FE is behind"; the caller
// holds the bytes in the scrollback ring and resyncs on recovery rather
// than shipping a torn stream.
func (c *ChannelCredit) TryReserve(n uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sent+n <= c.granted {
		c.sent += n
		return true
	}
	return false
}

// Refund returns n previously-reserved bytes to the window. Used when a
// reserve succeeded but the subsequent enqueue could not proceed (the
// scheduler queue was full), so the credit was debited for bytes that
// were never sent. Clamps at zero — never underflows.
func (c *ChannelCredit) Refund(n uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n > c.sent {
		c.sent = 0
		return
	}
	c.sent -= n
}

// Grant adds n bytes of credit to the channel's budget and wakes
// any goroutine waiting in Reserve. Returns ErrCreditOverflow if
// the running total would wrap; the caller MAY close the channel
// or reply with an error envelope.
func (c *ChannelCredit) Grant(n uint32) error {
	c.mu.Lock()
	newGranted := c.granted + uint64(n)
	if newGranted < c.granted {
		c.mu.Unlock()
		return ErrCreditOverflow
	}
	c.granted = newGranted
	c.mu.Unlock()
	// Non-blocking wake. If the buffer is full a previous wake is
	// still pending; the Reserve loop will see fresh granted on its
	// next iteration regardless.
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return nil
}

// Close marks the channel as gone. Any pending Reserve calls return
// ErrCreditChannelClosed; future Reserve calls return immediately
// with the same error. Idempotent.
func (c *ChannelCredit) Close() {
	c.closedMu.Lock()
	defer c.closedMu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	close(c.done)
}

// Sent returns the running debit total (bytes already reserved).
// Test-only; production code MUST NOT base decisions on this.
func (c *ChannelCredit) Sent() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent
}

// Granted returns the running grant total (bytes the FE has
// authorized, cumulative). Test-only.
func (c *ChannelCredit) Granted() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.granted
}
