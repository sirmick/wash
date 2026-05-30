// Unit tests for the files-clipboard pure logic.
// Run with: node --test apps/fm/fe/src/clipboard.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { parseClipboardState, planPaste, type ClipboardState } from './clipboard.ts';

// ---- parseClipboardState ----

test('parseClipboardState accepts a copy with paths', () => {
  assert.deepEqual(parseClipboardState('copy', ['/a', '/b']), { op: 'copy', paths: ['/a', '/b'] });
});

test('parseClipboardState accepts a cut with paths', () => {
  assert.deepEqual(parseClipboardState('cut', ['/a']), { op: 'cut', paths: ['/a'] });
});

test('parseClipboardState returns null for an empty path list (cleared clipboard)', () => {
  assert.equal(parseClipboardState('copy', []), null);
});

test('parseClipboardState returns null for an unknown/absent op', () => {
  assert.equal(parseClipboardState('', ['/a']), null);
  assert.equal(parseClipboardState(undefined, ['/a']), null);
  assert.equal(parseClipboardState('link', ['/a']), null);
});

test('parseClipboardState treats a non-array paths value as empty → null', () => {
  assert.equal(parseClipboardState('copy', undefined), null);
  assert.equal(parseClipboardState('copy', null), null);
});

// ---- planPaste ----

const cb = (op: 'copy' | 'cut', paths: string[]): ClipboardState => ({ op, paths });

test('planPaste maps copy → bulk copy, no clear', () => {
  assert.deepEqual(planPaste(cb('copy', ['/a', '/b']), '/dest'), {
    op: 'copy', paths: ['/a', '/b'], dest: '/dest', clearAfter: false,
  });
});

test('planPaste maps cut → bulk move, clears afterwards (one-shot)', () => {
  assert.deepEqual(planPaste(cb('cut', ['/a']), '/dest'), {
    op: 'move', paths: ['/a'], dest: '/dest', clearAfter: true,
  });
});

test('planPaste returns null when there is no clipboard', () => {
  assert.equal(planPaste(null, '/dest'), null);
});

test('planPaste returns null when the clipboard is empty', () => {
  assert.equal(planPaste(cb('copy', []), '/dest'), null);
});

test('planPaste returns null when there is no destination', () => {
  assert.equal(planPaste(cb('copy', ['/a']), ''), null);
});

test('parse → plan round-trip: a cut push becomes a move plan', () => {
  const state = parseClipboardState('cut', ['/x', '/y']);
  assert.deepEqual(planPaste(state, '/into'), {
    op: 'move', paths: ['/x', '/y'], dest: '/into', clearAfter: true,
  });
});
