import { test } from 'node:test';
import assert from 'node:assert/strict';
import { nextSelection, rekeyPath, rekeySelection, successorAfterRemoval, type SelectionState } from './selection.ts';

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

// ---- rekeyPath / rekeySelection (post-rename re-keying) ----

test('rekeyPath maps the renamed path itself', () => {
  assert.equal(rekeyPath('/a/old', '/a/old', '/a/new'), '/a/new');
});

test('rekeyPath maps descendants of a renamed dir and leaves others alone', () => {
  assert.equal(rekeyPath('/a/old/x/y', '/a/old', '/a/new'), '/a/new/x/y');
  assert.equal(rekeyPath('/a/older', '/a/old', '/a/new'), null, 'prefix match must be on a path boundary');
  assert.equal(rekeyPath('/b', '/a/old', '/a/new'), null);
});

test('rekeySelection re-keys affected members and returns a new set', () => {
  const prev = sel('/a/old', '/a/old/k', '/a/other');
  const r = rekeySelection(prev, '/a/old', '/a/new');
  assert.deepEqual([...r].sort(), ['/a/new', '/a/new/k', '/a/other']);
  assert.deepEqual([...prev].sort(), ['/a/old', '/a/old/k', '/a/other'], 'input untouched');
});

// ---- successorAfterRemoval (post-delete selection) ----

test('successor is the next sibling in display order', () => {
  assert.equal(successorAfterRemoval(ROWS, '/b'), '/c');
});

test('successor of the last sibling is the previous sibling', () => {
  assert.equal(successorAfterRemoval(ROWS, '/e'), '/d');
});

test('successor skips the removed dir\'s own children and stays in its folder', () => {
  const rows = ['/x', '/x/1', '/x/2', '/y', '/y/1', '/z'];
  assert.equal(successorAfterRemoval(rows, '/x'), '/y');
  // /y/1 is the last (only) entry in /y: the next visible row /z is not a
  // sibling, and there is no previous sibling → nothing.
  assert.equal(successorAfterRemoval(rows, '/y/1'), null);
});

test('successor of an only child is null; unknown row is null', () => {
  assert.equal(successorAfterRemoval(['/solo'], '/solo'), null);
  assert.equal(successorAfterRemoval(ROWS, '/nope'), null);
});
