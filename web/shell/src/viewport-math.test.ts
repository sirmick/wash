import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { clampViewport, clampToPlane, isOnScreen, isOrphaned, viewportForRect, nextZ, sendToViewportRect, resizeRect } from './viewport-math.ts';

const PER = 3; // VIEWPORTS_PER_AXIS
const screen = { w: 1000, h: 800 };

test('clampViewport rounds then clamps into [0, perAxis-1]', () => {
  assert.deepEqual(clampViewport(1, 2, PER), { vx: 1, vy: 2 });
  assert.deepEqual(clampViewport(-1, -5, PER), { vx: 0, vy: 0 });
  assert.deepEqual(clampViewport(9, 9, PER), { vx: 2, vy: 2 });
  assert.deepEqual(clampViewport(1.4, 1.6, PER), { vx: 1, vy: 2 }, 'rounds to nearest');
});

test('viewportForRect maps a window centre to its grid cell', () => {
  // Centre at (100,100) → cell (0,0).
  assert.deepEqual(viewportForRect({ x: 0, y: 0, w: 200, h: 200 }, screen, PER), { vx: 0, vy: 0 });
  // Centre at (1100,900) → floor(1100/1000)=1, floor(900/800)=1 → (1,1).
  assert.deepEqual(viewportForRect({ x: 1000, y: 800, w: 200, h: 200 }, screen, PER), { vx: 1, vy: 1 });
  // Centre at (2500,2100) → floor 2/2 → (2,2).
  assert.deepEqual(viewportForRect({ x: 2400, y: 2000, w: 200, h: 200 }, screen, PER), { vx: 2, vy: 2 });
});

test('viewportForRect clamps a window pushed past the last cell', () => {
  // Centre way off-grid (>3 screens) clamps to the max cell, not 3/4/…
  assert.deepEqual(viewportForRect({ x: 9000, y: 9000, w: 100, h: 100 }, screen, PER), { vx: 2, vy: 2 });
  // Negative origin clamps to 0.
  assert.deepEqual(viewportForRect({ x: -500, y: -500, w: 100, h: 100 }, screen, PER), { vx: 0, vy: 0 });
});

test('viewportForRect uses the CENTRE, not the origin (a window straddling a boundary)', () => {
  // Origin in cell 0 but centre crosses into cell 1 on x.
  assert.deepEqual(viewportForRect({ x: 900, y: 0, w: 400, h: 100 }, screen, PER), { vx: 1, vy: 0 });
});

test('nextZ returns max+1, and 1 for an empty stack', () => {
  assert.equal(nextZ([]), 1);
  assert.equal(nextZ([{ z: 0 }, { z: 4 }, { z: 2 }]), 5);
  assert.equal(nextZ([{ z: 1 }]), 2);
});

test('sendToViewportRect moves by whole screens and keeps the offset within the cell', () => {
  // A window 40px into cell (0,0) sits 40px into whichever cell it is sent to.
  const r = { x: 40, y: 30, w: 300, h: 200 };
  assert.deepEqual(sendToViewportRect(r, screen, PER, 1, 0), { x: 1040, y: 30 });
  assert.deepEqual(sendToViewportRect(r, screen, PER, 2, 2), { x: 2040, y: 1630 });
  assert.deepEqual(sendToViewportRect(r, screen, PER, 0, 0), { x: 40, y: 30 }, 'its own cell is a no-op');
});

test('sendToViewportRect is relative to the cell the window is ON, not the origin', () => {
  // Already on (2,1); sending it to (0,0) must walk it back, not add.
  const r = { x: 2040, y: 830, w: 300, h: 200 };
  assert.deepEqual(sendToViewportRect(r, screen, PER, 0, 0), { x: 40, y: 30 });
});

test('sendToViewportRect clamps to the plane so a window is never orphaned', () => {
  // A window wider than a screen, sent to the last column, would hang off
  // the right edge of the plane where no viewport can reach its titlebar.
  // Only the axis that overflows is pulled back: this one is short, so its
  // y lands where the whole-screen move put it.
  const wide = { x: 0, y: 0, w: 1200, h: 200 };
  assert.deepEqual(sendToViewportRect(wide, screen, PER, 2, 2), { x: screen.w * PER - wide.w, y: 1600 });
  // And a window taller than a screen clamps on y the same way.
  const tall = { x: 0, y: 0, w: 300, h: 900 };
  assert.deepEqual(sendToViewportRect(tall, screen, PER, 2, 2), { x: 2000, y: screen.h * PER - tall.h });
});

