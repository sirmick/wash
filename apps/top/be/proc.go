// /proc reader for wash-top.
//
// All pure stdlib, no cgo. Sampler holds the previous-tick jiffies so
// per-pid %CPU is a diff, not an "average since boot" lie. One sampler
// per BE process; not safe for concurrent calls (the snapshot loop is
// single-threaded by design).
//
// Math: %cpu = (Δutime+Δstime) / Δtotal_cpu_jiffies * numCPUs * 100.
// htop's default Irix-mode behavior — a multithreaded process pinning
// two cores reads ~200%. Easier to reason about than Solaris mode.

package top

import (
	"bytes"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/proc"
)

// pageSize and clockTicks are populated once at startup; both come from
// the kernel and never change. The hardcoded clockTicks=100 matches
// every Linux on amd64/arm64; rare archs that differ would need a
// sysconf call that requires cgo, which we don't ship.
var (
	pageSize   = int64(os.Getpagesize())
	clockTicks = int64(100)
)

// LoadAvg is /proc/loadavg's first three numbers + the running/total
// from the fourth.
type LoadAvg struct {
	L1, L5, L15      float64
	Running, Threads uint32
}

// NetIface is one row of /proc/net/dev's cumulative RX/TX byte
// counters. Diffs across two samples give bytes/s. Real determines
// whether this iface contributes to the aggregate (vs. only showing
// in the popover); see realNetIface.
type NetIface struct {
	Name           string
	RxBytes, TxBytes uint64
	Real           bool
}

// DiskDev is one row of /proc/diskstats's cumulative sector
// counters, in bytes (sectors × 512). Whole drives only — partitions
// are filtered out via /sys/block/ membership. Real flags real
// physical/virtual drives (sd*, vd*, nvme*, mmcblk*) so the aggregate
// excludes dm-* and we don't double-count LVM/dmcrypt mappers.
type DiskDev struct {
	Name                  string
	ReadBytes, WriteBytes uint64
	Real                  bool
}

// ProcInfo is one process row. Built per tick; not retained across
// ticks. CPU is the computed %CPU (Irix mode) since the previous
// sample — 0 on the very first snapshot.
type ProcInfo struct {
	PID, PPID int32
	UID       int32
	User      string
	State     string // single-letter R/S/D/Z/T/I
	Nice      int32
	Threads   int32
	StartUnix int64
	RSS       uint64 // bytes
	VSize     uint64
	CPU       float64
	MemPct    float64
	TimeJiff  uint64 // accumulated utime+stime in jiffies (for next-tick diff)
	Comm      string
	Cmd       string // full /proc/<pid>/cmdline (or "[comm]" for kthreads)
}

// procSample is the previous-tick state we hold for each pid to
// compute deltas. Stored on the Sampler.
type procSample struct {
	timeJiff uint64
}

// Sampler holds previous-tick totals so subsequent snapshots can diff
// against them. First call returns zero %CPU values (no baseline yet).
type Sampler struct {
	bootUnix int64

	// userCache memoizes uid → username; getent is slow at htop-scale.
	userCache map[int32]string

	prevCPU   proc.CPUTimes
	prevProcs map[int32]procSample
	havePrev  bool
}

// NewSampler builds a sampler primed with the system's boot time, used
// to convert /proc/<pid>/stat's starttime (jiffies since boot) to a
// wall-clock unix seconds value.
func NewSampler() *Sampler {
	return &Sampler{
		bootUnix:  readBootTime(),
		userCache: map[int32]string{},
		prevProcs: map[int32]procSample{},
	}
}

