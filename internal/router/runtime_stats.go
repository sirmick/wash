// Runtime-stats collector for the About panel's process table.
//
// Each app's SDK ships EvtRuntimeStats on the event channel every
// few seconds (see internal/sdk/heartbeat.go). The router stashes
// the latest payload per instance in r.runtimeStats. The About app
// queries the full table via a synthetic-peer bus call addressed to
// com.wash.router (see syntheticRouterPeer).
//
// What the heartbeat carries: Go-internal facts only — NumGoroutine,
// MemStats subset, uptime. What the heartbeat deliberately doesn't
// carry: process-level RSS / VSize. Those come from /proc/<pid>/statm
// at query time, because the router already has the pid (Cmd.Process)
// and no app needs to learn about its own /proc state. A "total wash
// memory" footer is the sum of every row's RSS.
//
// Linux-only on the /proc side. wash-top already requires Linux, so
// the same constraint is fine here.

package router

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

// pageSize is the kernel's reported page size. /proc/<pid>/statm
// returns counts in pages; multiply by this to get bytes. Captured
// once at package init; the kernel doesn't change it for a running
// process.
var pageSize = uint64(syscall.Getpagesize())

// routerStart is the timestamp the router process began collecting
// stats — used so the synthetic "router" row in the table has an
// uptime independent of any app heartbeat. Set at package init,
// re-exec'd routers naturally reset it.
var routerStart = time.Now()

// runtimeStatsRecord is the per-instance entry the router keeps. The
// Go-runtime block is whatever the last heartbeat carried; lastSeen
// tracks staleness so a wedged BE can be flagged (or dropped) by the
// query path.
type runtimeStatsRecord struct {
	wire.EvtRuntimeStats
	lastSeen time.Time
}

// recordRuntimeStats is the inbound handler. Called from
// AppInstance.handleEvt when a runtime.stats frame arrives. Stores
// the latest snapshot keyed by instance id.
func (r *Router) recordRuntimeStats(instID string, evt wire.EvtRuntimeStats) {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	if r.runtimeStats == nil {
		r.runtimeStats = make(map[string]*runtimeStatsRecord)
	}
	r.runtimeStats[instID] = &runtimeStatsRecord{
		EvtRuntimeStats: evt,
		lastSeen:        time.Now(),
	}
}

// dropRuntimeStats removes an instance from the stats map. Called
// from tearDown on instance death so the table reflects only live
// processes.
func (r *Router) dropRuntimeStats(instID string) {
	r.statsMu.Lock()
	delete(r.runtimeStats, instID)
	r.statsMu.Unlock()
}

// runtimeRow is what the synthetic-peer query returns per process.
// JSON tags match the FE's expectation (snake_case wire convention).
//
// Three classes of row the FE renders differently:
//
//   - Heartbeat received, RuntimeKind "go": full row with Go-runtime
//     columns populated.
//   - Heartbeat received, non-Go kind: liveness confirmed (uptime,
//     last-seen) but Go-specific columns rendered "—" by the FE.
//     A future Rust/Python SDK lands in this bucket.
//   - HasHeartbeat false: instance is registered but has not yet
//     pinged (just-spawned, or doesn't ship the protocol yet).
//     The FE dashes out everything Go and the last-seen age.
type runtimeRow struct {
	// "router" for the router self-row; otherwise the instance id.
	InstanceID string `json:"instance_id"`
	// Empty for the router; the manifest id for app rows.
	AppID string `json:"app_id,omitempty"`
	Name  string `json:"name"`
	// PID is the kernel pid the row's stats came from. 0 if the
	// router lost it (in-process test apps).
	PID int `json:"pid"`

	// HasHeartbeat is true iff a runtime.stats frame has arrived
	// for this instance at least once. Lets the FE distinguish
	// "non-Go app live" from "process registered but silent".
	HasHeartbeat bool `json:"has_heartbeat"`
	// RuntimeKind echoes the heartbeat field ("go", "rust", …).
	// Empty for rows without a heartbeat. The FE renders Go-
	// specific columns only when this is "go".
	RuntimeKind string `json:"runtime_kind,omitempty"`

	// Go-runtime self-report. Zeroed when the heartbeat is non-Go
	// or when none has arrived. omitempty so the FE can `!field`-
	// check rather than guess at zero-vs-missing semantics.
	GoVersion    string `json:"go_version,omitempty"`
	NumGoroutine int    `json:"num_goroutine,omitempty"`
	NumCgoCall   int64  `json:"num_cgo_call,omitempty"`
	HeapAlloc    uint64 `json:"heap_alloc,omitempty"`
	HeapInuse    uint64 `json:"heap_inuse,omitempty"`
	HeapObjects  uint64 `json:"heap_objects,omitempty"`
	StackInuse   uint64 `json:"stack_inuse,omitempty"`
	Sys          uint64 `json:"sys,omitempty"`
	NumGC        uint32 `json:"num_gc,omitempty"`
	GCPauseTotal uint64 `json:"gc_pause_total_ns,omitempty"`

	// UptimeSec is universal — every heartbeat carries it. Zero
	// when no heartbeat has arrived; the router-self-row reads it
	// from its own routerStart.
	UptimeSec int64 `json:"uptime_sec,omitempty"`

	// Process-level memory from /proc/<pid>/statm. The "what htop
	// shows you" number, language-agnostic. Zero on read failure
	// (pid gone, /proc unreadable) — the FE renders "—".
	RSSBytes   uint64 `json:"rss_bytes,omitempty"`
	VSizeBytes uint64 `json:"vsize_bytes,omitempty"`

	// LastSeenAgeSec — seconds since the last heartbeat. Only
	// meaningful when HasHeartbeat is true. Zero for the router
	// self-row (it's always "now" in its own frame).
	LastSeenAgeSec int64 `json:"last_seen_age_sec,omitempty"`
}

