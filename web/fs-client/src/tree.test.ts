// Unit tests for the visible-tree flattening.
// Run with: node --test web/fs-client/src/tree.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { flattenTree, type TreeInput } from './tree.ts';
import type { SortableEntry, SortOptions } from './sort.ts';

// Minimal entry factory — flattenTree reads name + type; the rest are
// there to satisfy SortableEntry (and let sort run).
function ent(name: string, type: 'dir' | 'file' = 'file'): SortableEntry {
  return { name, type, size: 0, mod_unix: 0, created_unix: 0 };
}

const sortNameAsc: SortOptions = { key: 'name', desc: false, showHidden: false };

// Convenience: build input with defaults, run, return rows.
function run(over: Partial<TreeInput<SortableEntry>>) {
  const input: TreeInput<SortableEntry> = {
    listings: {},
    expanded: {},
    sort: sortNameAsc,
    start: '/',
    cur: '',
    ...over,
  };
  return flattenTree(input);
}

// Compact view of a row: [path, depth, childCount].
const shape = (rows: ReturnType<typeof run>) =>
  rows.map((r) => [r.path, r.depth, r.childCount] as const);

test('flattens a single listed dir, dirs sorted before files, depth 0', () => {
  const rows = run({ listings: { '/': [ent('b.txt'), ent('adir', 'dir'), ent('a.txt')] } });
  assert.deepEqual(shape(rows), [
    ['/adir', 0, undefined], // dir first; not listed → no childCount
    ['/a.txt', 0, undefined],
    ['/b.txt', 0, undefined],
  ]);
});

test('an expanded listed dir descends, children at depth+1', () => {
  const rows = run({
    listings: { '/': [ent('a', 'dir')], '/a': [ent('x'), ent('y')] },
    expanded: { '/a': true },
  });
  assert.deepEqual(shape(rows), [
    ['/a', 0, 2], // childCount = listings['/a'].length
    ['/a/x', 1, undefined],
    ['/a/y', 1, undefined],
  ]);
});

test('a collapsed dir is rendered but not descended (childCount still shown)', () => {
  const rows = run({
    listings: { '/': [ent('a', 'dir')], '/a': [ent('x')] },
    expanded: {}, // collapsed
  });
  assert.deepEqual(shape(rows), [['/a', 0, 1]]);
});

test('childCount counts ALL entries including hidden, even when showHidden is false', () => {
  const rows = run({
    listings: { '/': [ent('a', 'dir')], '/a': [ent('.dot'), ent('y')] },
    expanded: { '/a': true },
    sort: { key: 'name', desc: false, showHidden: false },
  });
  // childCount = 2 (counts .dot); but the rendered child rows drop .dot.
  assert.deepEqual(shape(rows), [
    ['/a', 0, 2],
    ['/a/y', 1, undefined],
  ]);
});

test('showHidden surfaces dotfile rows', () => {
  const rows = run({
    listings: { '/': [ent('.rc'), ent('z')] },
    sort: { key: 'name', desc: false, showHidden: true },
  });
  assert.deepEqual(shape(rows), [['/.rc', 0, undefined], ['/z', 0, undefined]]);
});

test('an unlisted start yields no rows (and no fallback when cur is empty)', () => {
  assert.deepEqual(run({ listings: {}, start: '/x' }), []);
});

test('fallback bridge: renders cur’s listed neighbourhood when the main walk cannot reach it', () => {
  // start "/" is not listed (its list_ok is still in flight), but the
  // user navigated to /a/b which IS listed — bridge it in at depth =
  // segs(/a/b) - segs(/) = 2.
  const rows = run({
    listings: { '/a/b': [ent('leaf')] },
    start: '/',
    cur: '/a/b',
  });
  assert.deepEqual(shape(rows), [['/a/b/leaf', 2, undefined]]);
});

test('fallback does not double-render when the main walk already reached cur', () => {
  const rows = run({
    listings: { '/': [ent('a', 'dir')], '/a': [ent('x')] },
    expanded: { '/a': true },
    start: '/',
    cur: '/a',
  });
  assert.deepEqual(shape(rows), [['/a', 0, 1], ['/a/x', 1, undefined]]);
});

test('fallback depth is relative to a non-root start (sandbox tree root)', () => {
  // Sandbox: tree rooted at /a (listed), but /a/b is not listed yet so
  // the main walk stops; cur=/a/b/c is listed → bridge at depth
  // segs(/a/b/c) - segs(/a) = 3 - 1 = 2.
  const rows = run({
    listings: { '/a': [ent('b', 'dir')], '/a/b/c': [ent('deep')] },
    start: '/a',
    cur: '/a/b/c',
  });
  assert.deepEqual(shape(rows), [
    ['/a/b', 0, undefined], // main walk: /a's child b at depth 0 (no childCount: /a/b unlisted)
    ['/a/b/c/deep', 2, undefined], // bridge from /a/b/c at relative depth 2
  ]);
});
