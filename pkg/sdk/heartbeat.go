// Runtime-stats heartbeat: every app's SDK ticks Go-runtime self-
// report to the router via EvtRuntimeStats on channel 1. The router
// keeps a per-instance map for the About panel's process table.
//
// Why SDK and not per-app: every wash app gets this for free, no
// wiring required by app authors. The heartbeat is a single
// goroutine started in Run after the connection handshakes; it
// exits when the context cancels or the transport closes.
//
// What rides along: Go-internal facts only (NumGoroutine, MemStats
// subset, uptime). Process-level RSS / VSize are NOT shipped here —
// the router derives them from /proc/<pid>/statm at query time,
// since it already has the pid and the app doesn't need to read its
// own /proc state. See router.runtimeRowsLocked.

package sdk

import (
	"context"
	"log"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

// HeartbeatInterval is the default cadence between runtime.stats
// emissions. ~5s is the goldilocks: cheap (one small JSON frame),
// responsive enough that the About table feels live without bloating
// the apps' own goroutine count with timer noise.
//
// Override via WASH_RUNTIME_STATS_INTERVAL=<seconds> (small integer);
// "0" disables the heartbeat entirely — useful for tests that don't
// want the periodic frame interfering with frame-counting assertions.
const HeartbeatInterval = 5 * time.Second

// heartbeatStart is captured per process so the uptime field
// reflects "since this Go runtime began", not wall-clock or router
// spawn time. Set on package init so a slow app startup doesn't
// confuse the number on the first tick.
var heartbeatStart = time.Now()

// startHeartbeat launches the per-conn ticker goroutine. Cancels on
// ctx done. Errors writing the frame are logged once (via log
// inside writeEvt's path) and the loop continues — a transient
// write failure shouldn't kill the app.
func (c *Conn) startHeartbeat(ctx context.Context) {
	interval := resolveHeartbeatInterval()
	if interval <= 0 {
		return // disabled
	}
	// Emit once immediately so the router's table has data without
	// waiting a full interval after handshake.
	_ = c.writeEvt(buildRuntimeStats(c.def))
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := c.writeEvt(buildRuntimeStats(c.def)); err != nil {
					// Usually just the conn closing under us, but a
					// write failure on a live conn means About-panel
					// stats silently freeze — one line so the stop is
					// attributable either way.
					log.Printf("sdk: heartbeat stopped: %v", err)
					return
				}
			}
		}
	}()
}

// resolveHeartbeatInterval reads WASH_RUNTIME_STATS_INTERVAL (seconds)
// with a fallback to the constant default. "0" disables.
func resolveHeartbeatInterval() time.Duration {
	v := os.Getenv("WASH_RUNTIME_STATS_INTERVAL")
	if v == "" {
		return HeartbeatInterval
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return HeartbeatInterval
	}
	return time.Duration(n) * time.Second
}

// buildRuntimeStats reads the current MemStats + goroutine count and
// returns the wire payload. Cheap: ReadMemStats is documented as
// stop-the-world but bounded; on a small Go process it's microseconds.
//
// GoVersion is included because the router itself doesn't know which
// Go toolchain built each app binary — apps in a heterogeneous build
// (e.g. one shim cross-built) may differ.
func buildRuntimeStats(def *AppDef) wire.EvtRuntimeStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return wire.EvtRuntimeStats{
		T:            wire.TEvtRuntimeStats,
		RuntimeKind:  "go",
		UptimeSec:    int64(time.Since(heartbeatStart).Seconds()),
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
	}
}
