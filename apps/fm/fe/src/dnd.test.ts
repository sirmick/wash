// Unit tests for the drag payload parsing + drop-accept logic.
// Run with: node --test apps/fm/fe/src/dnd.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  DRAG_MIME,
  dragPayload,
  dropEffectFor,
  hasWashDrag,
  readDragPaths,
  type DragData,
} from './dnd.ts';

// fakeTransfer builds a minimal DataTransfer-shaped object: a map of
// format → string plus the corresponding `types` list, so the parsing
// can be exercised without a real DragEvent.
function fakeTransfer(data: Record<string, string>): DragData {
  return {
    getData: (format) => data[format] ?? '',
    types: Object.keys(data),
  };
}

// ---- readDragPaths ----

test('readDragPaths returns the JSON array carried under DRAG_MIME', () => {
  const dt = fakeTransfer({ [DRAG_MIME]: JSON.stringify(['/a', '/b/c']) });
  assert.deepEqual(readDragPaths(dt), ['/a', '/b/c']);
});

test('readDragPaths returns [] for a null/undefined transfer', () => {
  assert.deepEqual(readDragPaths(null), []);
  assert.deepEqual(readDragPaths(undefined), []);
});

test('readDragPaths returns [] when the MIME is absent (non-wash drag)', () => {
  const dt = fakeTransfer({ 'text/plain': '/a\n/b' });
  assert.deepEqual(readDragPaths(dt), []);
});

test('readDragPaths returns [] for malformed JSON', () => {
  const dt = fakeTransfer({ [DRAG_MIME]: '{not json' });
  assert.deepEqual(readDragPaths(dt), []);
});

test('readDragPaths returns [] when the payload is JSON but not an array', () => {
  const dt = fakeTransfer({ [DRAG_MIME]: JSON.stringify({ path: '/a' }) });
  assert.deepEqual(readDragPaths(dt), []);
});

test('readDragPaths filters out non-string array members defensively', () => {
  const dt = fakeTransfer({ [DRAG_MIME]: JSON.stringify(['/a', 42, null, '/b']) });
  assert.deepEqual(readDragPaths(dt), ['/a', '/b']);
});

// ---- hasWashDrag ----

test('hasWashDrag is true only when the drag carries our MIME', () => {
  assert.equal(hasWashDrag(fakeTransfer({ [DRAG_MIME]: '[]' })), true);
  assert.equal(hasWashDrag(fakeTransfer({ 'text/plain': 'x' })), false);
  assert.equal(hasWashDrag(null), false);
  assert.equal(hasWashDrag(undefined), false);
});

// ---- dropEffectFor ----

test('dropEffectFor maps the alt modifier to copy, otherwise move', () => {
  assert.equal(dropEffectFor(true), 'copy');
  assert.equal(dropEffectFor(false), 'move');
});

// ---- dragPayload ----

test('dragPayload carries the whole selection when the dragged row is in it', () => {
  const sel = new Set(['/a', '/b', '/c']);
  assert.deepEqual(dragPayload('/b', sel).sort(), ['/a', '/b', '/c']);
});

test('dragPayload carries just the row when it is not part of the selection', () => {
  const sel = new Set(['/a', '/b']);
  assert.deepEqual(dragPayload('/z', sel), ['/z']);
});

test('dragPayload carries just the row when the selection is empty', () => {
  assert.deepEqual(dragPayload('/z', new Set()), ['/z']);
});

// ---- round-trip: dragPayload → JSON → readDragPaths ----

test('a payload survives the JSON round-trip back through readDragPaths', () => {
  const sel = new Set(['/x/1', '/x/2']);
  const paths = dragPayload('/x/1', sel);
  const dt = fakeTransfer({ [DRAG_MIME]: JSON.stringify(paths) });
  assert.deepEqual(readDragPaths(dt).sort(), ['/x/1', '/x/2']);
});
