// Unit tests for window layouts' pure half (docs/WM_LAYOUT.md §4).

import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  PRESETS,
  TILE_GUTTER,
  applyShape,
  canonicalShape,
  cellStateKey,
  decodeCellLayout,
  dissolve,
  floatAt,
  floatWindow,
  frameToWin,
  moveToSlot,
  neighbourSlot,
  parseCellStateKey,
  reconcileAll,
  slotAt,
  slotFrames,
  tileWindow,
} from './tiling.ts';
import type { Cell, CellLayout, TileWin } from './tiling.ts';
import { viewportForRect, workArea } from './viewport-math.ts';

const SCREEN = { w: 1000, h: 640 };
const INSETS = { top: 0, right: 0, bottom: 40 };
const areaOf = (c: Cell) => workArea(SCREEN, c.vx, c.vy, INSETS);
const cellOf = (w: TileWin) => viewportForRect(w, SCREEN, 3);

const win = (key: string, x: number, y: number, extra: Partial<TileWin> = {}): TileWin =>
  ({ key, x, y, w: 300, h: 200, state: 'normal', gz: 1, ...extra });

const run = (layouts: Record<string, CellLayout>, wins: TileWin[]) =>
  reconcileAll({ layouts: new Map(Object.entries(layouts)), wins, cellOf, areaOf });

// apply folds a reconcile result into the windows and layouts, the way the
// router's echo would.
function settle(layouts: Record<string, CellLayout>, wins: TileWin[]) {
  for (let i = 0; i < 4; i++) {
    const { changed, ops } = run(layouts, wins);
    if (!changed.size && !ops.length) return { layouts, wins };
    for (const [id, l] of changed) {
      if (l) layouts[id] = l;
      else delete layouts[id];
    }
    wins = wins.map((w) => {
      const op = ops.find((o) => o.key === w.key);
      return op ? { ...w, x: op.x, y: op.y, w: op.w, h: op.h } : w;
    });
  }
  throw new Error('reconcile did not settle');
}

test('every preset is already canonical, so a tick is a string compare', () => {
  for (const p of PRESETS) assert.equal(canonicalShape(p.shape), p.shape.replace('1@25 1 1@25', '1@25 1@50 1@25'));
});

test('cell state keys round-trip and stay in the shell: namespace', () => {
  assert.equal(cellStateKey({ vx: 2, vy: 1 }), 'shell:layout:2,1');
  assert.deepEqual(parseCellStateKey('shell:layout:2,1'), { vx: 2, vy: 1 });
  assert.equal(parseCellStateKey('shell:other'), null);
});

test('slot frames tile the work area with gutters and no seams', () => {
  const f = slotFrames('r2', areaOf({ vx: 0, vy: 0 }));
  assert.deepEqual(f[0], { x: 0, y: 0, w: 498, h: 600 });
  assert.equal(f[1].x, f[0].x + f[0].w + TILE_GUTTER);
  assert.equal(f[1].x + f[1].w, 1000);
  // A frame's window is its content: less the titlebar and borders.
  assert.deepEqual(frameToWin(f[0]), { x: 0, y: 0, w: 496, h: 570 });
  assert.deepEqual(frameToWin(f[0], true), f[0]);
});

test('applyShape fills slots most-recently-focused first; the rest float', () => {
  const wins = [win('a', 10, 10, { gz: 1 }), win('b', 20, 20, { gz: 3 }), win('c', 30, 30, { gz: 2 })];
  const l = applyShape(null, 'r2', wins);
  assert.deepEqual(l.slots, ['b', 'c']);
  assert.deepEqual(l.floating, ['a']);
  assert.deepEqual(l.restore.b, { x: 20, y: 20, w: 300, h: 200 });
});

test('reshaping keeps windows in the slots they had', () => {
  const l = applyShape({ shape: 'r3', slots: ['a', 'b', 'c'], floating: [], restore: {} }, 'r2', []);
  assert.deepEqual(l.slots, ['a', 'b']);
});

test('arranging places every slotted window in its panel', () => {
  const wins = [win('a', 10, 10, { gz: 2 }), win('b', 400, 300, { gz: 1 })];
  const { layouts, wins: after } = settle({ '0,0': applyShape(null, 'r2', wins) }, wins);
  assert.deepEqual(layouts['0,0'].slots, ['a', 'b']);
  assert.deepEqual(after.map(({ x, y, w, h }) => ({ x, y, w, h })), [
    { x: 0, y: 0, w: 496, h: 570 },
    { x: 502, y: 0, w: 496, h: 570 },
  ]);
});

test('a closed window empties its slot; a new one takes the first empty slot', () => {
  const layouts = { '0,0': { shape: 'r2', slots: ['a', 'b'], floating: [], restore: {} } };
  let { layouts: l, wins } = settle(layouts, [win('b', 600, 10)]);
  assert.deepEqual(l['0,0'].slots, [null, 'b']);
  ({ layouts: l, wins } = settle(l, [...wins, win('c', 100, 100)]));
  assert.deepEqual(l['0,0'].slots, ['c', 'b']);
  assert.equal(wins.find((w) => w.key === 'c')!.x, 0);
});

