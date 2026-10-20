// Window layouts — the live half (docs/WM_LAYOUT.md §4, §6). Holds every
// cell's layout as the router last told us, offers the actions the menus
// call, and on the HEAD shell only runs reconcileAll whenever the windows,
// the layouts or the work area change, sending what it returns.
//
// Data and geometry are split on purpose. Any tab may change a layout (a
// preset, Float, Tile) — that is just a shell.state_set. Only the head turns
// layouts into window.move/window.resize, from its own screen, so two tabs
// with different screen sizes never fight over the same windows; when a tab
// gains the head it re-places everything for its screen, the way
// wash-display resizes its output (main.tsx republishDisplayState).

import { batch, createEffect, createRoot, createSignal, on, untrack } from 'solid-js';
import {
  applyShape,
  canonicalShape,
  cellId,
  cellStateKey,
  decodeCellLayout,
  dissolve,
  floatAt,
  floatWindow,
  moveToSlot,
  neighbourSlot,
  parseCellStateKey,
  reconcileAll,
  splitWinKey,
  tileWindow,
  winKey,
} from './tiling';
import type { Cell, CellLayout, TileWin } from './tiling';
import { cellWorkArea, moveLocal, resizeLocal, viewportFor, windows } from './wm';
import type { Win } from './wm';
import type { FocusDir } from '@wash/ui/split-tree';

const [layouts, setLayouts] = createSignal<ReadonlyMap<string, CellLayout>>(new Map());

// previews are shapes being dragged (a divider in flight), by cellId. They
// stand in for the stored shape until the drag commits, so the panels move
// under the pointer: on the head for real (the edge-resize path already
// streams live resizes), elsewhere locally only.
const [previews, setPreviews] = createSignal<ReadonlyMap<string, string>>(new Map());

export function setPreview(c: Cell, shape: string | null): void {
  const next = new Map(previews());
  if (shape === null) next.delete(cellId(c));
  else next.set(cellId(c), shape);
  setPreviews(next);
}

// effectiveShape is what a cell looks like right now: the preview if a
// divider is being dragged, else the stored shape.
export const effectiveShape = (c: Cell): string | undefined =>
  previews().get(cellId(c)) ?? cellLayout(c)?.shape;

// dragHover is the panel under a window being dragged by its titlebar —
// where a drop would put it. The overlay highlights it.
const [dragHover, setDragHover] = createSignal<{ cell: Cell; slot: number } | null>(null);
export { dragHover, setDragHover };

export interface TilingDeps {
  // Whether this tab holds the router's foreground head.
  holdsHead: () => boolean;
  // shell.state_set on the LOCAL router (layouts live there, as the head
  // does; remote hosts' windows are tiled by the local head too).
  sendState: (key: string, state: StoredLayout | null) => void;
  move: (origin: string, windowID: number, x: number, y: number) => void;
  resize: (origin: string, windowID: number, w: number, h: number) => void;
  log: (msg: string) => void;
}

let deps: TilingDeps | null = null;

// What goes on the wire: the layout plus who wrote it and their revision.
// The router echoes every write back to every shell, the writer included,
// so a tab that writes twice in a row sees its first write come back after
// its second — and reverting to it, even for one reconcile, can undo a
// drop. Own echoes older than this tab's latest write are ignored (the same
// idea as window geometry's geomTok); everyone else's writes apply.
export type StoredLayout = CellLayout & { by: string; rev: number };
const TAB_ID = Math.random().toString(36).slice(2, 10);
let rev = 0;
const latestRev = new Map<string, number>();

const toTileWin = (w: Win): TileWin => ({
  key: winKey(w.origin, w.windowID),
  x: w.x,
  y: w.y,
  w: w.w,
  h: w.h,
  state: w.state,
  gz: w.gz,
  chromeless: w.chromeless,
});

// ---- the router's copy (local origin only) ----

// replaceTilingState takes a snapshot's app_state map: every shell:layout
// key in it, and nothing else.
export function replaceTilingState(appState: Record<string, unknown> | undefined): void {
  const next = new Map<string, CellLayout>();
  for (const [k, v] of Object.entries(appState ?? {})) {
    const c = parseCellStateKey(k);
    const l = c && decodeCellLayout(v);
    if (c && l) next.set(cellId(c), l);
  }
  setLayouts(next);
}

// patchTilingState applies one app_state patch op. Returns false when the
// key is not a layout, so the caller can treat it as an app's state.
export function patchTilingState(key: string, state: unknown): boolean {
  const c = parseCellStateKey(key);
  if (!c) return false;
  const own = state as Partial<StoredLayout> | null;
  if (own?.by === TAB_ID && typeof own.rev === 'number' && own.rev < (latestRev.get(key) ?? 0)) return true;
  const l = state == null ? null : decodeCellLayout(state);
  const next = new Map(layouts());
  if (l) next.set(cellId(c), l);
  else next.delete(cellId(c));
  setLayouts(next);
  return true;
}

