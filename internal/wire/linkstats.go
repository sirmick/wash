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
