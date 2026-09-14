package agentd

import (
	"sort"
	"sync"
	"time"
)

// Usage is telemetry, not a roster transition. Agents may report it for every
// streamed chunk, while a human-readable counter gains nothing from updating
// faster than twice a second. Keep the newest value per row and ship one small
// Bulk patch rather than republishing the full roster at Interactive priority.
const usagePatchInterval = 500 * time.Millisecond

var (
	usageMu      sync.Mutex
	usagePending = map[string]usagePatchRow{}
	usageTimer   *time.Timer
	usageSending bool
	usageDelay   = usagePatchInterval // test seam

	usagePublish = func(p usagePatch) {
		if svc != nil {
			svc.PublishBulk(p)
		}
	}
)

type usagePatch struct {
	Kind string          `json:"kind"`
	Rows []usagePatchRow `json:"rows"`
}

type usagePatchRow struct {
	Key  string `json:"key"`
	Used int64  `json:"used"`
	Size int64  `json:"size"`
}

// setUsage keeps the authoritative snapshot current without publishing it.
// A later structural roster push therefore includes the newest counters even
// if it overtakes the coalesced patch.
func (h *hosted) setUsage(used, size int64) {
	hostedMu.Lock()
	h.used, h.size = used, size
	hostedMu.Unlock()

	now := time.Now()
	mutateStateIf(func(s *State) bool {
		r := rows[h.key]
		if r == nil {
			return false
		}
		r.Used, r.Size = used, size
		r.lastSeen = now
		s.Rows = publish(now)
		return false
	})

	queueUsagePatch(usagePatchRow{Key: h.key, Used: used, Size: size})
}

func queueUsagePatch(row usagePatchRow) {
	usageMu.Lock()
	usagePending[row.Key] = row // latest wins for this roster row
	if usageTimer == nil && !usageSending {
		usageTimer = time.AfterFunc(usageDelay, flushUsagePatches)
	}
	usageMu.Unlock()
}

func flushUsagePatches() {
	usageMu.Lock()
	if usageSending || len(usagePending) == 0 {
		usageTimer = nil
		usageMu.Unlock()
		return
	}
	if usageTimer != nil {
		usageTimer.Stop()
	}
	rows := make([]usagePatchRow, 0, len(usagePending))
	for _, row := range usagePending {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	usagePending = map[string]usagePatchRow{}
	usageTimer = nil
	usageSending = true
	usageMu.Unlock()

	usagePublish(usagePatch{Kind: "usage_patch", Rows: rows})

	usageMu.Lock()
	usageSending = false
	if len(usagePending) > 0 && usageTimer == nil {
		usageTimer = time.AfterFunc(usageDelay, flushUsagePatches)
	}
	usageMu.Unlock()
}

func stopUsagePatches() {
	usageMu.Lock()
	if usageTimer != nil {
		usageTimer.Stop()
	}
	usageTimer = nil
	usagePending = map[string]usagePatchRow{}
	usageMu.Unlock()
}