// write stores a layout: optimistically here (so the next reconcile does
// not redo the same work before the echo) and on the router.
function write(c: Cell, l: CellLayout | null): void {
  const next = new Map(layouts());
  if (l) next.set(cellId(c), l);
  else next.delete(cellId(c));
  setLayouts(next);
  const key = cellStateKey(c);
  const r = ++rev;
  latestRev.set(key, r);
  deps?.sendState(key, l ? { ...l, by: TAB_ID, rev: r } : null);
}

// ---- queries + actions (any shell) ----

export const cellLayout = (c: Cell): CellLayout | undefined => layouts().get(cellId(c));

const cellWins = (c: Cell): TileWin[] =>
  windows
    .filter((w) => !w.crashed)
    .filter((w) => { const v = viewportFor(w); return v.vx === c.vx && v.vy === c.vy; })
    .map(toTileWin);

// setCellLayout gives a cell a layout (a layout string; throws on a
// malformed one) or, with null, dissolves it. Maximized windows in the cell
// are restored first: the router ignores moves for them, and a layout is a
// request to see everything.
export function setCellLayout(c: Cell, shape: string | null): void {
  const prev = cellLayout(c) ?? null;
  if (shape === null) {
    if (prev) write(c, dissolve(prev));
    return;
  }
  const canon = canonicalShape(shape);
  if (prev && prev.shape === canon) return;
  for (const w of windows) {
    if (w.state !== 'maximized') continue;
    const v = viewportFor(w);
    if (v.vx === c.vx && v.vy === c.vy) window.wash.restoreWindow(w.windowID, w.origin);
  }
  // Restored windows count as normal for slotting; their geometry is the
  // router's to settle and the head re-places them once it does.
  const wins = cellWins(c).map((w) => (w.state === 'maximized' ? { ...w, state: 'normal' as const } : w));
  write(c, applyShape(prev, canon, wins));
}

// windowTiling reports how a window relates to its cell's layout.
export function windowTiling(w: Win): 'none' | 'slotted' | 'floating' {
  const l = cellLayout(viewportFor(w));
  if (!l || l.shape === '') return 'none';
  const k = winKey(w.origin, w.windowID);
  return l.slots.includes(k) ? 'slotted' : 'floating';
}

export function floatInLayout(w: Win): void {
  const c = viewportFor(w);
  const l = cellLayout(c);
  if (l) write(c, floatWindow(l, winKey(w.origin, w.windowID)));
}

export function tileInLayout(w: Win): void {
  const c = viewportFor(w);
  const l = cellLayout(c);
  if (l) write(c, tileWindow(l, toTileWin(w)));
}

export const hasFreeSlot = (c: Cell): boolean => (cellLayout(c)?.slots ?? []).includes(null);

// setCellShape changes a tiled cell's shape without touching who sits
// where — a divider drag or Even out panels, which keep the slot count.
export function setCellShape(c: Cell, shape: string): void {
  const l = cellLayout(c);
  if (!l || l.shape === '' || l.shape === shape) return;
  write(c, { ...l, shape });
}

// slotOf is a window's panel index in its cell's layout, or -1.
export function slotOf(w: Win): number {
  const l = cellLayout(viewportFor(w));
  return l ? l.slots.indexOf(winKey(w.origin, w.windowID)) : -1;
}

// moveWindowToSlot puts a window into slot j of cell c (a swap within the
// cell, a move from anywhere else).
export function moveWindowToSlot(w: Win, c: Cell, j: number): void {
  const changed = moveToSlot(layouts(), winKey(w.origin, w.windowID), c, j, { x: w.x, y: w.y, w: w.w, h: w.h });
  batch(() => {
    for (const [id, l] of changed) {
      const [vx, vy] = id.split(',').map(Number);
      write({ vx, vy }, l);
    }
  });
}

// floatWindowAt takes a window out of cell c's layout where it was dropped
// (Shift-drop): its floating size, at (x, y).
export function floatWindowAt(w: Win, c: Cell, x: number, y: number): void {
  const l = cellLayout(c);
  if (!l || l.shape === '') return;
  const k = winKey(w.origin, w.windowID);
  write(c, l.slots.includes(k) ? floatAt(l, k, x, y) : { ...l, floating: l.floating.includes(k) ? l.floating : [...l.floating, k] });
}

