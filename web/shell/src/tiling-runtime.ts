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
  floatWindow,
  parseCellStateKey,
  reconcileAll,
  splitWinKey,
  tileWindow,
  winKey,
} from './tiling';
import type { Cell, CellLayout, TileWin } from './tiling';
import { cellWorkArea, moveLocal, resizeLocal, viewportFor, windows } from './wm';
import type { Win } from './wm';

const [layouts, setLayouts] = createSignal<ReadonlyMap<string, CellLayout>>(new Map());

export interface TilingDeps {
  // Whether this tab holds the router's foreground head.
  holdsHead: () => boolean;
  // shell.state_set on the LOCAL router (layouts live there, as the head
  // does; remote hosts' windows are tiled by the local head too).
  sendState: (key: string, state: CellLayout | null) => void;
  move: (origin: string, windowID: number, x: number, y: number) => void;
  resize: (origin: string, windowID: number, w: number, h: number) => void;
  log: (msg: string) => void;
}

let deps: TilingDeps | null = null;

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
  deps?.sendState(cellStateKey(c), l);
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

// ---- the head's loop ----

function reconcileNow(): void {
  if (!deps || !deps.holdsHead()) return;
  const live = windows.filter((w) => !w.crashed);
  const { changed, ops } = reconcileAll({
    layouts: new Map(layouts()),
    wins: live.map(toTileWin),
    cellOf: (w) => viewportFor(w),
    areaOf: (c) => cellWorkArea(c.vx, c.vy),
  });
  batch(() => {
    for (const [id, l] of changed) {
      const [vx, vy] = id.split(',').map(Number);
      write({ vx, vy }, l);
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
        for (const w of windows) void [w.x, w.y, w.w, w.h, w.state, w.gz, w.crashed];
        cellWorkArea(0, 0);
        return d.holdsHead();
      },
      () => {
        if (layouts().size) schedule();
      },
    ));
  });
}
