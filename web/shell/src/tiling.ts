// Window layouts — the pure half (docs/WM_LAYOUT.md §4). Given every cell's
// layout and the windows on the plane, reconcileAll decides which window
// sits in which slot and where every tiled window must be. No Solid, no DOM,
// no wire: tiling-runtime.ts feeds it the store and sends what it returns,
// and only on the head shell (§6).
//
// A cell's layout keeps SHAPE (a split-tree layout string) separate from
// OCCUPANCY (which window is in which slot), because windows move between
// slots far more often than the shape changes. Every window in a tiled cell
// is either slotted or floating; one that is neither has just arrived.

import { focusNeighbor, formatLayout, parseLayout, place } from '@wash/ui/split-tree';
import type { FocusDir } from '@wash/ui/split-tree';
import type { Rect } from './viewport-math.ts';

// Panels are separated by this much wallpaper. Room for a divider's grab
// area later (M5), and it keeps adjacent frames' borders from doubling up.
export const TILE_GUTTER = 4;

// Frame chrome, as window.tsx draws it: the stored w/h is the CONTENT size,
// the frame adds a titlebar and a border per side. Chromeless windows add
// neither.
export const TITLEBAR_H = 28;
export const FRAME_BORDER = 1;

export interface CellLayout {
  // Canonical layout string (formatLayout). '' = being dissolved: the head
  // restores every window it still remembers, then drops the layout.
  shape: string;
  // A window key per leaf, in tree order; null = an empty panel.
  slots: (string | null)[];
  // Windows explicitly floated in this cell — never auto-slotted.
  floating: string[];
  // Each tiled window's frame from before it was tiled. Also a pending
  // restore: an entry for a window that is no longer slotted is applied by
  // the head and then dropped.
  restore: Record<string, Rect>;
}

export interface Cell { vx: number; vy: number }

export interface TileWin {
  key: string;
  x: number;
  y: number;
  w: number;
  h: number;
  state: 'normal' | 'minimized' | 'maximized';
  // Global stacking (wm.ts Win.gz): higher = more recently focused.
  gz: number;
  chromeless?: boolean;
}