test('a full cell floats an arrival', () => {
  const layouts = { '0,0': { shape: '1', slots: ['a'], floating: [], restore: {} } };
  const { layouts: l } = settle(layouts, [win('a', 0, 0), win('b', 100, 100)]);
  assert.deepEqual(l['0,0'].floating, ['b']);
});

test('floating a window gives back its pre-tile frame, in place', () => {
  const pre = { x: 33, y: 44, w: 300, h: 200 };
  const layouts = { '0,0': { shape: 'r2', slots: ['a', null], floating: [], restore: { a: pre } } };
  const tiled = settle(layouts, [win('a', 33, 44)]);
  const floated = settle({ '0,0': floatWindow(tiled.layouts['0,0'], 'a') }, tiled.wins);
  const a = floated.wins[0];
  assert.deepEqual({ x: a.x, y: a.y, w: a.w, h: a.h }, pre);
  assert.deepEqual(floated.layouts['0,0'].floating, ['a']);
  assert.deepEqual(floated.layouts['0,0'].restore, {});
  // And it is not pulled back into the free slot.
  assert.deepEqual(floated.layouts['0,0'].slots, [null, null]);
  // Tile puts it back.
  const back = settle({ '0,0': tileWindow(floated.layouts['0,0'], floated.wins[0]) }, floated.wins);
  assert.deepEqual(back.layouts['0,0'].slots, ['a', null]);
});

test('sent to a floating cell, a window keeps its slot position but gets its size back', () => {
  const layouts = { '0,0': { shape: 'r2', slots: ['a', null], floating: [], restore: { a: { x: 5, y: 5, w: 300, h: 200 } } } };
  // Tiled to 496x570 at (0,0), then moved one screen right (send to viewport).
  const { layouts: l, wins } = settle(layouts, [win('a', 1000, 0, { w: 496, h: 570 })]);
  assert.deepEqual(l['0,0'].slots, [null, null]);
  assert.deepEqual({ x: wins[0].x, y: wins[0].y, w: wins[0].w, h: wins[0].h }, { x: 1000, y: 0, w: 300, h: 200 });
});

test('moving between tiled cells carries the true floating frame along', () => {
  const pre = { x: 5, y: 5, w: 300, h: 200 };
  const layouts = {
    '0,0': { shape: '1', slots: ['a'], floating: [], restore: { a: pre } },
    '1,0': { shape: '1', slots: [null], floating: [], restore: {} },
  };
  const { layouts: l, wins } = settle(layouts, [win('a', 1000, 0, { w: 996, h: 570 })]);
  assert.deepEqual(l['1,0'].slots, ['a']);
  assert.deepEqual(l['1,0'].restore.a, pre);
  assert.equal(wins[0].x, 1000);
});

test('minimizing empties the slot; restoring takes it back with its frame remembered', () => {
  const pre = { x: 5, y: 5, w: 300, h: 200 };
  const layouts = { '0,0': { shape: 'r2', slots: ['a', null], floating: [], restore: { a: pre } } };
  const min = settle(layouts, [win('a', 0, 0, { state: 'minimized' })]);
  assert.deepEqual(min.layouts['0,0'].slots, [null, null]);
  assert.deepEqual(min.layouts['0,0'].restore.a, pre);
  const back = settle(min.layouts, [{ ...min.wins[0], state: 'normal' }]);
  assert.deepEqual(back.layouts['0,0'].slots, ['a', null]);
  assert.deepEqual(back.layouts['0,0'].restore.a, pre);
});

test('a maximized window keeps its slot and is not moved', () => {
  const layouts = { '0,0': { shape: 'r2', slots: ['a', null], floating: [], restore: {} } };
  const { changed, ops } = run(layouts, [win('a', 10, 10, { state: 'maximized' })]);
  assert.equal(changed.size, 0);
  assert.equal(ops.length, 0);
});

test('dissolving restores every window, then drops the layout', () => {
  const pre = { x: 5, y: 5, w: 300, h: 200 };
  const layouts = { '0,0': { shape: 'r2', slots: ['a', null], floating: [], restore: { a: pre } } };
  const tiled = settle(layouts, [win('a', 5, 5)]);
  const gone = settle({ '0,0': dissolve(tiled.layouts['0,0']) }, tiled.wins);
  assert.equal(gone.layouts['0,0'], undefined);
  assert.deepEqual({ x: gone.wins[0].x, y: gone.wins[0].y, w: gone.wins[0].w, h: gone.wins[0].h }, pre);
});

test('a settled layout produces no writes and no moves', () => {
  const wins = [win('a', 10, 10)];
  const { layouts, wins: after } = settle({ '0,0': applyShape(null, 'r2', wins) }, wins);
  const { changed, ops } = run(layouts, after);
  assert.equal(changed.size, 0);
  assert.equal(ops.length, 0);
});

