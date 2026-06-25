// Link-health telemetry (docs/QOS.md). The router pushes a link.stats
// frame ~1/s carrying per-class throughput + the session running totals;
// this module differences successive snapshots into instant/peak rates,
// folds in what only the FE can see (the WS send-buffer backlog +
// reconnect count), derives a green/amber/red status, and republishes for
// the desktop info panel + the About screen via window.wash.onLinkStats.

import { Sub } from './api';

// LinkSnapshot mirrors internal/wire/linkstats.go. Per-class arrays are
// indexed [Interactive, Bulk, Background, Control].
export interface LinkSnapshot {
  tx_bytes: number[];
  tx_frames: number[];
  queue_full: number[];
  dropped: number[];
  depth_hi: number[];
  depth: number[];
  credit_stalls: number;
  credit_wait_ns: number;
  rx_bytes: number;
  rx_frames: number;
  raw_bytes: number;
  wire_bytes: number;
}

export interface RawLinkStatsMsg {
  t: 'link.stats';
  live: LinkSnapshot;
  session: LinkSnapshot;
  connects: number;
  uptime_ms: number;
}

// LinkHealth is the FE-facing shape: the router's snapshots plus derived
// rates and FE-only signals. tx is router→browser ("down"); rx is
// browser→router ("up").
export interface LinkHealth {
  live: LinkSnapshot;
  session: LinkSnapshot;
  connects: number;
  uptimeMs: number;
  rateDownBps: number;
  peakDownBps: number;
  rateUpBps: number;
  bufferedAmount: number;
  reconnects: number;
  status: 'ok' | 'warn' | 'bad';
}

const sum = (a: number[] | undefined): number => (a ? a.reduce((x, y) => x + y, 0) : 0);

const healthSub = new Sub<LinkHealth | null>(null);

let prevTxTotal = 0;
let prevRx = 0;
let prevAt = 0;
let peakDown = 0;
let prevDropped = 0;
let prevQueueFull = 0;
let prevStalls = 0;

// Reconnect tracking: count each successful re-open after a drop.
let reconnects = 0;
let wasDown = false;

// noteConnState is fed the shell's ConnState transitions so the panel can
// show how many times the link dropped and recovered this page-load.
export function noteConnState(s: string): void {
  if (s === 'reconnecting' || s === 'closed') {
    wasDown = true;
  } else if (s === 'open' && wasDown) {
    reconnects++;
    wasDown = false;
  }
}

function deriveStatus(live: LinkSnapshot, buffered: number): LinkHealth['status'] {
  const dropped = sum(live.dropped);
  const queueFull = sum(live.queue_full);
  const stalls = live.credit_stalls;
  // Detect *rising* counters — a single historical drop shouldn't pin the
  // dot red forever. Live counters reset on reconnect, so a value lower
  // than last reads simply as "not rising".
  const dropRising = dropped > prevDropped;
  const qfRising = queueFull > prevQueueFull;
  const stallRising = stalls > prevStalls;
  prevDropped = dropped;
  prevQueueFull = queueFull;
  prevStalls = stalls;
  if (dropRising) return 'bad';
  if (buffered > 1_000_000 || qfRising || stallRising) return 'warn';
  return 'ok';
}

// ingestLinkStats is called from main.tsx's onCtrl for each link.stats
// frame, with the current WS send-buffer backlog the shell read locally.
export function ingestLinkStats(msg: RawLinkStatsMsg, bufferedAmount: number): void {
  const now = performance.now();
  const txTotal = sum(msg.live.tx_bytes);
  const rx = msg.live.rx_bytes;
  let rateDown = 0;
  let rateUp = 0;
  if (prevAt > 0 && now > prevAt) {
    const dt = (now - prevAt) / 1000;
    rateDown = Math.max(0, (txTotal - prevTxTotal) / dt);
    rateUp = Math.max(0, (rx - prevRx) / dt);
  }
  prevTxTotal = txTotal;
  prevRx = rx;
  prevAt = now;
  if (rateDown > peakDown) peakDown = rateDown;

  healthSub.set({
    live: msg.live,
    session: msg.session,
    connects: msg.connects,
    uptimeMs: msg.uptime_ms,
    rateDownBps: rateDown,
    peakDownBps: peakDown,
    rateUpBps: rateUp,
    bufferedAmount,
    reconnects,
    status: deriveStatus(msg.live, bufferedAmount),
  });
}

export function linkHealth(): LinkHealth | null {
  return healthSub.value;
}

export function onLinkHealth(cb: (h: LinkHealth) => void): () => void {
  return healthSub.on((v) => {
    if (v) cb(v);
  });
}