export interface GeomOp {
  key: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

export const winKey = (origin: string, windowID: number): string => `${origin}#${windowID}`;

export function splitWinKey(key: string): { origin: string; windowID: number } {
  const i = key.lastIndexOf('#');
  return { origin: key.slice(0, i), windowID: Number(key.slice(i + 1)) };
}

// ---- storage keys (one shell:-namespaced app_state key per cell) ----

const KEY_PREFIX = 'shell:layout:';

export const cellStateKey = (c: Cell): string => `${KEY_PREFIX}${c.vx},${c.vy}`;

export function parseCellStateKey(key: string): Cell | null {
  if (!key.startsWith(KEY_PREFIX)) return null;
  const m = /^(\d+),(\d+)$/.exec(key.slice(KEY_PREFIX.length));
  return m ? { vx: Number(m[1]), vy: Number(m[2]) } : null;
}

export const cellId = (c: Cell): string => `${c.vx},${c.vy}`;

// decodeCellLayout validates a stored blob; anything malformed is treated
// as no layout rather than a broken one.
export function decodeCellLayout(raw: unknown): CellLayout | null {
  if (!raw || typeof raw !== 'object') return null;
  const o = raw as Partial<CellLayout>;
  if (typeof o.shape !== 'string') return null;
  if (o.shape !== '') {
    try { parseLayout(o.shape, () => ({ kind: 'slot' as const })); } catch { return null; }
  }
  const slots = Array.isArray(o.slots) ? o.slots.map((s) => (typeof s === 'string' ? s : null)) : [];
  const floating = Array.isArray(o.floating) ? o.floating.filter((s): s is string => typeof s === 'string') : [];
  const restore: Record<string, Rect> = {};
  if (o.restore && typeof o.restore === 'object') {
    for (const [k, r] of Object.entries(o.restore)) {
      if (r && typeof r === 'object' && ['x', 'y', 'w', 'h'].every((f) => typeof (r as never)[f] === 'number')) {
        restore[k] = { x: r.x, y: r.y, w: r.w, h: r.h };
      }
    }
  }
  return { shape: o.shape, slots, floating, restore };
}

// ---- geometry ----

type Slot = { kind: 'slot' };
const slot = (): Slot => ({ kind: 'slot' });

export const slotCount = (shape: string): number =>
  shape === '' ? 0 : place(parseLayout(shape, slot), { x: 0, y: 0, w: 1, h: 1 }, 0).leaves.length;

// canonicalShape is the stored form of a layout string; throws on a
// malformed one (LayoutSyntaxError).
export const canonicalShape = (shape: string): string => formatLayout(parseLayout(shape, slot));

// slotFrames is each slot's FRAME rect within area, in tree order. Edges
// are rounded, not sizes, so neighbours never overlap or leave a seam.
export function slotFrames(shape: string, area: Rect): Rect[] {
  if (shape === '') return [];
  const { leaves } = place(parseLayout(shape, slot), area, TILE_GUTTER);
  return leaves.map(({ rect: r }) => {
    const x = Math.round(r.x);
    const y = Math.round(r.y);
    return { x, y, w: Math.round(r.x + r.w) - x, h: Math.round(r.y + r.h) - y };
  });
}

// frameToWin converts a frame rect into the window geometry that fills it.
export function frameToWin(frame: Rect, chromeless?: boolean): Rect {
  const bd = chromeless ? 0 : FRAME_BORDER;
  const tb = chromeless ? 0 : TITLEBAR_H;
  return { x: frame.x, y: frame.y, w: Math.max(1, frame.w - bd * 2), h: Math.max(1, frame.h - tb - bd * 2) };
}

// ---- actions (any shell; they only change data) ----

// applyShape gives a cell a layout. Windows already slotted keep their
// slot index where the new shape still has it; the rest of the cell's
// visible windows fill the empty slots most-recently-focused first, and
// what does not fit floats. A cell without a layout is arranged fresh.
export function applyShape(prev: CellLayout | null, shape: string, cellWins: TileWin[]): CellLayout {
  const n = slotCount(shape);
  const slots: (string | null)[] = Array.from({ length: n }, (_, i) => prev?.slots[i] ?? null);
  const restore: Record<string, Rect> = { ...(prev?.restore ?? {}) };
  const placed = new Set(slots.filter((s): s is string => s !== null));
  const candidates = cellWins
    .filter((w) => w.state !== 'minimized' && !placed.has(w.key))
    .sort((a, b) => b.gz - a.gz);
  const floating: string[] = [];
  for (const w of candidates) {
    const free = slots.indexOf(null);
    if (free < 0) { floating.push(w.key); continue; }
    slots[free] = w.key;
    if (!restore[w.key]) restore[w.key] = { x: w.x, y: w.y, w: w.w, h: w.h };
  }
  return { shape, slots, floating, restore };
}

// dissolve marks a layout for removal: every slotted window keeps its
// restore entry, so the head puts them all back, then drops the layout.
export const dissolve = (prev: CellLayout): CellLayout => ({ ...prev, shape: '', slots: [], floating: [] });

// floatWindow takes a window out of its slot; the head restores its frame.
export function floatWindow(prev: CellLayout, key: string): CellLayout {
  return {
    ...prev,
    slots: prev.slots.map((s) => (s === key ? null : s)),
    floating: prev.floating.includes(key) ? prev.floating : [...prev.floating, key],
  };
}

// tileWindow puts a floating window into the first empty slot, recording
// its current frame to restore later. Unchanged when nothing is free.
export function tileWindow(prev: CellLayout, w: TileWin): CellLayout {
  const free = prev.slots.indexOf(null);
  if (free < 0) return prev;
  const slots = [...prev.slots];
  slots[free] = w.key;
  return {
    ...prev,
    slots,
    floating: prev.floating.filter((k) => k !== w.key),
    restore: { ...prev.restore, [w.key]: { x: w.x, y: w.y, w: w.w, h: w.h } },
  };
}

// slotAt is the index of the slot whose frame holds point p (plane coords),
// or -1 over a gutter or outside the area.
export function slotAt(shape: string, area: Rect, p: { x: number; y: number }): number {
  return slotFrames(shape, area).findIndex((f) => p.x >= f.x && p.x < f.x + f.w && p.y >= f.y && p.y < f.y + f.h);
}

// moveToSlot puts window `key` into slot j of cell `to`, wherever it was
// before. Within one cell it is a swap: the window already in j takes the
// mover's old slot. From elsewhere, the displaced window takes the first
// other free slot, else floats (and is restored to its own floating frame).
// The mover's pre-tile frame moves with it; `current` is the fallback for a
// window that was never tiled. Returns every layout it changed, by cellId.
export function moveToSlot(
  layouts: ReadonlyMap<string, CellLayout>,
  key: string,
  to: Cell,
  j: number,
  current: Rect,
): Map<string, CellLayout> {
  const out = new Map<string, CellLayout>();
  const toId = cellId(to);
  const target = layouts.get(toId);
  if (!target || target.shape === '' || j < 0 || j >= target.slots.length) return out;
  if (target.slots[j] === key) return out;

  // Same cell, already slotted: a swap.
  const i = target.slots.indexOf(key);
  if (i >= 0) {
    const slots = [...target.slots];
    [slots[i], slots[j]] = [slots[j], slots[i]];
    out.set(toId, { ...target, slots });
    return out;
  }

  // From elsewhere: take the mover out of wherever it was, keeping its
  // pre-tile frame.
  let restore: Rect = current;
  for (const [id, l] of layouts) {
    if (id === toId) continue;
    if (!l.slots.includes(key) && !l.floating.includes(key) && !l.restore[key]) continue;
    if (l.restore[key]) restore = l.restore[key];
    const { [key]: _gone, ...rest } = l.restore;
    void _gone;
    out.set(id, {
      ...l,
      slots: l.slots.map((s) => (s === key ? null : s)),
      floating: l.floating.filter((k) => k !== key),
      restore: rest,
    });
  }
  const slots = [...target.slots];
  const displaced = slots[j];
  slots[j] = key;
  let floating = target.floating.filter((k) => k !== key);
  if (displaced) {
    const free = slots.indexOf(null);
    if (free >= 0) slots[free] = displaced;
    else floating = [...floating, displaced];
  }
  out.set(toId, { ...target, slots, floating, restore: { ...target.restore, [key]: target.restore[key] ?? restore } });
  return out;
}

// floatAt floats a window where it was dropped: the head gives it back its
// floating SIZE at (x, y), rather than its whole pre-tile frame.
export function floatAt(prev: CellLayout, key: string, x: number, y: number): CellLayout {
  const l = floatWindow(prev, key);
  const r = prev.restore[key];
  return r ? { ...l, restore: { ...l.restore, [key]: { x, y, w: r.w, h: r.h } } } : l;
}

// neighbourSlot is the slot a directional key lands on from slot i, by
// geometry (the kernel's focusNeighbor), or -1 at the edge. `only` limits
// the candidates (focus skips empty panels; a swap may land in one).
export function neighbourSlot(
  shape: string,
  area: Rect,
  i: number,
  dir: FocusDir,
  only: (k: number) => boolean = () => true,
): number {
  const frames = slotFrames(shape, area)
    .map((rect, k) => ({ path: String(k), rect }))
    .filter((_, k) => k === i || only(k));
  const p = focusNeighbor(frames, String(i), dir);
  return p === undefined ? -1 : Number(p);
}

// ---- reconcile (head only) ----

export interface ReconcileInput {
  layouts: Map<string, CellLayout>; // by cellId
  wins: TileWin[];
  // The cell a window belongs to (its centre's cell).
  cellOf: (w: TileWin) => Cell;
  // The work area of a cell, in plane coords.
  areaOf: (c: Cell) => Rect;
}

export interface ReconcileResult {
  // Layouts whose data changed: write these back. null = delete the key.
  changed: Map<string, CellLayout | null>;
  // Geometry to send, already diffed against the windows' current frames.
  ops: GeomOp[];
}

const sameRect = (a: Rect, b: Rect) => a.x === b.x && a.y === b.y && a.w === b.w && a.h === b.h;

const layoutEq = (a: CellLayout, b: CellLayout) => JSON.stringify(a) === JSON.stringify(b);

// reconcileAll brings every cell's occupancy in line with where the windows
// actually are, then says where each tiled window must be:
//
//   - A slot whose window is gone, minimized, or now in another cell empties.
//   - A window that leaves a tiled cell gets its pre-tile SIZE back where it
//     landed; one floated in place gets its whole frame back.
//   - A normal window new to a tiled cell (spawned there, moved there,
//     un-minimized) takes the first empty slot, else floats. It carries its
//     pre-tile frame with it from the cell it left.
//   - Every slotted normal window is placed in its slot. A maximized one
//     keeps its slot but is left alone until it is restored.
export function reconcileAll(input: ReconcileInput): ReconcileResult {
  const changed = new Map<string, CellLayout | null>();
  const ops = new Map<string, GeomOp>();
  const byKey = new Map(input.wins.map((w) => [w.key, w]));
  const cellKeyOf = new Map(input.wins.map((w) => [w.key, cellId(input.cellOf(w))]));

  // Pass 1: release. Collect the pre-tile frame of every window leaving a
  // slot or a floating list, so a window that lands in another tiled cell
  // keeps its true floating frame rather than its old slot's.
  const carried = new Map<string, Rect>();
  const next = new Map<string, CellLayout>();
  for (const [id, prev] of input.layouts) {
    const here = (k: string) => cellKeyOf.get(k) === id;
    const visible = (k: string) => byKey.get(k)?.state !== 'minimized';
    const slots = prev.slots.map((k) => (k && byKey.has(k) && here(k) && visible(k) ? k : null));
    const floating = prev.floating.filter((k) => byKey.has(k) && here(k));
    const restore: Record<string, Rect> = {};
    for (const [k, r] of Object.entries(prev.restore)) {
      const w = byKey.get(k);
      if (!w) continue; // closed: nothing to restore
      if (slots.includes(k)) { restore[k] = r; continue; }
      if (w.state === 'minimized' && here(k)) {
        // Keep it: un-minimized, the window re-enters this cell as an
        // arrival and should still remember its floating frame.
        restore[k] = r;
        continue;
      }
      carried.set(k, r);
      if (w.state !== 'normal') continue;
      // Released while visible: back to its floating size — in place if it
      // is still in this cell (floated), where it landed if it moved away.
      const target = here(k) ? r : { x: w.x, y: w.y, w: r.w, h: r.h };
      if (!sameRect(target, w)) ops.set(k, { key: k, ...target });
    }
    next.set(id, { shape: prev.shape, slots, floating, restore });
  }

  // Pass 2: arrivals, and drop dissolved layouts once nothing is pending.
  for (const [id, cur] of next) {
    if (cur.shape === '') continue;
    const known = new Set([...cur.slots, ...cur.floating]);
    const arrivals = input.wins
      .filter((w) => cellKeyOf.get(w.key) === id && w.state === 'normal' && !known.has(w.key))
      .sort((a, b) => b.gz - a.gz);
    for (const w of arrivals) {
      const free = cur.slots.indexOf(null);
      if (free < 0) { cur.floating.push(w.key); continue; }
      cur.slots[free] = w.key;
      cur.restore[w.key] = carried.get(w.key) ?? cur.restore[w.key] ?? { x: w.x, y: w.y, w: w.w, h: w.h };
      ops.delete(w.key); // arriving in a slot beats being restored
    }
  }

  // Pass 3: place.
  for (const [id, cur] of next) {
    if (cur.shape === '') continue;
    const [vx, vy] = id.split(',').map(Number);
    const frames = slotFrames(cur.shape, input.areaOf({ vx, vy }));
    cur.slots.forEach((k, i) => {
      const w = k ? byKey.get(k) : undefined;
      if (!w || w.state !== 'normal' || !frames[i]) return;
      const target = frameToWin(frames[i], w.chromeless);
      if (!sameRect(target, w)) ops.set(w.key, { key: w.key, ...target });
    });
  }

  for (const [id, cur] of next) {
    const prev = input.layouts.get(id)!;
    if (cur.shape === '' && Object.keys(cur.restore).length === 0) {
      changed.set(id, null);
      continue;
    }
    if (!layoutEq(prev, cur)) changed.set(id, cur);
  }
  return { changed, ops: [...ops.values()] };
}

// ---- presets ----

export interface Preset { name: string; shape: string }

export const PRESETS: Preset[] = [
  { name: 'Single', shape: '1' },
  { name: 'Halves', shape: 'r2' },
  { name: 'Thirds', shape: 'r3' },
  { name: '25 / 50 / 25', shape: 'r[1@25 1 1@25]' },
  { name: 'Main + stack', shape: 'r[1 c2]' },
  { name: 'Top + two', shape: 'c[1 r2]' },
  { name: '2 × 2', shape: 'r[c2 c2]' },
  { name: '3 × 3', shape: 'r[c3 c3 c3]' },
];
