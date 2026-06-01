import { test } from 'node:test';
import assert from 'node:assert/strict';
import { nextSelection, type SelectionState } from './selection.ts';

const ROWS = ['/a', '/b', '/c', '/d', '/e'];
const empty: SelectionState = { selection: new Set(), anchor: null };
const sel = (...p: string[]) => new Set(p);
const plain = { shift: false, ctrlOrMeta: false };
const ctrl = { shift: false, ctrlOrMeta: true };
const shift = { shift: true, ctrlOrMeta: false };

test('plain click selects just the row and moves the anchor', () => {
  const r = nextSelection({ selection: sel('/a', '/b'), anchor: '/a' }, '/c', ROWS, plain);
  assert.deepEqual([...r.selection], ['/c']);
  assert.equal(r.anchor, '/c');
});

test('ctrl-click adds an unselected row and moves the anchor', () => {
  const r = nextSelection({ selection: sel('/a'), anchor: '/a' }, '/c', ROWS, ctrl);
  assert.deepEqual([...r.selection].sort(), ['/a', '/c']);
  assert.equal(r.anchor, '/c');
});

test('ctrl-click removes an already-selected row (toggle off)', () => {
  const r = nextSelection({ selection: sel('/a', '/c'), anchor: '/a' }, '/c', ROWS, ctrl);
  assert.deepEqual([...r.selection], ['/a']);
  assert.equal(r.anchor, '/c');
});

test('ctrl-click does not mutate the previous selection set', () => {
  const prev = sel('/a');
  nextSelection({ selection: prev, anchor: '/a' }, '/c', ROWS, ctrl);
  assert.deepEqual([...prev], ['/a']); // unchanged
});

test('shift-click ranges anchor→target downward (inclusive)', () => {
  const r = nextSelection({ selection: sel('/b'), anchor: '/b' }, '/d', ROWS, shift);
  assert.deepEqual([...r.selection], ['/b', '/c', '/d']);
  assert.equal(r.anchor, '/b', 'anchor is preserved so the range can grow');
});

test('shift-click ranges target→anchor upward (inclusive, order-normalised)', () => {
  const r = nextSelection({ selection: sel('/d'), anchor: '/d' }, '/b', ROWS, shift);
  assert.deepEqual([...r.selection], ['/b', '/c', '/d']);
  assert.equal(r.anchor, '/d');
});

test('shift-click re-pivots on the same anchor (range shrinks/grows, anchor stays)', () => {
  const start: SelectionState = { selection: sel('/b'), anchor: '/b' };
  const grown = nextSelection(start, '/e', ROWS, shift);
  assert.deepEqual([...grown.selection], ['/b', '/c', '/d', '/e']);
  const shrunk = nextSelection(grown, '/c', ROWS, shift);
  assert.deepEqual([...shrunk.selection], ['/b', '/c']);
  assert.equal(shrunk.anchor, '/b');
});

test('shift-click with no anchor degrades to single-select and sets the anchor', () => {
  const r = nextSelection(empty, '/c', ROWS, shift);
  assert.deepEqual([...r.selection], ['/c']);
  assert.equal(r.anchor, '/c');
});

test('shift-click whose anchor scrolled out of the visible rows single-selects, anchor untouched', () => {
  const r = nextSelection({ selection: sel('/gone'), anchor: '/gone' }, '/c', ROWS, shift);
  assert.deepEqual([...r.selection], ['/c']);
  assert.equal(r.anchor, '/gone', 'stale anchor is left as-is, matching the prior inline behaviour');
});

test('shift takes precedence over ctrl when both are held', () => {
  const r = nextSelection({ selection: sel('/a'), anchor: '/b' }, '/d', ROWS, { shift: true, ctrlOrMeta: true });
  assert.deepEqual([...r.selection], ['/b', '/c', '/d']); // range, not toggle
});