// runtimeTable is the bus reply payload. Rows are sorted by name so
// the About table is stable across renders.
type runtimeTable struct {
	// Rows: every running app instance plus a synthetic router row.
	Rows []runtimeRow `json:"rows"`
	// TotalRSS is the sum of every row's RSS — "how much memory is
	// wash using on this box". Convenience for the FE so it doesn't
	// re-sum on every render.
	TotalRSS uint64 `json:"total_rss"`
	// CapturedAtSec is the unix second this snapshot was assembled.
	// Lets the FE label "as of HH:MM:SS" without trusting its clock.
	CapturedAtSec int64 `json:"captured_at_sec"`
}

// collectRuntimeTable assembles the snapshot the synthetic-peer
// query returns. Walks every live AppInstance, joins with the
// heartbeat map, reads /proc/<pid>/statm for RSS, and tacks on the
// router's own row at the top.
func (r *Router) collectRuntimeTable() runtimeTable {
	// Snapshot the live instances under r.mu, then drop the lock
	// before touching /proc — the statm read can stall under very
	// rare cgroup-freezer cases and we don't want to block the
	// router's main map while it does.
	type liveApp struct {
		instID, appID, name string
		pid                 int
	}
	r.mu.Lock()
	live := make([]liveApp, 0, len(r.apps))
	for _, inst := range r.apps {
		name := inst.AppID
		pid := 0
		if inst.Manifest != nil && inst.Manifest.Name != "" {
			name = inst.Manifest.Name
		}
		if inst.Cmd != nil && inst.Cmd.Process != nil {
			pid = inst.Cmd.Process.Pid
		}
		live = append(live, liveApp{
			instID: inst.InstanceID,
			appID:  inst.AppID,
			name:   name,
			pid:    pid,
		})
	}
	r.mu.Unlock()

	r.statsMu.Lock()
	statsCopy := make(map[string]*runtimeStatsRecord, len(r.runtimeStats))
	for k, v := range r.runtimeStats {
		statsCopy[k] = v
	}
	r.statsMu.Unlock()

	now := time.Now()
	rows := make([]runtimeRow, 0, len(live)+1)

	// Router self-row first. PID = os.Getpid(); Go-runtime self-
	// read via runtime.ReadMemStats here (no heartbeat for the
	// router itself, it's the collector).
	rows = append(rows, r.routerSelfRow(now))

	for _, la := range live {
		row := runtimeRow{
			InstanceID: la.instID,
			AppID:      la.appID,
			Name:       la.name,
			PID:        la.pid,
		}
		if rec, ok := statsCopy[la.instID]; ok {
			row.HasHeartbeat = true
			// Default kind to "go" for pre-RuntimeKind binaries
			// (they predate the field but were always Go). New
			// non-Go SDKs MUST set the kind explicitly.
			row.RuntimeKind = rec.RuntimeKind
			if row.RuntimeKind == "" {
				row.RuntimeKind = "go"
			}
			row.UptimeSec = rec.UptimeSec
			row.LastSeenAgeSec = int64(now.Sub(rec.lastSeen).Seconds())
			// Go-specific block: copy only when kind says go.
			// Non-Go SDKs leave these zero on the wire; the
			// FE keys off RuntimeKind anyway, but skipping the
			// copy keeps the JSON clean (omitempty drops them).
			if row.RuntimeKind == "go" {
				row.GoVersion = rec.GoVersion
				row.NumGoroutine = rec.NumGoroutine
				row.NumCgoCall = rec.NumCgoCall
				row.HeapAlloc = rec.HeapAlloc
				row.HeapInuse = rec.HeapInuse
				row.HeapObjects = rec.HeapObjects
				row.StackInuse = rec.StackInuse
				row.Sys = rec.Sys
				row.NumGC = rec.NumGC
				row.GCPauseTotal = rec.GCPauseTotal
			}
		}
		if la.pid > 0 {
			rss, vsize := readStatm(la.pid)
			row.RSSBytes = rss
			row.VSizeBytes = vsize
		}
		rows = append(rows, row)
	}

	// Stable sort: router first, then by name ascending. Avoids
	// in-place sort.Slice import for the trivial case.
	sortRowsRouterFirstByName(rows)

	var total uint64
	for _, row := range rows {
		total += row.RSSBytes
	}
	return runtimeTable{
		Rows:          rows,
		TotalRSS:      total,
		CapturedAtSec: now.Unix(),
	}
}

