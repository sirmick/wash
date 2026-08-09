// host.stats producer.
//
// Sidebar's About widget (and any future ambient-status widget that
// the session chrome owns) wants live CPU/mem load. Existing
// wash-top samples /proc/{stat,meminfo} every 2s but is a
// user-launched window app — present only when the user has it open.
// The session BE samples on its own ticker so the sidebar always has
// a live value to render.
//
// Cadence is 5s (vs. wash-top's 2s) because the sidebar is ambient
// status, not an active monitoring view — slower ticks mean less
// chatter on the WS, and the bar smoothes a 5s delta enough to read.
// The wire payload is intentionally tiny (one float, two ints) so
// the per-tick frame is small.
//
// No window-mapped pause: the session app is always mapped (desktop
// surface) so a "pause when minimized" branch would never fire.

package session

import (
	"log"
	"time"

	"github.com/sirmick/wash/internal/proc"
	"github.com/sirmick/wash/pkg/sdk"
)

// HostStatsInterval is the sample cadence. Exported so a future
// settings panel (or a test) can tune it. The minimum useful value
// is ~1s — under that, CPU% noise from the sampler's own work
// starts to dominate. 5s strikes the readable/ambient balance.
const HostStatsInterval = 5 * time.Second

// startHostStats kicks off a goroutine that samples /proc/{stat,
// meminfo} every HostStatsInterval and pushes a `host.stats`
// app_msg to the FE. Runs for the lifetime of the Conn — exits when
// c.Done() closes.
//
// First tick fires after one full interval (5s of warm-up). That's
// deliberate: CPUPercent needs a baseline, so the first sample is
// just to establish prev. Pushing immediately would ship a zero,
// which the FE would render as a flicker.
func startHostStats(c *sdk.Conn) {
	go runHostStatsLoop(c)
}

func runHostStatsLoop(c *sdk.Conn) {
	prev, _, err := proc.ReadStat()
	if err != nil {
		log.Printf("wash-session host.stats: initial /proc/stat: %v", err)
		// Keep going — readStat may transiently fail and recover
		// (cgroups during a container restart, etc.). The next tick
		// will retry.
	}
	pushNetIfaces(c) // interface IPs need no baseline — ship the first set now
	t := time.NewTicker(HostStatsInterval)
	defer t.Stop()
	for {
		select {
		case <-c.Done():
			return
		case <-t.C:
			pushNetIfaces(c) // companion push: live interface IPs for the net panel
			curr, _, err := proc.ReadStat()
			if err != nil {
				log.Printf("wash-session host.stats: /proc/stat: %v", err)
				continue
			}
			mem, err := proc.ReadMem()
			if err != nil {
				log.Printf("wash-session host.stats: /proc/meminfo: %v", err)
				continue
			}
			cpuPct := proc.CPUPercent(prev, curr)
			prev = curr
			msg := map[string]any{
				"kind":            "host.stats",
				"cpu_pct":         cpuPct,
				"mem_used":        mem.Used(),
				"mem_total":       mem.Total,
				"mem_pct":         mem.Percent(),
				"captured_at_sec": time.Now().Unix(),
			}
			if err := c.SendAppMsg(msg); err != nil {
				log.Printf("wash-session host.stats: send: %v", err)
			}
		}
	}
}