// Snapshot collects one tick worth of data: cpu totals, per-core,
// meminfo, loadavg, and the process table. Returns a fully-populated
// snapshot ready to ship.
func (s *Sampler) Snapshot() (Snapshot, error) {
	var snap Snapshot
	snap.TS = time.Now().Unix()
	snap.CPUCount = runtime.NumCPU()

	cpu, perCPU, err := proc.ReadStat()
	if err != nil {
		return snap, fmt.Errorf("stat: %w", err)
	}
	snap.CPU = cpuRowFrom(cpu)
	snap.PerCPU = make([]cpuRow, len(perCPU))
	for i, c := range perCPU {
		snap.PerCPU[i] = cpuRowFrom(c)
	}

	mem, _ := proc.ReadMem()
	snap.Mem = memRow{
		Total: mem.Total, Available: mem.Available, Free: mem.Free,
		Buffers: mem.Buffers, Cached: mem.Cached,
		SwapTotal: mem.SwapTotal, SwapFree: mem.SwapFree,
	}

	if la, err := readLoadAvg(); err == nil {
		snap.Load = loadRow{
			L1: la.L1, L5: la.L5, L15: la.L15,
			Running: la.Running, Threads: la.Threads,
		}
	}

	// Net + disk. Cumulative counters; the FE diffs against the
	// previous snapshot to render bytes/s, exactly like CPU jiffies.
	for _, n := range readNetIfaces() {
		snap.PerNet = append(snap.PerNet, netDev{
			Name: n.Name, RxBytes: n.RxBytes, TxBytes: n.TxBytes, Real: n.Real,
		})
		if n.Real {
			snap.Net.RxBytes += n.RxBytes
			snap.Net.TxBytes += n.TxBytes
		}
	}
	for _, d := range readDiskDevs() {
		snap.PerDisk = append(snap.PerDisk, diskDev{
			Name: d.Name, ReadBytes: d.ReadBytes, WriteBytes: d.WriteBytes, Real: d.Real,
		})
		if d.Real {
			snap.Disk.ReadBytes += d.ReadBytes
			snap.Disk.WriteBytes += d.WriteBytes
		}
	}

	// CPU delta denominator for %CPU. First call → 0 (no baseline);
	// the FE shows 0% for the first tick, then real values forward.
	var totalDelta uint64
	if s.havePrev && cpu.Total() > s.prevCPU.Total() {
		totalDelta = cpu.Total() - s.prevCPU.Total()
	}

	procs, nextProcs := s.readProcs(totalDelta, mem.Total)
	snap.Procs = procs

	s.prevCPU = cpu
	s.prevProcs = nextProcs
	s.havePrev = true
	return snap, nil
}

func cpuRowFrom(c proc.CPUTimes) cpuRow {
	return cpuRow{
		User: c.User, Nice: c.Nice, System: c.System, Idle: c.Idle,
		IOWait: c.IOWait, IRQ: c.IRQ, SoftIRQ: c.SoftIRQ, Steal: c.Steal,
	}
}

// readProcs walks /proc/[0-9]* and builds the process list. Returns
// both the visible rows AND the next-tick sample map so the caller
// can roll the state forward in one shot.
func (s *Sampler) readProcs(totalDelta uint64, memTotal uint64) ([]ProcInfo, map[int32]procSample) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, s.prevProcs
	}
	out := make([]ProcInfo, 0, 256)
	next := make(map[int32]procSample, len(s.prevProcs))
	numCPU := uint64(runtime.NumCPU())
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid64, err := strconv.ParseInt(e.Name(), 10, 32)
		if err != nil {
			continue
		}
		pid := int32(pid64)
		pi, ok := s.readOneProc(pid, memTotal)
		if !ok {
			continue
		}
		if prev, hadPrev := s.prevProcs[pid]; hadPrev && totalDelta > 0 && pi.TimeJiff > prev.timeJiff {
			delta := pi.TimeJiff - prev.timeJiff
			// Irix mode: percentage of one core. A two-thread process
			// pinning two cores reads ~200%.
			pi.CPU = float64(delta) * float64(numCPU) * 100.0 / float64(totalDelta)
		}
		next[pid] = procSample{timeJiff: pi.TimeJiff}
		out = append(out, pi)
	}
	return out, next
}