// routerSelfRow synthesizes the table row for the router itself.
// No heartbeat (the router is the consumer of heartbeats), so we
// read MemStats directly here. PID is os.Getpid(); RSS/VSize from
// /proc/self/statm.
func (r *Router) routerSelfRow(now time.Time) runtimeRow {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	pid := os.Getpid()
	rss, vsize := readStatm(pid)
	return runtimeRow{
		InstanceID:   "router",
		Name:         "wash-router",
		PID:          pid,
		HasHeartbeat: true,
		RuntimeKind:  "go",
		GoVersion:    runtime.Version(),
		NumGoroutine: runtime.NumGoroutine(),
		NumCgoCall:   runtime.NumCgoCall(),
		HeapAlloc:    m.HeapAlloc,
		HeapInuse:    m.HeapInuse,
		HeapObjects:  m.HeapObjects,
		StackInuse:   m.StackInuse,
		Sys:          m.Sys,
		NumGC:        m.NumGC,
		GCPauseTotal: m.PauseTotalNs,
		UptimeSec:    int64(now.Sub(routerStart).Seconds()),
		RSSBytes:     rss,
		VSizeBytes:   vsize,
	}
}

// readStatm parses /proc/<pid>/statm and returns (RSS, VSize) in
// bytes. The file format is a single line of space-separated integers
// in pages: "size resident shared text lib data dt". We use size for
// VSize and resident for RSS. Returns (0, 0) on any read or parse
// failure — caller renders "—".
//
// statm over status because it's a single short line of integers
// vs. a multiline kv parse; the values we want are in the first two
// columns and the read is sub-microsecond.
func readStatm(pid int) (rss uint64, vsize uint64) {
	path := filepath.Join("/proc", strconv.Itoa(pid), "statm")
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	if !scan.Scan() {
		return 0, 0
	}
	fields := strings.Fields(scan.Text())
	if len(fields) < 2 {
		return 0, 0
	}
	sizePages, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, 0
	}
	residentPages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, 0
	}
	return residentPages * pageSize, sizePages * pageSize
}

// sortRowsRouterFirstByName sorts in place: the router self-row at
// index 0, then app rows alphabetical by Name. Insertion sort is
// fine — we have ~15 rows max.
func sortRowsRouterFirstByName(rows []runtimeRow) {
	// Move the router row to position 0 if it's not already there.
	for i, r := range rows {
		if r.InstanceID == "router" && i != 0 {
			rows[0], rows[i] = rows[i], rows[0]
			break
		}
	}
	// Insertion-sort the tail (indices 1..end) by Name.
	for i := 2; i < len(rows); i++ {
		for j := i; j > 1 && rows[j-1].Name > rows[j].Name; j-- {
			rows[j-1], rows[j] = rows[j], rows[j-1]
		}
	}
}

// statsMu / runtimeStats are declared on the Router struct in
// router.go but the lifecycle (record/drop) is owned by this file —
// the router's other code paths don't touch the map directly.