test('clampToPlane pulls an off-plane origin back inside', () => {
  // Past the far corner — the exact shape the refresh bug produced.
  assert.deepEqual(clampToPlane({ x: 3100, y: 2500, w: 300, h: 200 }, screen, PER), { x: 2700, y: 2200 });
  // Negative origin clamps to 0.
  assert.deepEqual(clampToPlane({ x: -80, y: -40, w: 300, h: 200 }, screen, PER), { x: 0, y: 0 });
  // Already inside — untouched.
  assert.deepEqual(clampToPlane({ x: 40, y: 30, w: 300, h: 200 }, screen, PER), { x: 40, y: 30 });
});

test('isOrphaned is true only when no viewport can show any of the window', () => {
  // Entirely past the right edge of the plane.
  assert.equal(isOrphaned({ x: 3000, y: 100, w: 300, h: 200 }, screen, PER), true);
  // Entirely past the bottom.
  assert.equal(isOrphaned({ x: 100, y: 2400, w: 300, h: 200 }, screen, PER), true);
  // Entirely off the top-left.
  assert.equal(isOrphaned({ x: -300, y: 100, w: 300, h: 200 }, screen, PER), true);
  // Straddling the far edge is still reachable — leave it alone.
  assert.equal(isOrphaned({ x: 2900, y: 100, w: 300, h: 200 }, screen, PER), false);
  // Comfortably inside.
  assert.equal(isOrphaned({ x: 40, y: 30, w: 300, h: 200 }, screen, PER), false);
});

test('isOnScreen is true when any part of the window is in the camera cell', () => {
  const at = (vx: number, vy: number) => ({ vx, vy });
  // Wholly inside cell (0,0).
  assert.equal(isOnScreen({ x: 40, y: 30, w: 300, h: 200 }, screen, at(0, 0)), true);
  // Wholly inside (1,0), camera on (0,0): needs a pan.
  assert.equal(isOnScreen({ x: 1040, y: 30, w: 300, h: 200 }, screen, at(0, 0)), false);
  // Straddling the (0,0)/(1,0) edge is visible from BOTH — no pan either way,
  // because the user can already see it.
  const straddle = { x: 900, y: 30, w: 300, h: 200 };
  assert.equal(isOnScreen(straddle, screen, at(0, 0)), true);
  assert.equal(isOnScreen(straddle, screen, at(1, 0)), true);
  // Touching an edge exactly is not overlap.
  assert.equal(isOnScreen({ x: 1000, y: 30, w: 300, h: 200 }, screen, at(0, 0)), false);
  // Vertical axis too.
  assert.equal(isOnScreen({ x: 40, y: 830, w: 300, h: 200 }, screen, at(0, 0)), false);
  assert.equal(isOnScreen({ x: 40, y: 830, w: 300, h: 200 }, screen, at(0, 1)), true);
});

test('resizeRect: an east/south edge grows the size, origin fixed', () => {
  const r = { x: 100, y: 100, w: 400, h: 300 };
  assert.deepEqual(resizeRect(r, 'se', 50, 20), { x: 100, y: 100, w: 450, h: 320 });
  assert.deepEqual(resizeRect(r, 'e', 50, 20), { x: 100, y: 100, w: 450, h: 300 });
  assert.deepEqual(resizeRect(r, 's', 50, 20), { x: 100, y: 100, w: 400, h: 320 });
});

test('resizeRect: a west/north edge moves the origin, the far edge stays', () => {
  const r = { x: 100, y: 100, w: 400, h: 300 };
  assert.deepEqual(resizeRect(r, 'nw', -30, -40), { x: 70, y: 60, w: 430, h: 340 });
  assert.deepEqual(resizeRect(r, 'ne', 30, 40), { x: 100, y: 140, w: 430, h: 260 });
  assert.deepEqual(resizeRect(r, 'sw', 30, 40), { x: 130, y: 100, w: 370, h: 340 });
});

test('resizeRect: the minimum size stops the origin, not the far edge', () => {
  const r = { x: 100, y: 100, w: 400, h: 300 };
  const g = resizeRect(r, 'nw', 1000, 1000);
  assert.deepEqual(g, { x: 340, y: 320, w: 160, h: 80 });
  assert.equal(g.x + g.w, r.x + r.w);
  assert.equal(g.y + g.h, r.y + r.h);
});

test('resizeRect: never past the plane top-left', () => {
  const r = { x: 20, y: 10, w: 400, h: 300 };
  assert.deepEqual(resizeRect(r, 'nw', -100, -100), { x: 0, y: 0, w: 420, h: 310 });
});