// readOneProc reads /proc/<pid>/{stat,status,cmdline} and assembles a
// ProcInfo. Returns (_, false) on any IO error (pid likely exited mid-
// walk — completely normal, not worth logging).
func (s *Sampler) readOneProc(pid int32, memTotal uint64) (ProcInfo, bool) {
	pi := ProcInfo{PID: pid}
	base := "/proc/" + strconv.Itoa(int(pid))

	statBytes, err := os.ReadFile(base + "/stat")
	if err != nil {
		return pi, false
	}
	if err := parseStat(statBytes, &pi); err != nil {
		return pi, false
	}

	if uid, ok := readStatusUID(base + "/status"); ok {
		pi.UID = uid
		pi.User = s.lookupUser(uid)
	}

	if b, err := os.ReadFile(base + "/cmdline"); err == nil && len(b) > 0 {
		b = bytes.TrimRight(b, "\x00")
		for i, c := range b {
			if c == 0 {
				b[i] = ' '
			}
		}
		pi.Cmd = string(b)
	} else {
		pi.Cmd = "[" + pi.Comm + "]"
	}

	pi.RSS *= uint64(pageSize)
	if memTotal > 0 {
		pi.MemPct = float64(pi.RSS) / float64(memTotal) * 100.0
	}
	// parseStat left StartUnix as "seconds since boot"; promote to
	// wall-clock seconds now that we know bootUnix.
	if s.bootUnix > 0 {
		pi.StartUnix += s.bootUnix
	}
	return pi, true
}

// parseStat parses /proc/<pid>/stat. The "comm" field (2) is wrapped
// in parens AND may contain spaces / parens of its own, so we cut
// strictly at the LAST ')' before parsing the rest space-delimited.
//
// Field indexing post-comm is 0-based in `rest`, so rest[0] is state
// (proc(5)'s field 3), rest[1] is ppid (field 4), etc.
func parseStat(b []byte, pi *ProcInfo) error {
	open := bytes.IndexByte(b, '(')
	end := bytes.LastIndexByte(b, ')')
	if open < 0 || end < 0 || end < open {
		return fmt.Errorf("malformed stat")
	}
	pi.Comm = string(b[open+1 : end])
	rest := strings.Fields(string(b[end+2:]))
	// rest[0]=state, [1]=ppid, [11]=utime (14), [12]=stime (15),
	// [17]=nice (20), [18]=num_threads (21), [20]=starttime (22),
	// [21]=vsize (23), [22]=rss-pages (24).
	if len(rest) < 23 {
		return fmt.Errorf("short stat: %d", len(rest))
	}
	pi.State = rest[0]
	if v, err := strconv.ParseInt(rest[1], 10, 32); err == nil {
		pi.PPID = int32(v)
	}
	utime, _ := strconv.ParseUint(rest[11], 10, 64)
	stime, _ := strconv.ParseUint(rest[12], 10, 64)
	pi.TimeJiff = utime + stime
	if v, err := strconv.ParseInt(rest[17], 10, 32); err == nil {
		pi.Nice = int32(v)
	}
	if v, err := strconv.ParseInt(rest[18], 10, 32); err == nil {
		pi.Threads = int32(v)
	}
	if v, err := strconv.ParseUint(rest[21], 10, 64); err == nil {
		pi.VSize = v
	}
	if v, err := strconv.ParseUint(rest[22], 10, 64); err == nil {
		pi.RSS = v // pages; promoted to bytes by readOneProc
	}
	if v, err := strconv.ParseUint(rest[20], 10, 64); err == nil && clockTicks > 0 {
		pi.StartUnix = int64(v) / clockTicks // seconds-since-boot; bootUnix added later
	}
	return nil
}

// readStatusUID reads "Uid: real eff saved fs" out of /proc/<pid>/status.
// Returns the real uid.
func readStatusUID(path string) (int32, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				if v, err := strconv.ParseInt(f[1], 10, 32); err == nil {
					return int32(v), true
				}
			}
		}
	}
	return 0, false
}

// lookupUser memoizes uid → name. Falls back to the decimal uid on
// lookup failure; misbehaving name services aren't worth blocking the
// snapshot on twice.
func (s *Sampler) lookupUser(uid int32) string {
	if name, ok := s.userCache[uid]; ok {
		return name
	}
	u, err := user.LookupId(strconv.Itoa(int(uid)))
	if err != nil {
		s.userCache[uid] = strconv.Itoa(int(uid))
		return s.userCache[uid]
	}
	s.userCache[uid] = u.Username
	return u.Username
}

