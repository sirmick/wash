package login

// In-memory failure rate limiter for the /auth endpoint.
//
// The login PAM gate is the only trust boundary wash-login defends
// (see docs threat model: TLS assumed, the authenticated user already
// has box access). A weak password is therefore only as safe as our
// willingness to let an attacker keep guessing — `su`'s ~2s
// pam_faildelay only slows a *serial* guesser, and nothing throttles
// parallel POSTs to /auth at all. This limiter closes that gap.
//
// Two independent buckets are checked per attempt: one keyed on the
// submitted username, one on the client IP. The username bucket is the
// spoof-proof backstop — an attacker who forges X-Forwarded-For to
// dodge the per-IP bucket still trips the per-username one. A blocked
// attempt is rejected *before* the credentials reach the Authenticator,
// so a lockout costs nothing and leaks nothing.
//
// Sliding window: we keep the timestamps of recent failures per key,
// pruned to the window on every touch. At or above max within the window the
// key is locked until the oldest in-window failure ages out. A success
// resets both of the attempt's keys immediately.

import (
	"sync"
	"time"
)

const (
	// defaultMaxAuthFails is how many failures a single key may
	// accumulate within the window before it's locked out.
	defaultMaxAuthFails = 5
	// defaultAuthWindow is the sliding window over which failures are
	// counted.
	defaultAuthWindow = 15 * time.Minute
	// sweepEvery bounds map growth: after this many fail() calls we
	// drop empty/expired buckets so a spray of distinct usernames/IPs
	// can't grow the map without bound.
	sweepEvery = 256
)

// rateLimiter is a sliding-window failure counter keyed by arbitrary
// strings. Safe for concurrent use.
type rateLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	buckets map[string][]time.Time
	calls   int
	// now is injectable so tests can drive the clock deterministically.
	now func() time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	if max <= 0 {
		max = defaultMaxAuthFails
	}
	if window <= 0 {
		window = defaultAuthWindow
	}
	return &rateLimiter{
		max:     max,
		window:  window,
		buckets: make(map[string][]time.Time),
		now:     time.Now,
	}
}

// pruneLocked drops failures older than the window from key's bucket
// and returns the surviving slice. Caller holds mu.
func (l *rateLimiter) pruneLocked(key string) []time.Time {
	cutoff := l.now().Add(-l.window)
	fails := l.buckets[key]
	i := 0
	for i < len(fails) && fails[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		fails = append([]time.Time(nil), fails[i:]...)
		if len(fails) == 0 {
			delete(l.buckets, key)
		} else {
			l.buckets[key] = fails
		}
	}
	return fails
}

// allowed reports whether all of keys are currently under the limit.
// A single locked key blocks the whole attempt.
func (l *rateLimiter) allowed(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		if len(l.pruneLocked(k)) >= l.max {
			return false
		}
	}
	return true
}

// fail records a failure against each of keys.
func (l *rateLimiter) fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, k := range keys {
		fails := l.pruneLocked(k)
		l.buckets[k] = append(fails, now)
	}
	l.calls++
	if l.calls >= sweepEvery {
		l.calls = 0
		l.sweepLocked()
	}
}

// reset clears the failure history for each of keys — called on a
// successful auth so a user who fat-fingered a few times isn't left
// near the lockout threshold.
func (l *rateLimiter) reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.buckets, k)
	}
}

// retryAfter returns how long until the most-constrained of keys frees
// up (the oldest in-window failure of the fullest locked bucket ages
// out). Zero when nothing is currently locked. Caller need not hold mu.
func (l *rateLimiter) retryAfter(keys ...string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	var max time.Duration
	for _, k := range keys {
		fails := l.pruneLocked(k)
		if len(fails) < l.max {
			continue
		}
		// Unlocks when the oldest failure leaves the window.
		d := fails[0].Add(l.window).Sub(l.now())
		if d > max {
			max = d
		}
	}
	return max
}

// sweepLocked drops empty buckets and buckets whose every entry has
// aged out. Caller holds mu.
func (l *rateLimiter) sweepLocked() {
	for k := range l.buckets {
		if len(l.pruneLocked(k)) == 0 {
			delete(l.buckets, k)
		}
	}
}
