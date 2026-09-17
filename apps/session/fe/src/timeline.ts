// Pure decisions for the sidebar Timeline (docs/COMMANDER.md §6): how the
// journal's entries fold into hours, what each kind is called and looks
// like, and which rows a filter keeps. No DOM, no Solid — `node --test`-able.

export type TimelineEntry = WashActivityEntry;

/** The coarse families a person filters by; a kind's prefix decides. */
export type TimelineFamily = 'windows' | 'agents' | 'files' | 'hosts' | 'other';

export const FAMILIES: ReadonlyArray<{ id: TimelineFamily | 'all'; label: string }> = [
  { id: 'all', label: 'All' },
  { id: 'windows', label: 'Windows' },
  { id: 'agents', label: 'Agents' },
  { id: 'files', label: 'Files' },
  { id: 'hosts', label: 'Hosts' },
];

export function familyOf(kind: string): TimelineFamily {
  if (kind.startsWith('window.')) return 'windows';
  if (kind.startsWith('agent.') || kind === 'brief' || kind === 'rollup') return 'agents';
  if (kind === 'open.routed' || kind.startsWith('bulk.') || kind.startsWith('term.')) return 'files';
  if (kind.startsWith('session.') || kind.startsWith('peer.')) return 'hosts';
  return 'other';
}

/** kindIcon names the sprite icon a row wears (web/shell/build-icons.mjs
 *  is the set); the family sets the colour. */
export function kindIcon(kind: string): string {
  switch (kind) {
    case 'window.open':
    case 'window.focus': return 'monitor';
    case 'window.close': return 'x';
    case 'window.title': return 'pencil';
    case 'window.state': return 'layout-grid';
    case 'open.routed': return 'file-text';
    case 'session.attach': return 'wifi';
    case 'session.detach': return 'unplug';
    case 'peer.up':
    case 'peer.down': return 'network';
    case 'priv.escalate': return 'shield';
    case 'brief':
    case 'rollup': return 'sparkles';
  }
  if (kind.startsWith('agent.')) return 'bot';
  if (kind.startsWith('bulk.')) return 'list-checks';
  if (kind.startsWith('term.')) return 'terminal';
  return 'info';
}

/** kindLabel is the short verb a row shows before its title. */
export function kindLabel(kind: string): string {
  const table: Record<string, string> = {
    'window.open': 'opened', 'window.close': 'closed', 'window.focus': 'switched to',
    'window.title': 'now', 'window.state': 'window', 'open.routed': 'opened',
    'session.attach': 'connected', 'session.detach': 'disconnected',
    'peer.up': 'host up', 'peer.down': 'host down', 'priv.escalate': 'escalated',
    'agent.start': 'agent started', 'agent.turn': 'agent turn', 'agent.tool': 'agent ran',
    'agent.ask': 'agent asked', 'agent.answer': 'answered', 'agent.end': 'agent ended',
    'agent.resume': 'agent resumed', 'bulk.start': 'started', 'bulk.done': 'finished',
    'bulk.fail': 'failed', brief: 'AI brief', rollup: 'AI rollup',
  };
  return table[kind] ?? kind;
}

/** fmtClock renders a wall-clock time for a row: HH:MM. */
export function fmtClock(ts: number): string {
  const d = new Date(ts);
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
}

/** hourKey buckets a timestamp by local hour; hourLabel names the bucket
 *  ("Today 14:00", "Yesterday 09:00", "Mon 16 Sep 08:00"). */
export function hourKey(ts: number): number {
  const d = new Date(ts);
  d.setMinutes(0, 0, 0);
  return d.getTime();
}

export function hourLabel(key: number, now = Date.now()): string {
  const d = new Date(key);
  const hh = `${String(d.getHours()).padStart(2, '0')}:00`;
  const today = new Date(now); today.setHours(0, 0, 0, 0);
  const day = new Date(key); day.setHours(0, 0, 0, 0);
  const diff = Math.round((today.getTime() - day.getTime()) / 86_400_000);
  if (diff === 0) return `Today ${hh}`;
  if (diff === 1) return `Yesterday ${hh}`;
  return `${d.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short' })} ${hh}`;
}

export interface TimelineHour {
  key: number;
  label: string;
  rows: TimelineEntry[];
}

/** groupByHour folds newest-first entries into newest-first hour groups. */
export function groupByHour(entries: ReadonlyArray<TimelineEntry>, now = Date.now()): TimelineHour[] {
  const out: TimelineHour[] = [];
  for (const e of entries) {
    const key = hourKey(e.ts);
    const last = out[out.length - 1];
    if (last && last.key === key) last.rows.push(e);
    else out.push({ key, label: hourLabel(key, now), rows: [e] });
  }
  return out;
}

/** filterEntries keeps rows of one family (or all) matching a query over
 *  title, line, app and host. */
export function filterEntries(entries: ReadonlyArray<TimelineEntry>, family: TimelineFamily | 'all', query: string): TimelineEntry[] {
  const q = query.trim().toLowerCase();
  return entries.filter((e) => {
    if (family !== 'all' && familyOf(e.kind) !== family) return false;
    if (!q) return true;
    return [e.title, e.line, e.app, e.host].some((v) => (v ?? '').toLowerCase().includes(q));
  });
}

/** mergeNewestFirst combines pages from several hosts into one list,
 *  newest first, deduped by (host, seq). */
export function mergeNewestFirst(pages: ReadonlyArray<{ entries: TimelineEntry[] }>, cap = 500): TimelineEntry[] {
  const seen = new Set<string>();
  const all: TimelineEntry[] = [];
  for (const p of pages) {
    for (const e of p.entries) {
      const id = `${e.host}:${e.seq}`;
      if (seen.has(id)) continue;
      seen.add(id);
      all.push(e);
    }
  }
  all.sort((a, b) => b.ts - a.ts || b.seq - a.seq);
  return all.slice(0, cap);
}

/** prependLive puts a tailed entry at the top, deduped, keeping the cap. */
export function prependLive(entries: ReadonlyArray<TimelineEntry>, e: TimelineEntry, cap = 500): TimelineEntry[] {
  const id = `${e.host}:${e.seq}`;
  if (entries.some((x) => `${x.host}:${x.seq}` === id)) return [...entries];
  return [e, ...entries].slice(0, cap);
}

/** rowTitle is what a row shows after its verb: the title when the entry
 *  has one, else its line; and the line becomes the subtitle. */
export function rowText(e: TimelineEntry): { main: string; sub: string } {
  if (e.title && e.title !== e.line) return { main: e.title, sub: e.line };
  return { main: e.line || e.title || e.kind, sub: '' };
}