// readLoadAvg parses /proc/loadavg.
func readLoadAvg() (LoadAvg, error) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return LoadAvg{}, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 4 {
		return LoadAvg{}, fmt.Errorf("short loadavg")
	}
	la := LoadAvg{}
	la.L1, _ = strconv.ParseFloat(fields[0], 64)
	la.L5, _ = strconv.ParseFloat(fields[1], 64)
	la.L15, _ = strconv.ParseFloat(fields[2], 64)
	if i := strings.IndexByte(fields[3], '/'); i > 0 {
		r, _ := strconv.ParseUint(fields[3][:i], 10, 32)
		t, _ := strconv.ParseUint(fields[3][i+1:], 10, 32)
		la.Running = uint32(r)
		la.Threads = uint32(t)
	}
	return la, nil
}

// readNetIfaces parses /proc/net/dev. Each row's Real flag tells the
// caller whether to fold this iface's counters into the aggregate.
// "Real" = backed by a physical device (the standard
// /sys/class/net/<name>/device-symlink heuristic); virtual ifaces
// (loopback, docker0, br-*, virbr*, veth*, tun*, tap*, wg*) get
// Real=false and only show up in the popover.
func readNetIfaces() []NetIface {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil
	}
	lines := strings.Split(string(b), "\n")
	// First two lines are the header.
	if len(lines) < 3 {
		return nil
	}
	out := make([]NetIface, 0, len(lines)-2)
	for _, line := range lines[2:] {
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		fields := strings.Fields(line[i+1:])
		// fields[0] = rx_bytes, [8] = tx_bytes (proc(5) "/proc/net/dev").
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		out = append(out, NetIface{
			Name: name, RxBytes: rx, TxBytes: tx, Real: realNetIface(name),
		})
	}
	return out
}

// realNetIface returns true iff the interface is backed by a physical
// device. The kernel exposes a /sys/class/net/<name>/device symlink
// only for physical NICs; loopback, bridges, veth, tun/tap, and wg
// have no device symlink. Cheap and robust.
func realNetIface(name string) bool {
	if name == "lo" {
		return false
	}
	if _, err := os.Stat("/sys/class/net/" + name + "/device"); err != nil {
		return false
	}
	return true
}

// readDiskDevs parses /proc/diskstats and returns whole-disk rows
// only — partitions live under /sys/block/<parent>/<part>/ and are
// skipped via the /sys/block/ membership test. Sector counts (fields
// 6 and 10) are promoted to bytes (×512, the fixed Linux block size).
//
// Real flags physical/virtual disks (sd*, vd*, nvme*, mmcblk*, hd*)
// — the aggregate excludes dm-* and similar so a user with LVM or
// LUKS doesn't see the same bytes counted twice (mapper + underlying
// disk both report I/O).
func readDiskDevs() []DiskDev {
	b, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return nil
	}
	out := make([]DiskDev, 0)
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		// Format: major minor name reads merged sectors_read ms_read
		// writes merged_w sectors_written ms_writes ios_in_progress
		// io_ms weighted_io_ms (+ trimsectors etc. on newer kernels).
		if len(fields) < 11 {
			continue
		}
		name := fields[2]
		// loop/ram are uninteresting and noisy on dev boxes; skip
		// before the /sys check so we don't even allocate the row.
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
			continue
		}
		// Whole-disk membership in /sys/block/ — partitions live one
		// level deeper, so this single test rejects them all.
		if _, err := os.Stat("/sys/block/" + name); err != nil {
			continue
		}
		readSectors, _ := strconv.ParseUint(fields[5], 10, 64)
		writeSectors, _ := strconv.ParseUint(fields[9], 10, 64)
		out = append(out, DiskDev{
			Name:       name,
			ReadBytes:  readSectors * 512,
			WriteBytes: writeSectors * 512,
			Real:       realDisk(name),
		})
	}
	return out
}

// realDisk returns true for physical/virtual disks the user thinks of
// as "the disk". dm-* (LVM, dmcrypt) and md* (software RAID) are
// stacked atop real disks and report the same I/O — including them
// in the aggregate would double-count.
func realDisk(name string) bool {
	for _, p := range []string{"sd", "vd", "hd", "nvme", "mmcblk", "xvd"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// readBootTime returns the system boot wall-clock seconds from
// /proc/stat's "btime" line. Used once at sampler init.
func readBootTime() int64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "btime ") {
			if v, err := strconv.ParseInt(strings.TrimSpace(line[6:]), 10, 64); err == nil {
				return v
			}
		}
	}
	return 0
}

