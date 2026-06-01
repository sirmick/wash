import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { clampViewport, viewportForRect, nextZ } from './viewport-math.ts';

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
