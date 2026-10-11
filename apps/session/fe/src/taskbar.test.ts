// Unit tests for the taskbar's pure decision (taskbar.ts): whether a click
// on a window's pill has to move the camera before focusing it.
//
// Run: node --test --conditions=browser apps/session/fe/src/taskbar.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { panToReveal, type PlacedWindow } from './taskbar.ts';

const screen = { w: 1000, h: 800 };
const win = (over: Partial<PlacedWindow>): PlacedWindow => ({
  x: 100,
  y: 100,
  w: 400,
  h: 300,
  state: 'normal',
  viewport: { vx: 0, vy: 0 },
  ...over,
});

test('a window in the current cell leaves the camera alone', () => {
  assert.equal(panToReveal(win({}), { vx: 0, vy: 0 }, screen), null);
});

test('a window in another cell pans the camera to its home cell', () => {
  // The bug: focusing it from (2,2) raised it where nobody could see it.
  const w = win({ x: 100, y: 100, viewport: { vx: 0, vy: 0 } });
  assert.deepEqual(panToReveal(w, { vx: 2, vy: 2 }, screen), { vx: 0, vy: 0 });
});

test('a window hanging into the current cell from next door is already visible', () => {
  // Centre in (1,0) so that is its home, but its left edge reaches into (0,0).
  const w = win({ x: 900, y: 100, w: 400, viewport: { vx: 1, vy: 0 } });
  assert.equal(panToReveal(w, { vx: 0, vy: 0 }, screen), null);
});

test('a minimized window is judged by where it will reappear', () => {
  const w = win({ state: 'minimized', x: 1200, y: 100, viewport: { vx: 1, vy: 0 } });
  assert.deepEqual(panToReveal(w, { vx: 0, vy: 0 }, screen), { vx: 1, vy: 0 });
  assert.equal(panToReveal(w, { vx: 1, vy: 0 }, screen), null);
});

test('a maximized window is judged by its cell, not its restore rect', () => {
  // Restore rect reaches into (0,0), but maximized it fills (1,0) only.
  const w = win({ state: 'maximized', x: 900, y: 100, w: 400, viewport: { vx: 1, vy: 0 } });
  assert.deepEqual(panToReveal(w, { vx: 0, vy: 0 }, screen), { vx: 1, vy: 0 });
  assert.equal(panToReveal(w, { vx: 1, vy: 0 }, screen), null);
});
