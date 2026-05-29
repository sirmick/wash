// Package proc reads pure-stdlib snapshots of /proc/{stat,meminfo} and
// computes derived values (CPU busy %, memory used %) shared between
// wash-top (which polls the full process table) and wash-session
// (which only needs ambient host load for the sidebar's About widget).
//
// Scope is deliberately narrow: CPU aggregate + memory only. The per-
// process walk, network counters, disk counters, and load averages
// remain in apps/top/be/proc.go — wash-top is the sole consumer.
package proc

import (
	"os"
	"strconv"
	"strings"
)

// CPUTimes is the aggregate row from /proc/stat. All fields are
// jiffies (USER_HZ ticks), not seconds. The cumulative values are
// meaningful only as differences across two snapshots.
type CPUTimes struct {
	User    uint64
	Nice    uint64
	System  uint64
	Idle    uint64
	IOWait  uint64
	IRQ     uint64
	SoftIRQ uint64
	Steal   uint64
}

// Total returns the sum of every field — the denominator for any
// "fraction of CPU time" calculation.
func (c CPUTimes) Total() uint64 {
	return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
}

// Busy returns the non-idle, non-iowait time. Subtracting iowait
// matches htop's default — a process blocked on disk is not the CPU
// being busy with it.
func (c CPUTimes) Busy() uint64 {
	return c.Total() - c.Idle - c.IOWait
}

// ReadStat parses /proc/stat. Returns the aggregate "cpu " line and
// per-core "cpu0".."cpuN" lines, in order. Callers that don't need
// per-core can discard the second return.
func ReadStat() (CPUTimes, []CPUTimes, error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return CPUTimes{}, nil, err
	}
	var agg CPUTimes
	var per []CPUTimes
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "cpu") {
			// /proc/stat puts cpu lines first; once we hit a non-cpu
			// line (intr, ctxt, btime, …) we can stop.
			break
		}
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		c := CPUTimes{}
		c.User, _ = strconv.ParseUint(fields[1], 10, 64)
		c.Nice, _ = strconv.ParseUint(fields[2], 10, 64)
		c.System, _ = strconv.ParseUint(fields[3], 10, 64)
		c.Idle, _ = strconv.ParseUint(fields[4], 10, 64)
		c.IOWait, _ = strconv.ParseUint(fields[5], 10, 64)
		c.IRQ, _ = strconv.ParseUint(fields[6], 10, 64)
		c.SoftIRQ, _ = strconv.ParseUint(fields[7], 10, 64)
		if len(fields) > 8 {
			c.Steal, _ = strconv.ParseUint(fields[8], 10, 64)
		}
		if fields[0] == "cpu" {
			agg = c
		} else {
			per = append(per, c)
		}
	}
	return agg, per, nil
}

// CPUPercent computes the busy fraction (0..100) between two
// snapshots. Returns 0 when there's no measurable delta — protects
// callers from divide-by-zero and from the spurious "100% busy" you'd
// get if the kernel briefly reported a non-monotonic counter (rare
// but documented around suspend/resume).
//
// The first call after process start has no prior baseline; pass a
// zero CPUTimes and discard the result.
func CPUPercent(prev, curr CPUTimes) float64 {
	totalDelta := curr.Total() - prev.Total()
	if totalDelta == 0 || curr.Total() < prev.Total() {
		return 0
	}
	busyDelta := curr.Busy() - prev.Busy()
	if curr.Busy() < prev.Busy() {
		return 0
	}
	return float64(busyDelta) / float64(totalDelta) * 100.0
}
