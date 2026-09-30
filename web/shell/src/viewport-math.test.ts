import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { clampViewport, clampToPlane, isOrphaned, viewportForRect, nextZ, sendToViewportRect } from './viewport-math.ts';

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
