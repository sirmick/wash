// Run with: node --test --conditions=browser web/shell/src/mouse-chord.test.ts
import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { ChordFilter, type ButtonEdge } from './mouse-chord.ts';

// A filter on a hand-cranked clock: fire() runs the pending timer.
function rig() {
  const out: string[] = [];
  let timer: (() => void) | null = null;
  const f = new ChordFilter(
    (e: ButtonEdge) => out.push(`${e.btn} ${e.state}`),
    60,
    (fn) => { timer = fn; return 1 as unknown as ReturnType<typeof setTimeout>; },
    () => { timer = null; },
  );
  return { f, out, fire: () => { const t = timer; timer = null; t?.(); } };
}

test('left then right inside the window is one middle-click', () => {
  const { f, out } = rig();
  f.down('left');
  f.down('right');
  f.up('left');
  assert.deepEqual(out, ['middle down'], 'still held by one button');
  f.up('right');
  assert.deepEqual(out, ['middle down', 'middle up']);
});

test('right then left works the same', () => {
  const { f, out } = rig();
  f.down('right');
  f.down('left');
  f.up('right');
  f.up('left');
  assert.deepEqual(out, ['middle down', 'middle up']);
});

test('a lone click goes through as itself, down before up', () => {
  const { f, out } = rig();
  f.down('left');
  assert.deepEqual(out, [], 'held while a chord is still possible');
  f.up('left');
  assert.deepEqual(out, ['left down', 'left up']);
});

test('a held press is let through when the window closes or the pointer moves', () => {
  const { f, out, fire } = rig();
  f.down('right');
  fire();
  assert.deepEqual(out, ['right down']);
  f.down('left');
  f.up('left');
  f.up('right');
  assert.deepEqual(out, ['right down', 'left down', 'left up', 'right up'], 'too late for a chord');

  const b = rig();
  b.f.down('left');
  b.f.moved();
  assert.deepEqual(b.out, ['left down'], 'a drag starts at once');
});

test('a real middle button is never held', () => {
  const { f, out } = rig();
  f.down('middle');
  f.up('middle');
  assert.deepEqual(out, ['middle down', 'middle up']);
});