test('decodeCellLayout refuses junk rather than tiling with it', () => {
  assert.equal(decodeCellLayout(null), null);
  assert.equal(decodeCellLayout({ shape: 'x[' }), null);
  assert.deepEqual(decodeCellLayout({ shape: 'r2', slots: ['a', 7] }), { shape: 'r2', slots: ['a', null], floating: [], restore: {} });
});

// ---- M5: moving between panels ----

const pos = (w: { x: number; y: number; w: number; h: number }) => ({ x: w.x, y: w.y, w: w.w, h: w.h });

const L = (shape: string, slots: (string | null)[], extra: Partial<CellLayout> = {}): CellLayout =>
  ({ shape, slots, floating: [], restore: {}, ...extra });

test('slotAt finds the panel under a point, and nothing over a gutter', () => {
  const area = areaOf({ vx: 0, vy: 0 });
  assert.equal(slotAt('r2', area, { x: 10, y: 10 }), 0);
  assert.equal(slotAt('r2', area, { x: 900, y: 10 }), 1);
  assert.equal(slotAt('r2', area, { x: 499, y: 10 }), -1);
  assert.equal(slotAt('r[c2 c2]', area, { x: 900, y: 500 }), 3);
});

test('moveToSlot within a cell swaps', () => {
  const out = moveToSlot(new Map([['0,0', L('r3', ['a', 'b', 'c'])]]), 'a', { vx: 0, vy: 0 }, 2, { x: 0, y: 0, w: 1, h: 1 });
  assert.deepEqual(out.get('0,0')!.slots, ['c', 'b', 'a']);
});

test('moveToSlot from a floating window displaces the occupant to a free slot, else floats it', () => {
  const cur = { x: 1, y: 2, w: 3, h: 4 };
  let out = moveToSlot(new Map([['0,0', L('r3', ['a', 'b', null], { floating: ['f'] })]]), 'f', { vx: 0, vy: 0 }, 0, cur);
  assert.deepEqual(out.get('0,0')!.slots, ['f', 'b', 'a']);
  assert.deepEqual(out.get('0,0')!.floating, []);
  assert.deepEqual(out.get('0,0')!.restore.f, cur);
  out = moveToSlot(new Map([['0,0', L('r2', ['a', 'b'], { floating: ['f'] })]]), 'f', { vx: 0, vy: 0 }, 1, cur);
  assert.deepEqual(out.get('0,0')!.slots, ['a', 'f']);
  assert.deepEqual(out.get('0,0')!.floating, ['b']);
});

test('moveToSlot across cells carries the pre-tile frame and empties the old slot', () => {
  const pre = { x: 9, y: 9, w: 300, h: 200 };
  const layouts = new Map([
    ['0,0', L('r2', ['a', null], { restore: { a: pre } })],
    ['1,0', L('r2', [null, null])],
  ]);
  const out = moveToSlot(layouts, 'a', { vx: 1, vy: 0 }, 1, { x: 0, y: 0, w: 1, h: 1 });
  assert.deepEqual(out.get('0,0')!.slots, [null, null]);
  assert.deepEqual(out.get('0,0')!.restore, {});
  assert.deepEqual(out.get('1,0')!.slots, [null, 'a']);
  assert.deepEqual(out.get('1,0')!.restore.a, pre);
});

test('a window moved to another cell panel settles there without a restore', () => {
  const pre = { x: 9, y: 9, w: 300, h: 200 };
  const layouts = {
    '0,0': L('r2', ['a', null], { restore: { a: pre } }),
    '1,0': L('r2', [null, null]),
  };
  const moved = moveToSlot(new Map(Object.entries(layouts)), 'a', { vx: 1, vy: 0 }, 1, pre);
  for (const [id, l] of moved) (layouts as Record<string, CellLayout>)[id] = l;
  // The drop itself put the window in cell (1,0).
  const { layouts: l, wins } = settle(layouts, [win('a', 1700, 100)]);
  assert.deepEqual(l['1,0'].slots, [null, 'a']);
  const right = slotFrames('r2', areaOf({ vx: 1, vy: 0 }))[1];
  assert.deepEqual(pos(wins[0]), frameToWin(right));
});

test('floatAt keeps the floating size but takes the drop position', () => {
  const l = floatAt(L('r2', ['a', null], { restore: { a: { x: 9, y: 9, w: 300, h: 200 } } }), 'a', 120, 80);
  assert.deepEqual(l.restore.a, { x: 120, y: 80, w: 300, h: 200 });
  const { wins } = settle({ '0,0': l }, [win('a', 120, 80, { w: 496, h: 570 })]);
  assert.deepEqual(pos(wins[0]), { x: 120, y: 80, w: 300, h: 200 });
});

test('neighbourSlot moves by geometry, -1 at the edge', () => {
  const area = areaOf({ vx: 0, vy: 0 });
  // Main + stack: main on the left, two stacked on the right.
  assert.equal(neighbourSlot('r[1 c2]', area, 0, 'right'), 1);
  assert.equal(neighbourSlot('r[1 c2]', area, 1, 'down'), 2);
  assert.equal(neighbourSlot('r[1 c2]', area, 2, 'left'), 0);
  assert.equal(neighbourSlot('r[1 c2]', area, 0, 'left'), -1);
});
