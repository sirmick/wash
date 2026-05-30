// Unit tests for the back/forward navigation history reducer.
// Run with: node --test apps/fm/fe/src/nav-history.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  emptyHistory,
  initAt,
  pushPath,
  canBack,
  canForward,
  back,
  forward,
  at,
} from './nav-history.ts';

test('emptyHistory has no entries and idx before the start', () => {
  const h = emptyHistory();
  assert.deepEqual(h, { entries: [], idx: -1 });
  assert.equal(canBack(h), false);
  assert.equal(canForward(h), false);
});

test('initAt seeds a single entry at idx 0', () => {
  const h = initAt('/home');
  assert.deepEqual(h, { entries: ['/home'], idx: 0 });
  assert.equal(canBack(h), false);
  assert.equal(canForward(h), false);
});

test('pushPath appends and advances idx to the new entry', () => {
  let h = initAt('/a');
  h = pushPath(h, '/b');
  assert.deepEqual(h, { entries: ['/a', '/b'], idx: 1 });
  h = pushPath(h, '/c');
  assert.deepEqual(h, { entries: ['/a', '/b', '/c'], idx: 2 });
});

test('pushPath after going back discards the forward entries (branch)', () => {
  // /a → /b → /c, then back to /b, then navigate to /x:
  // /c is discarded, /x becomes the new head.
  let h = initAt('/a');
  h = pushPath(h, '/b');
  h = pushPath(h, '/c');
  h = at(h, back(h)!.idx); // now at /b (idx 1)
  h = pushPath(h, '/x');
  assert.deepEqual(h, { entries: ['/a', '/b', '/x'], idx: 2 });
  assert.equal(canForward(h), false);
});

test('back returns the previous entry without mutating, forward is then possible', () => {
  let h = initAt('/a');
  h = pushPath(h, '/b');
  const move = back(h);
  assert.deepEqual(move, { idx: 0, path: '/a' });
  // back() did not mutate h (still pointing at /b)
  assert.equal(h.idx, 1);
  // applying the move and checking forward availability
  const moved = at(h, move!.idx);
  assert.equal(canBack(moved), false);
  assert.equal(canForward(moved), true);
});

test('back at the oldest entry returns null', () => {
  const h = initAt('/a');
  assert.equal(back(h), null);
});

test('forward returns the next entry; null at the newest', () => {
  let h = initAt('/a');
  h = pushPath(h, '/b');
  const atOldest = at(h, 0);
  assert.deepEqual(forward(atOldest), { idx: 1, path: '/b' });
  assert.equal(forward(h), null); // already at the newest
});

test('back then forward round-trips to the same location', () => {
  let h = initAt('/a');
  h = pushPath(h, '/b');
  h = pushPath(h, '/c'); // at /c (idx 2)
  h = at(h, back(h)!.idx); // /b (idx 1)
  h = at(h, back(h)!.idx); // /a (idx 0)
  assert.equal(h.idx, 0);
  h = at(h, forward(h)!.idx); // /b
  h = at(h, forward(h)!.idx); // /c
  assert.deepEqual(h, { entries: ['/a', '/b', '/c'], idx: 2 });
});

test('at returns a new wrapper sharing the (never-mutated) entries array', () => {
  // The reducer treats entries as immutable — pushPath always slices a
  // fresh array — so at() can share the reference cheaply without risk.
  const h = initAt('/a');
  const moved = at(h, 0);
  assert.notEqual(moved, h); // new wrapper object
  assert.equal(moved.entries, h.entries); // same array, intentionally
});
