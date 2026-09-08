package wire

// Link-health telemetry surfaced to the FE (the desktop info panel + the
// About screen). LinkStatsSnapshot is a plain-value copy of the router's
// per-connection counters; ShellLinkStats wraps the live (current
// connection) snapshot together with the session-lifetime running totals,
// pushed ~1/s while a browser is attached.
//
// Outbound only (router → FE): EncodeCtrl json-marshals it and the FE
// decodes in JS, so it needs no DecodeCtrl case. Per-class arrays are
// indexed by Class — Interactive=0, Bulk=1, Background=2, Control=3.

import "sort"

const TShellLinkStats = "link.stats"

type LinkStatsSnapshot struct {
	TxBytes   [4]uint64 `json:"tx_bytes"`
	TxFrames  [4]uint64 `json:"tx_frames"`
	QueueFull [4]uint64 `json:"queue_full"`
	Dropped   [4]uint64 `json:"dropped"`
	DepthHi   [4]uint64 `json:"depth_hi"`
	Depth     [4]uint64 `json:"depth"` // instantaneous queue occupancy at snapshot

	CreditStalls uint64 `json:"credit_stalls"`
	CreditWaitNs uint64 `json:"credit_wait_ns"`

	RxBytes  uint64 `json:"rx_bytes"`
	RxFrames uint64 `json:"rx_frames"`

	RawBytes  uint64 `json:"raw_bytes"`  // pre-compression asset bytes
	WireBytes uint64 `json:"wire_bytes"` // post-compression asset bytes

	DisplayTxBytes  uint64 `json:"display_tx_bytes"`  // video + video-popup payload bytes
	DisplayTxFrames uint64 `json:"display_tx_frames"` // video + video-popup frames

	// Apps breaks the same FE-bound traffic down by the app that
	// produced it, sorted by AppID so the render order is stable.
	// It is a PARTIAL view of TxBytes on purpose: only frames whose
	// origin is an app are attributable, so router lifecycle traffic
	// (window.create, session.patch, the link push itself) is absent
	// and the rows sum to less than the class totals above. The
	// difference is the router's own overhead, which is what a reader
	// wanting "who is using the link" should see it as.
	Apps []AppClassStats `json:"apps,omitempty"`
}

// AppClassStats is one app's FE-bound traffic, split by priority class
// (indexed like every other per-class array here: Interactive=0, Bulk=1,
// Background=2, Control=3).
type AppClassStats struct {
	AppID    string    `json:"app_id"`
	TxBytes  [4]uint64 `json:"tx_bytes"`
	TxFrames [4]uint64 `json:"tx_frames"`
}

// Plus returns the element-wise sum of two snapshots, taking the max for
// the depth high-watermark and b's instantaneous depth. Used to fold the
// live connection into the session running totals for display.
func (a LinkStatsSnapshot) Plus(b LinkStatsSnapshot) LinkStatsSnapshot {
	out := a
	for i := 0; i < 4; i++ {
		out.TxBytes[i] += b.TxBytes[i]
		out.TxFrames[i] += b.TxFrames[i]
		out.QueueFull[i] += b.QueueFull[i]
		out.Dropped[i] += b.Dropped[i]
		if b.DepthHi[i] > out.DepthHi[i] {
			out.DepthHi[i] = b.DepthHi[i]
		}
		out.Depth[i] = b.Depth[i]
	}
	out.CreditStalls += b.CreditStalls
	out.CreditWaitNs += b.CreditWaitNs
	out.RxBytes += b.RxBytes
	out.RxFrames += b.RxFrames
	out.RawBytes += b.RawBytes
	out.WireBytes += b.WireBytes
	out.DisplayTxBytes += b.DisplayTxBytes
	out.DisplayTxFrames += b.DisplayTxFrames
	out.Apps = mergeAppStats(a.Apps, b.Apps)
	return out
}

// mergeAppStats sums two per-app tables by AppID and returns the result
// sorted by AppID. Neither input is mutated: a snapshot the caller still
// holds must not change under it, which is the same copy-on-write rule
// the rest of these counters follow.
func mergeAppStats(a, b []AppClassStats) []AppClassStats {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	byID := make(map[string]AppClassStats, len(a)+len(b))
	for _, src := range [][]AppClassStats{a, b} {
		for _, r := range src {
			cur := byID[r.AppID]
			cur.AppID = r.AppID
			for i := 0; i < 4; i++ {
				cur.TxBytes[i] += r.TxBytes[i]
				cur.TxFrames[i] += r.TxFrames[i]
			}
			byID[r.AppID] = cur
		}
	}
	out := make([]AppClassStats, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AppID < out[j].AppID })
	return out
}

// ShellLinkStats is the periodic link-health push (router → FE). Live is
// the current connection's counters; Session is the running total across
// every connection this router (session) has served — so the desktop
// panel's MB figures survive a reconnect.
type ShellLinkStats struct {
	T        string            `json:"t"`
	Live     LinkStatsSnapshot `json:"live"`
	Session  LinkStatsSnapshot `json:"session"`
	Connects uint64            `json:"connects"`
	UptimeMs int64             `json:"uptime_ms"`
}

func NewShellLinkStats(live, session LinkStatsSnapshot, connects uint64, uptimeMs int64) ShellLinkStats {
	return ShellLinkStats{T: TShellLinkStats, Live: live, Session: session, Connects: connects, UptimeMs: uptimeMs}
}
