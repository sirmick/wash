// Folding agentd's transcript stream into a window's event list.
//
// A streamed reply reaches the FE as its first chunk whole, then as deltas:
// events with `append` set whose text is what was ADDED to the row with that
// seq (apps/agentd/be/transcript_emit.go). Every message/thought event also
// carries `text_len`, the row's UTF-8 byte length after it applies, so a
// delta can be checked against the text we hold rather than trusted. Tool
// rows and everything else still arrive whole and replace by seq.
//
// Framework-free so both wash-ai and wash-edit fold the same way, and so it
// runs under node:test.

import type * as agentproto from './agent-protocol.gen';

const utf8 = new TextEncoder();

/** utf8Len is a string's byte length as agentd measures it (Go len()). */
export function utf8Len(s: string): number {
  return utf8.encode(s).length;
}

export interface ApplyResult {
  events: agentproto.Event[];
  /** A delta could not be applied: its base row is missing or our text does
   * not match what it appends to. The caller should ask for a replay — the
   * snapshot re-establishes the base, and deltas resume from it. */
  gap: boolean;
}

/** applyAgentEvent folds one transcript_event into `prev` (kept sorted by
 * seq). Never mutates `prev`. */
export function applyAgentEvent(prev: agentproto.Event[], e: agentproto.Event): ApplyResult {
  const at = prev.findIndex((x) => x.seq === e.seq);
  if (!e.append) {
    // A whole row: agentd mutates tool rows in place, so a seq we already
    // hold replaces rather than duplicates.
    if (at >= 0) {
      if (sameEvent(prev[at], e)) return { events: prev, gap: false };
      const next = prev.slice();
      next[at] = e;
      return { events: next, gap: false };
    }
    return { events: insertBySeq(prev, e), gap: false };
  }
  if (at < 0) return { events: prev, gap: true };
  const cur = prev[at];
  const text = cur.text ?? '';
  const base = cur.text_len ?? utf8Len(text);
  const add = e.text ?? '';
  const addLen = utf8Len(add);
  const total = e.text_len ?? base + addLen;
  if (base + addLen === total) {
    const next = prev.slice();
    next[at] = { ...cur, text: text + add, text_len: total };
    return { events: next, gap: false };
  }
  // Already have it (a replay crossed a delta): nothing to do.
  if (total <= base) return { events: prev, gap: false };
  return { events: prev, gap: true };
}

/** mergeAgentEvents folds a snapshot batch into `prev`, replacing by seq.
 *
 * A row the batch repeats unchanged keeps the object `prev` already holds,
 * and a batch that changes nothing returns `prev` itself. The transcript
 * renders keyed by object identity, so a replay that swapped in equal
 * copies rebuilt every row — thousands of them, images re-decoded — each
 * time a window re-subscribed after a reconnect. */
export function mergeAgentEvents(prev: agentproto.Event[], batch: agentproto.Event[]): agentproto.Event[] {
  const bySeq = new Map<number, agentproto.Event>();
  for (const e of prev) bySeq.set(e.seq, e);
  let changed = false;
  for (const e of batch) {
    const cur = bySeq.get(e.seq);
    if (cur && sameEvent(cur, e)) continue;
    bySeq.set(e.seq, e);
    changed = true;
  }
  if (!changed) return prev;
  return Array.from(bySeq.values()).sort((a, b) => a.seq - b.seq);
}

/** sameEvent: every field equal. Event fields are all primitives. */
export function sameEvent(a: agentproto.Event, b: agentproto.Event): boolean {
  if (a === b) return true;
  const ka = Object.keys(a) as (keyof agentproto.Event)[];
  const kb = Object.keys(b) as (keyof agentproto.Event)[];
  if (ka.length !== kb.length) return false;
  for (const k of ka) if (a[k] !== b[k]) return false;
  return true;
}

function insertBySeq(prev: agentproto.Event[], e: agentproto.Event): agentproto.Event[] {
  // The common case is a new tail; keep it O(1) there.
  if (prev.length === 0 || prev[prev.length - 1].seq < e.seq) return [...prev, e];
  return mergeAgentEvents(prev, [e]);
}

/** TranscriptHeld tracks how much of a session's transcript a window holds,
 * as agentd counts it: the epoch of the revs it has, and the highest rev.
 * A reconnecting window sends both with its replay request and is sent only
 * the rows that changed after (agentproto.TranscriptSubscribe) — not the
 * whole history, which for a long session runs to megabytes. */
export class TranscriptHeld {
  private epoch = '';
  private rev = 0;

  /** The window dropped what it held (a new session, a failed restore). */
  clear(): void {
    this.epoch = '';
    this.rev = 0;
  }

  /** A snapshot frame was merged. A new epoch means the revs started over. */
  snapshot(m: { epoch?: string; events?: agentproto.Event[] | null }): void {
    const epoch = m.epoch ?? '';
    if (epoch !== this.epoch) {
      this.epoch = epoch;
      this.rev = 0;
    }
    for (const e of m.events ?? []) this.see(e);
  }

  /** A live event was applied. */
  see(e: agentproto.Event): void {
    if ((e.rev ?? 0) > this.rev) this.rev = e.rev ?? 0;
  }

  /** What to put on a replay request; empty (a whole replay) until a
   * snapshot has said which epoch we are in. */
  since(): { epoch?: string; since?: number } {
    if (!this.epoch || this.rev === 0) return {};
    return { epoch: this.epoch, since: this.rev };
  }
}
