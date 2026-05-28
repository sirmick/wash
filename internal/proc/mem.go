package proc

import (
	"os"
	"strconv"
	"strings"
)

// MemInfo holds the fields we care about from /proc/meminfo. Values
// are bytes — the file reports kB, we promote so consumers don't
// have to know the unit.
type MemInfo struct {
	Total     uint64
	Available uint64
	Free      uint64
	Buffers   uint64
	Cached    uint64
	SwapTotal uint64
	SwapFree  uint64
}

// ReadMem parses /proc/meminfo. Missing keys leave the corresponding
// field zero; we don't fail on absence because kernels vary.
func ReadMem() (MemInfo, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return MemInfo{}, err
	}
	m := MemInfo{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		val, _ := strconv.ParseUint(fields[1], 10, 64)
		val *= 1024
		switch key {
		case "MemTotal":
			m.Total = val
		case "MemAvailable":
			m.Available = val
		case "MemFree":
			m.Free = val
		case "Buffers":
			m.Buffers = val
		case "Cached":
			m.Cached = val
		case "SwapTotal":
			m.SwapTotal = val
		case "SwapFree":
			m.SwapFree = val
		}
	}
	return m, nil
}

// Used returns the number of bytes of memory in use — Total minus
// Available. MemAvailable is the kernel's own estimate of "could be
// used by a new allocation without swapping", which is the number a
// status widget should display.
//
// Falls back to (Total - Free - Buffers - Cached) on kernels too old
// to expose MemAvailable (pre-3.14, vanishingly rare on anything wash
// would deploy onto in 2026 but covered for completeness).
func (m MemInfo) Used() uint64 {
	if m.Available != 0 && m.Total > m.Available {
		return m.Total - m.Available
	}
	if m.Total > m.Free+m.Buffers+m.Cached {
		return m.Total - m.Free - m.Buffers - m.Cached
	}
	return 0
}

// Percent returns the used-memory fraction (0..100). 0 when Total is
// 0 (a misconfigured cgroup or a parse failure).
func (m MemInfo) Percent() float64 {
	if m.Total == 0 {
		return 0
	}
	return float64(m.Used()) / float64(m.Total) * 100.0
}
