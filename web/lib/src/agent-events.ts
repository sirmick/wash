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

import type { AgentEvent } from './agent-session';

const utf8 = new TextEncoder();

/** utf8Len is a string's byte length as agentd measures it (Go len()). */
export function utf8Len(s: string): number {
  return utf8.encode(s).length;
}

export interface ApplyResult {
  events: AgentEvent[];
  /** A delta could not be applied: its base row is missing or our text does
   * not match what it appends to. The caller should ask for a replay — the
   * snapshot re-establishes the base, and deltas resume from it. */
  gap: boolean;
}

/** applyAgentEvent folds one transcript_event into `prev` (kept sorted by
 * seq). Never mutates `prev`. */
export function applyAgentEvent(prev: AgentEvent[], e: AgentEvent): ApplyResult {
  const at = prev.findIndex((x) => x.seq === e.seq);
  if (!e.append) {
    // A whole row: agentd mutates tool rows in place, so a seq we already
    // hold replaces rather than duplicates.
    if (at >= 0) {
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

/** mergeAgentEvents folds a snapshot batch into `prev`, replacing by seq. */
export function mergeAgentEvents(prev: AgentEvent[], batch: AgentEvent[]): AgentEvent[] {
  const bySeq = new Map<number, AgentEvent>();
  for (const e of prev) bySeq.set(e.seq, e);
  for (const e of batch) bySeq.set(e.seq, e);
  return Array.from(bySeq.values()).sort((a, b) => a.seq - b.seq);
}

function insertBySeq(prev: AgentEvent[], e: AgentEvent): AgentEvent[] {
  // The common case is a new tail; keep it O(1) there.
  if (prev.length === 0 || prev[prev.length - 1].seq < e.seq) return [...prev, e];
  return mergeAgentEvents(prev, [e]);
}
