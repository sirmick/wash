// FE-side credit emitter for per-channel flow control (QOS.md §5).
//
// Each raw channel from router → shell opens with an implicit initial
// credit window (64 KiB — matches Go-side DefaultChannelCredit). As
// the renderer absorbs bytes, this module emits `channel.credit{ch,n}`
// frames back to the router to replenish the budget, so the producer
// (router-side Bulk-class raw writer) doesn't stall.
//
// Strategy: count bytes absorbed per channel. When the running total
// crosses half the initial window, send one credit frame for those
// bytes and reset the counter. Half-window threshold trades wire
// chatter (fewer credit frames) against responsiveness (longer pause
// if producer gets ahead).

const INITIAL_WINDOW = 64 * 1024;
const REPLENISH_THRESHOLD = INITIAL_WINDOW >>> 1; // 32 KiB

export type CreditSender = (channelID: number, n: number) => void;

/**
 * CreditTracker accumulates bytes-absorbed per channel and emits
 * `channel.credit` frames via `send` when the per-channel running
 * total crosses REPLENISH_THRESHOLD. One instance per shell session.
 */
export class CreditTracker {
  private counts = new Map<number, number>();
  private readonly send: CreditSender;

  constructor(send: CreditSender) {
    this.send = send;
  }

  /**
   * Call this every time the FE consumes `n` bytes on raw channel
   * `channelID`. The tracker decides when to send a credit frame.
   */
  absorbed(channelID: number, n: number): void {
    const prior = this.counts.get(channelID) ?? 0;
    const next = prior + n;
    if (next >= REPLENISH_THRESHOLD) {
      // Send a single credit frame for the full accumulated amount,
      // not just the threshold — keeps the running grant in step
      // with the consumed total.
      this.send(channelID, next);
      this.counts.delete(channelID);
      return;
    }
    this.counts.set(channelID, next);
  }

  /**
   * Forget the running count for a channel (called on
   * channel.unbind). Avoids a spurious credit frame for a closed
   * channel.
   */
  forget(channelID: number): void {
    this.counts.delete(channelID);
  }

  /** Test hook: peek at the running count. */
  pending(channelID: number): number {
    return this.counts.get(channelID) ?? 0;
  }
}

export const CREDIT_INITIAL_WINDOW = INITIAL_WINDOW;
export const CREDIT_REPLENISH_THRESHOLD = REPLENISH_THRESHOLD;