// focusNeighbourPanel focuses the window in the panel next to the focused
// one, by geometry. False when the focused window is not tiled or there is
// no occupied panel that way — the caller then pans the viewport instead.
export function focusNeighbourPanel(w: Win | undefined, dir: FocusDir): boolean {
  if (!w) return false;
  const c = viewportFor(w);
  const l = cellLayout(c);
  const i = slotOf(w);
  if (!l || i < 0) return false;
  const j = neighbourSlot(l.shape, cellWorkArea(c.vx, c.vy), i, dir, (k) => l.slots[k] !== null);
  if (j < 0) return false;
  const { origin, windowID } = splitWinKey(l.slots[j]!);
  window.wash.focusWindow(windowID, origin);
  return true;
}

// swapNeighbourPanel moves the focused window into the panel next to it,
// swapping with whatever is there. False when there is nothing that way.
export function swapNeighbourPanel(w: Win | undefined, dir: FocusDir): boolean {
  if (!w) return false;
  const c = viewportFor(w);
  const l = cellLayout(c);
  const i = slotOf(w);
  if (!l || i < 0) return false;
  const j = neighbourSlot(l.shape, cellWorkArea(c.vx, c.vy), i, dir);
  if (j < 0) return false;
  moveWindowToSlot(w, c, j);
  return true;
}

// ---- the head's loop ----

function reconcileNow(): void {
  if (!deps) return;
  const head = deps.holdsHead();
  const pv = previews();
  // A non-head tab only ever places a divider drag of its own, locally.
  if (!head && pv.size === 0) return;
  const live = windows.filter((w) => !w.crashed);
  const stored = layouts();
  const effective = new Map(stored);
  for (const [id, shape] of pv) {
    const l = stored.get(id);
    if (l && l.shape !== '') effective.set(id, { ...l, shape });
  }
  const { changed, ops } = reconcileAll({
    layouts: effective,
    wins: live.map(toTileWin),
    cellOf: (w) => viewportFor(w),
    areaOf: (c) => cellWorkArea(c.vx, c.vy),
  });
  if (!head) {
    // Local preview: only the windows in the cells being dragged, and
    // nothing sent — the head places for real once the drag commits.
    const inPreview = new Set<string>();
    for (const id of pv.keys()) for (const k of stored.get(id)?.slots ?? []) if (k) inPreview.add(k);
    for (const op of ops) {
      if (!inPreview.has(op.key)) continue;
      const { origin, windowID } = splitWinKey(op.key);
      moveLocal(origin, windowID, op.x, op.y);
      resizeLocal(origin, windowID, op.w, op.h);
    }
    return;
  }
  batch(() => {
    for (const [id, l] of changed) {
      const [vx, vy] = id.split(',').map(Number);
      // Never persist a preview: the stored shape stays until the drag
      // commits.
      const keep = l && pv.has(id) ? { ...l, shape: stored.get(id)?.shape ?? l.shape } : l;
      write({ vx, vy }, keep);
    }
    for (const op of ops) {
      const { origin, windowID } = splitWinKey(op.key);
      const w = live.find((x) => x.origin === origin && x.windowID === windowID);
      if (!w) continue;
      // Local first, as every other geometry writer does: the store then
      // already says where the window is going, so a reconcile that runs
      // again before the router echoes does not send it twice.
      if (w.x !== op.x || w.y !== op.y) {
        moveLocal(origin, windowID, op.x, op.y);
        deps!.move(origin, windowID, op.x, op.y);
      }
      if (w.w !== op.w || w.h !== op.h) {
        resizeLocal(origin, windowID, op.w, op.h);
        deps!.resize(origin, windowID, op.w, op.h);
      }
    }
  });
  if (ops.length) deps.log(`tiling: placed ${ops.length} window(s)`);
}

// One reconcile per microtask, however many inputs changed in it — a
// snapshot or a sidebar toggle touches dozens of signals at once.
let queued = false;
function schedule(): void {
  if (queued) return;
  queued = true;
  queueMicrotask(() => {
    queued = false;
    untrack(reconcileNow);
  });
}

export function startTiling(d: TilingDeps): void {
  deps = d;
  createRoot(() => {
    createEffect(on(
      () => {
        // Everything reconcileAll reads: the layouts, every window's
        // geometry, state and focus order, each cell's work area (one
        // read covers them all: they share a size), and the head.
        layouts();
        previews();
        for (const w of windows) void [w.x, w.y, w.w, w.h, w.state, w.gz, w.crashed];
        cellWorkArea(0, 0);
        return d.holdsHead();
      },
      () => {
        if (layouts().size || previews().size) schedule();
      },
    ));
  });
}
