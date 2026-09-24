// Run with: node --test --conditions=browser web/shell/src/display-frames.test.ts
// (the fe-unit make target picks it up).

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { SerialQueue, normalizeWheel } from './display-frames.ts';

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

test('SerialQueue runs steps in submission order even when an earlier one is slower', async () => {
  const q = new SerialQueue();
  const order: string[] = [];
  // A "big full frame" that takes 30ms to decode, then a "tiny dirty rect"
  // that decodes instantly. Unordered, the tiny one would draw first and the
  // big one would then overwrite it with older pixels.
  q.enqueue(async () => {
    await sleep(30);
    order.push('full');
  });
  const last = q.enqueue(async () => {
    order.push('dirty');
  });
  assert.equal(q.pending, 2);
  await last;
  assert.deepEqual(order, ['full', 'dirty']);
  assert.equal(q.pending, 0);
});

test('SerialQueue keeps flowing after a step throws or rejects', async () => {
  const q = new SerialQueue();
  const order: string[] = [];
  q.enqueue(() => {
    throw new Error('sync boom');
  });
  q.enqueue(async () => {
    throw new Error('async boom');
  });
  await q.enqueue(async () => {
    order.push('after');
  });
  assert.deepEqual(order, ['after']);
});

test('normalizeWheel converts line and page deltas to pixels and emits notch counts', () => {
  // Chromium: pixel mode, one notch = 120px.
  assert.deepEqual(normalizeWheel({ deltaX: 0, deltaY: 120, deltaMode: 0 }, 800), [
    { axis: 'v', delta: 120, notches: 1 },
  ]);
  // Firefox: line mode, 3 lines per notch → 120px, 1 notch.
  assert.deepEqual(normalizeWheel({ deltaX: 0, deltaY: 3, deltaMode: 1 }, 800), [
    { axis: 'v', delta: 120, notches: 1 },
  ]);
  // Page mode uses the viewport height.
  assert.deepEqual(normalizeWheel({ deltaX: 0, deltaY: -1, deltaMode: 2 }, 600), [
    { axis: 'v', delta: -600, notches: -5 },
  ]);
  // Horizontal rides alongside, and a zero axis is omitted.
  assert.deepEqual(normalizeWheel({ deltaX: -240, deltaY: 0, deltaMode: 0 }, 800), [
    { axis: 'h', delta: -240, notches: -2 },
  ]);
  // A sub-notch trackpad tick still carries its pixel delta with 0 notches.
  assert.deepEqual(normalizeWheel({ deltaX: 0, deltaY: 20, deltaMode: 0 }, 800), [
    { axis: 'v', delta: 20, notches: 0 },
  ]);
});
