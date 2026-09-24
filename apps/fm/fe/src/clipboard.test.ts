// Unit tests for the files-clipboard pure logic.
// Run with: node --test --conditions=browser apps/fm/fe/src/clipboard.test.ts
// (or `make fe-unit`).

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { parseClipboardState, planPaste, pasteStatus, type ClipboardState } from './clipboard.ts';

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
    op: 'copy', paths: ['/a', '/b'], dest: '/dest', clearAfter: false, skipped: [],
  });
});

test('planPaste maps cut → bulk move, clears afterwards (one-shot)', () => {
  assert.deepEqual(planPaste(cb('cut', ['/a']), '/dest'), {
    op: 'move', paths: ['/a'], dest: '/dest', clearAfter: true, skipped: [],
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
    op: 'move', paths: ['/x', '/y'], dest: '/into', clearAfter: true, skipped: [],
  });
});

// ---- planPaste: the data-loss shapes are filtered before dispatch ----

test('planPaste drops a same-folder paste (copy) — nothing to dispatch', () => {
  // Ctrl+C /d/a.txt, Ctrl+V while still in /d: dst would equal src and a
  // Replace answer would delete the only copy.
  const plan = planPaste(cb('copy', ['/d/a.txt']), '/d');
  assert.deepEqual(plan, {
    op: 'copy', paths: [], dest: '/d', clearAfter: false,
    skipped: [{ path: '/d/a.txt', reason: 'same-folder' }],
  });
});

test('planPaste drops a same-folder paste (cut) and KEEPS the clipboard', () => {
  const plan = planPaste(cb('cut', ['/d/a.txt']), '/d');
  assert.equal(plan?.paths.length, 0);
  assert.equal(plan?.clearAfter, false, 'nothing moved → a later paste elsewhere must still work');
});

test('planPaste same-folder filtering is per entry; the rest proceeds', () => {
  const plan = planPaste(cb('cut', ['/d/a.txt', '/e/b.txt', '/d/sub']), '/d');
  assert.deepEqual(plan, {
    op: 'move', paths: ['/e/b.txt'], dest: '/d', clearAfter: true,
    skipped: [
      { path: '/d/a.txt', reason: 'same-folder' },
      { path: '/d/sub', reason: 'same-folder' },
    ],
  });
});

test('planPaste same-folder at the root', () => {
  assert.deepEqual(planPaste(cb('copy', ['/a']), '/')?.skipped, [{ path: '/a', reason: 'same-folder' }]);
});

test('planPaste refuses pasting a folder into itself', () => {
  const plan = planPaste(cb('copy', ['/d']), '/d');
  assert.deepEqual(plan?.skipped, [{ path: '/d', reason: 'into-self' }]);
  assert.deepEqual(plan?.paths, []);
});

test('planPaste refuses pasting a folder into its own subfolder', () => {
  for (const op of ['copy', 'cut'] as const) {
    const plan = planPaste(cb(op, ['/d']), '/d/sub/deeper');
    assert.deepEqual(plan?.skipped, [{ path: '/d', reason: 'into-self' }], op);
    assert.deepEqual(plan?.paths, [], op);
    assert.equal(plan?.clearAfter, false, op);
  }
});

test('planPaste into-self refuses the WHOLE paste (mirrors bulkops reject-whole-job)', () => {
  const plan = planPaste(cb('copy', ['/e/ok.txt', '/d']), '/d/sub');
  assert.deepEqual(plan?.paths, [], 'the valid sibling must not be dispatched either');
  assert.deepEqual(plan?.skipped, [{ path: '/d', reason: 'into-self' }]);
});

test('planPaste subtree check is separator-aware: /d-2 is not inside /d', () => {
  assert.deepEqual(planPaste(cb('copy', ['/d']), '/d-2'), {
    op: 'copy', paths: ['/d'], dest: '/d-2', clearAfter: false, skipped: [],
  });
});

test('planPaste: everything is inside the root', () => {
  assert.deepEqual(planPaste(cb('copy', ['/']), '/x')?.skipped, [{ path: '/', reason: 'into-self' }]);
});

// ---- pasteStatus ----

test('pasteStatus is null when nothing was skipped', () => {
  assert.equal(pasteStatus(planPaste(cb('copy', ['/a']), '/dest')!), null);
});

test('pasteStatus reports into-self as an error naming the folder', () => {
  assert.deepEqual(pasteStatus(planPaste(cb('copy', ['/d/photos']), '/d/photos/2024')!), {
    kind: 'error', text: 'paste: cannot copy photos into itself',
  });
  assert.deepEqual(pasteStatus(planPaste(cb('cut', ['/d/photos']), '/d/photos')!), {
    kind: 'error', text: 'paste: cannot move photos into itself',
  });
});

test('pasteStatus reports same-folder as info, singular and plural', () => {
  assert.deepEqual(pasteStatus(planPaste(cb('copy', ['/d/a.txt']), '/d')!), {
    kind: 'info', text: 'paste: a.txt is already in this folder',
  });
  assert.deepEqual(pasteStatus(planPaste(cb('cut', ['/d/a.txt', '/d/b.txt', '/e/c']), '/d')!), {
    kind: 'info', text: 'paste: 2 items are already in this folder',
  });
});
