package proc

import (
	"math"
	"testing"
)

// TestCPUPercentBasic exercises a hand-built before/after with a
// known busy delta. 50 busy jiffies out of a 200-jiffy window → 25%.
func TestCPUPercentBasic(t *testing.T) {
	prev := CPUTimes{User: 100, System: 100, Idle: 800, IOWait: 0} // total 1000, busy 200
	curr := CPUTimes{User: 150, System: 100, Idle: 950, IOWait: 0} // total 1200, busy 250
	// total_delta = 200; busy_delta = 50; → 25%.
	got := CPUPercent(prev, curr)
	if math.Abs(got-25.0) > 0.001 {
		t.Fatalf("CPUPercent=%.3f, want ~25.0", got)
	}
}

// TestCPUPercentIdleHost a system completely idle: 0% busy.
func TestCPUPercentIdleHost(t *testing.T) {
	prev := CPUTimes{Idle: 1000}
	curr := CPUTimes{Idle: 2000} // total grew by 1000, all idle
	got := CPUPercent(prev, curr)
	if got != 0 {
		t.Fatalf("CPUPercent on idle host=%.3f, want 0", got)
	}
}

// TestCPUPercentFullyPinned a single hyperthread is pegged at 100%
// for the full window.
func TestCPUPercentFullyPinned(t *testing.T) {
	prev := CPUTimes{}
	curr := CPUTimes{User: 1000} // total 1000, busy 1000, idle 0
	got := CPUPercent(prev, curr)
	if math.Abs(got-100.0) > 0.001 {
		t.Fatalf("CPUPercent at pin=%.3f, want 100", got)
	}
}

// TestCPUPercentNoDeltaReturnsZero a sample where total didn't move
// (clock skew / suspend / first call with zero baseline) must not
// divide-by-zero, just return 0.
func TestCPUPercentNoDeltaReturnsZero(t *testing.T) {
	prev := CPUTimes{User: 100, Idle: 100}
	curr := prev // identical → no delta
	got := CPUPercent(prev, curr)
	if got != 0 {
		t.Fatalf("CPUPercent on no-delta=%.3f, want 0", got)
	}
}

// TestCPUPercentNonMonotonicReturnsZero suspend/resume can produce a
// counter going backwards. We want zero, not a wildly-negative or
// wraparound result.
func TestCPUPercentNonMonotonicReturnsZero(t *testing.T) {
	prev := CPUTimes{User: 500, Idle: 500}
	curr := CPUTimes{User: 100, Idle: 100} // counters decreased
	got := CPUPercent(prev, curr)
	if got != 0 {
		t.Fatalf("CPUPercent on non-monotonic=%.3f, want 0", got)
	}
}

// TestMemUsedPrefersAvailable confirms Used() takes Total-Available
// when MemAvailable is present (kernels ≥ 3.14, which is universal in
// the deployments wash targets).
func TestMemUsedPrefersAvailable(t *testing.T) {
	m := MemInfo{
		Total:     16 * 1024 * 1024 * 1024,
		Available: 10 * 1024 * 1024 * 1024,
		Free:      2 * 1024 * 1024 * 1024,
		Buffers:   1 * 1024 * 1024 * 1024,
		Cached:    1 * 1024 * 1024 * 1024,
	}
	want := uint64(6 * 1024 * 1024 * 1024) // 16 - 10
	if got := m.Used(); got != want {
		t.Fatalf("Used=%d, want %d (must prefer MemAvailable)", got, want)
	}
}

// TestMemUsedFallbackOnAncientKernel covers the (vanishing) case
// where MemAvailable is absent. Falls back to Total - Free - Buffers
// - Cached. Synthetic; primary path is the above.
func TestMemUsedFallbackOnAncientKernel(t *testing.T) {
	m := MemInfo{
		Total:   8 * 1024,
		Free:    2 * 1024,
		Buffers: 1 * 1024,
		Cached:  1 * 1024,
		// Available = 0 → falls through
	}
	if got := m.Used(); got != 4*1024 {
		t.Fatalf("Used=%d, want %d (fallback path)", got, 4*1024)
	}
}

// TestMemPercentZeroTotalIsZero a misconfigured cgroup / parse miss
// can leave Total at zero. Percent must not divide-by-zero.
func TestMemPercentZeroTotalIsZero(t *testing.T) {
	m := MemInfo{}
	if got := m.Percent(); got != 0 {
		t.Fatalf("Percent on zero Total=%.3f, want 0", got)
	}
}

// TestMemPercentRange spot-check that the fraction returned is the
// expected 0..100 percentage, not a 0..1 ratio.
func TestMemPercentRange(t *testing.T) {
	m := MemInfo{Total: 1000, Available: 250}
	// Used = 750, Percent = 75
	got := m.Percent()
	if math.Abs(got-75.0) > 0.001 {
		t.Fatalf("Percent=%.3f, want ~75.0", got)
	}
}

// TestReadStatReturnsLiveValues exercises the real /proc/stat reader.
// Skipped on non-Linux to keep CI green; on Linux the aggregate Total
// must be non-zero and per-cpu len >= 1.
func TestReadStatReturnsLiveValues(t *testing.T) {
	agg, per, err := ReadStat()
	if err != nil {
		t.Skipf("ReadStat unsupported here: %v", err)
	}
	if agg.Total() == 0 {
		t.Fatalf("aggregate Total=0, /proc/stat parse looks broken: %+v", agg)
	}
	if len(per) == 0 {
		t.Fatalf("per-cpu length 0, expected at least 1 core")
	}
}

// TestReadMemReturnsLiveValues exercises the real /proc/meminfo reader.
func TestReadMemReturnsLiveValues(t *testing.T) {
	m, err := ReadMem()
	if err != nil {
		t.Skipf("ReadMem unsupported here: %v", err)
	}
	if m.Total == 0 {
		t.Fatalf("MemTotal=0, parse looks broken: %+v", m)
	}
	if m.Used() > m.Total {
		t.Fatalf("Used=%d > Total=%d, impossible", m.Used(), m.Total)
	}
	if pct := m.Percent(); pct < 0 || pct > 100 {
		t.Fatalf("Percent out of range: %.3f", pct)
	}
}
