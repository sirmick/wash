package router

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCreditReserveWithinWindow(t *testing.T) {
	c := NewChannelCredit(1000)
	if err := c.Reserve(context.Background(), 400); err != nil {
		t.Fatalf("reserve 400: %v", err)
	}
	if err := c.Reserve(context.Background(), 600); err != nil {
		t.Fatalf("reserve 600: %v", err)
	}
	if c.Sent() != 1000 {
		t.Fatalf("sent=%d, want 1000", c.Sent())
	}
}

func TestCreditReserveBlocksWhenExhausted(t *testing.T) {
	c := NewChannelCredit(100)
	if err := c.Reserve(context.Background(), 100); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	// Second reserve must block.
	done := make(chan error, 1)
	go func() { done <- c.Reserve(context.Background(), 50) }()
	select {
	case err := <-done:
		t.Fatalf("Reserve did not block past window; returned %v", err)
	case <-time.After(50 * time.Millisecond):
		// good — still blocked
	}
	// Grant exactly enough; reserve should now succeed.
	if err := c.Grant(50); err != nil {
		t.Fatalf("grant: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("post-grant reserve: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("grant did not unblock reserve")
	}
}

func TestCreditReserveCancelledByContext(t *testing.T) {
	c := NewChannelCredit(0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Reserve(ctx, 1) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("cancel did not unblock reserve")
	}
}

func TestCreditCloseUnblocksReserve(t *testing.T) {
	c := NewChannelCredit(0)
	done := make(chan error, 1)
	go func() { done <- c.Reserve(context.Background(), 1) }()
	time.Sleep(20 * time.Millisecond)
	c.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrCreditChannelClosed) {
			t.Fatalf("got %v, want ErrCreditChannelClosed", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("close did not unblock reserve")
	}
}

func TestCreditGrantOverflow(t *testing.T) {
	c := NewChannelCredit(^uint64(0) - 5)
	if err := c.Grant(10); !errors.Is(err, ErrCreditOverflow) {
		t.Fatalf("got %v, want ErrCreditOverflow", err)
	}
	// Counter must not have changed.
	if c.Granted() != ^uint64(0)-5 {
		t.Fatalf("granted changed after overflow: %d", c.Granted())
	}
}

func TestCreditCloseIdempotent(t *testing.T) {
	c := NewChannelCredit(10)
	c.Close()
	c.Close() // must not panic on double close
}

func TestCreditConcurrentGrantsAndReserves(t *testing.T) {
	// Stress test: many concurrent producers reserving, one
	// granter feeding the channel. All reserves should complete,
	// no double-counting, no deadlock.
	c := NewChannelCredit(1024)
	const producers = 32
	const perProducerBytes = 256
	var wg sync.WaitGroup
	wg.Add(producers)
	for i := 0; i < producers; i++ {
		go func() {
			defer wg.Done()
			if err := c.Reserve(context.Background(), perProducerBytes); err != nil {
				t.Errorf("reserve: %v", err)
			}
		}()
	}
	// Grant in small chunks to force most producers to actually
	// wait at least once.
	go func() {
		needed := uint32(producers * perProducerBytes)
		// We already have 1024 granted at start. Top up the rest.
		need := needed - 1024
		const chunk uint32 = 128
		for need > 0 {
			n := chunk
			if n > need {
				n = need
			}
			if err := c.Grant(n); err != nil {
				t.Errorf("grant: %v", err)
				return
			}
			need -= n
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Wait()
	wantSent := uint64(producers * perProducerBytes)
	if c.Sent() != wantSent {
		t.Fatalf("sent=%d, want %d", c.Sent(), wantSent)
	}
	if c.Granted() < wantSent {
		t.Fatalf("granted=%d, want ≥ %d", c.Granted(), wantSent)
	}
}
