package login

import (
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock for deterministic window tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

// newTestLimiter wires a rateLimiter to a controllable clock.
func newTestLimiter(max int, window time.Duration) (*rateLimiter, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := newRateLimiter(max, window)
	l.now = clk.now
	return l, clk
}

func TestRateLimiterLocksAfterMaxFails(t *testing.T) {
	l, _ := newTestLimiter(3, time.Minute)
	const key = "u:alice"
	for i := 0; i < 3; i++ {
		if !l.allowed(key) {
			t.Fatalf("attempt %d: blocked before reaching max", i)
		}
		l.fail(key)
	}
	if l.allowed(key) {
		t.Fatal("expected lockout after 3 failures, still allowed")
	}
}

func TestRateLimiterWindowExpiry(t *testing.T) {
	l, clk := newTestLimiter(3, time.Minute)
	const key = "ip:10.0.0.1"
	for i := 0; i < 3; i++ {
		l.fail(key)
	}
	if l.allowed(key) {
		t.Fatal("should be locked immediately after 3 fails")
	}
	// Just before the oldest failure ages out — still locked.
	clk.add(59 * time.Second)
	if l.allowed(key) {
		t.Fatal("should still be locked 59s in")
	}
	// Past the window — the oldest failure drops, back under max.
	clk.add(2 * time.Second)
	if !l.allowed(key) {
		t.Fatal("should be allowed again once the window passes")
	}
}

func TestRateLimiterResetClearsHistory(t *testing.T) {
	l, _ := newTestLimiter(3, time.Minute)
	const key = "u:bob"
	l.fail(key)
	l.fail(key)
	l.reset(key)
	// After reset the next two failures shouldn't trip the limit.
	l.fail(key)
	l.fail(key)
	if !l.allowed(key) {
		t.Fatal("reset should have cleared earlier failures")
	}
}

func TestRateLimiterAnyKeyLocks(t *testing.T) {
	l, _ := newTestLimiter(3, time.Minute)
	// Username under limit, IP over it ⇒ the attempt is blocked.
	l.fail("ip:10.0.0.1")
	l.fail("ip:10.0.0.1")
	l.fail("ip:10.0.0.1")
	if l.allowed("u:fresh", "ip:10.0.0.1") {
		t.Fatal("a single locked key must block the whole attempt")
	}
}

func TestRateLimiterRetryAfter(t *testing.T) {
	l, clk := newTestLimiter(2, time.Minute)
	const key = "u:carol"
	l.fail(key)
	clk.add(10 * time.Second)
	l.fail(key) // now locked; oldest failure was 10s ago
	d := l.retryAfter(key)
	// Oldest failure unlocks at +60s from when it happened, i.e. 50s
	// from now.
	if d < 49*time.Second || d > 51*time.Second {
		t.Fatalf("retryAfter = %s, want ~50s", d)
	}
	if got := l.retryAfter("u:nobody"); got != 0 {
		t.Fatalf("retryAfter for unlocked key = %s, want 0", got)
	}
}

func TestRateLimiterSweepDropsExpired(t *testing.T) {
	l, clk := newTestLimiter(5, time.Minute)
	// Spray distinct keys to trigger a sweep, then age them all out.
	for i := 0; i < sweepEvery+10; i++ {
		l.fail("k:" + string(rune('a'+i%26)) + string(rune('0'+i/26)))
	}
	clk.add(2 * time.Minute)
	// One more fail triggers another sweep pass after the window;
	// expired buckets should be gone.
	for i := 0; i < sweepEvery; i++ {
		l.fail("trigger")
	}
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n > 2 {
		t.Fatalf("expected expired buckets swept, have %d", n)
	}
}