// ---- Details (click-to-inspect) -----------------------------------

// Details is the payload for top.details_ok. Strings rather than typed
// fields so a future kernel adding a /proc/<pid>/foo line shows up
// without a parser change.
type Details struct {
	PID      int32             `json:"pid"`
	Found    bool              `json:"found"`
	Comm     string            `json:"comm,omitempty"`
	Cmd      string            `json:"cmd,omitempty"`
	Exe      string            `json:"exe,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	Status   map[string]string `json:"status,omitempty"`
	IO       map[string]string `json:"io,omitempty"`
	Limits   []limitRow        `json:"limits,omitempty"`
	Cgroup   string            `json:"cgroup,omitempty"`
	FDs      int32             `json:"fds,omitempty"`
	Wchan    string            `json:"wchan,omitempty"`
	OOMScore string            `json:"oom_score,omitempty"`
}

type limitRow struct {
	Name string `json:"name"`
	Soft string `json:"soft"`
	Hard string `json:"hard"`
	Unit string `json:"unit,omitempty"`
}

// readDetails gathers everything under /proc/<pid>/ that's useful for
// the inspect panel. Per-field errors are swallowed: a denied or
// missing file is shown as omitted, not an error pop-up.
func readDetails(pid int32) Details {
	d := Details{PID: pid}
	base := "/proc/" + strconv.Itoa(int(pid))
	if _, err := os.Stat(base); err != nil {
		return d
	}
	d.Found = true

	if b, err := os.ReadFile(base + "/status"); err == nil {
		d.Status = map[string]string{}
		for _, line := range strings.Split(string(b), "\n") {
			i := strings.IndexByte(line, ':')
			if i <= 0 {
				continue
			}
			k := line[:i]
			v := strings.TrimSpace(line[i+1:])
			d.Status[k] = v
			if k == "Name" {
				d.Comm = v
			}
		}
	}
	if b, err := os.ReadFile(base + "/cmdline"); err == nil && len(b) > 0 {
		b = bytes.TrimRight(b, "\x00")
		for i, c := range b {
			if c == 0 {
				b[i] = ' '
			}
		}
		d.Cmd = string(b)
	}
	if t, err := os.Readlink(base + "/exe"); err == nil {
		d.Exe = t
	}
	if t, err := os.Readlink(base + "/cwd"); err == nil {
		d.Cwd = t
	}
	if b, err := os.ReadFile(base + "/io"); err == nil {
		d.IO = map[string]string{}
		for _, line := range strings.Split(string(b), "\n") {
			i := strings.IndexByte(line, ':')
			if i <= 0 {
				continue
			}
			d.IO[line[:i]] = strings.TrimSpace(line[i+1:])
		}
	}
	if b, err := os.ReadFile(base + "/limits"); err == nil {
		d.Limits = parseLimits(b)
	}
	if b, err := os.ReadFile(base + "/cgroup"); err == nil {
		d.Cgroup = strings.TrimSpace(string(b))
	}
	if entries, err := os.ReadDir(base + "/fd"); err == nil {
		d.FDs = int32(len(entries))
	}
	if b, err := os.ReadFile(base + "/wchan"); err == nil {
		d.Wchan = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(base + "/oom_score"); err == nil {
		d.OOMScore = strings.TrimSpace(string(b))
	}
	return d
}

// parseLimits chews /proc/<pid>/limits, which is column-aligned text:
//
//   Limit                     Soft Limit           Hard Limit           Units
//   Max cpu time              unlimited            unlimited            seconds
//
// The fixed-column widths (26, 47, 68) are baked into fs/proc/base.c
// and haven't changed in over 15 years.
func parseLimits(b []byte) []limitRow {
	lines := strings.Split(string(b), "\n")
	if len(lines) < 2 {
		return nil
	}
	out := make([]limitRow, 0, len(lines)-1)
	for _, line := range lines[1:] {
		if len(line) < 68 {
			continue
		}
		row := limitRow{
			Name: strings.TrimSpace(line[:26]),
			Soft: strings.TrimSpace(line[26:47]),
			Hard: strings.TrimSpace(line[47:68]),
		}
		if len(line) > 68 {
			row.Unit = strings.TrimSpace(line[68:])
		}
		out = append(out, row)
	}
	return out
}
