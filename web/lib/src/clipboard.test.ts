// Unit tests for the clipboard model's one rule (docs/CLIPBOARD.md): a
// paste carries this computer's clipboard, and "last change wins" picks
// between it and the wash clipboard. Framework-free: a fake window.wash
// and no localStorage (the memory fallback). Run via `make fe-unit`.

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { markSystemSeen, resolvePaste, systemSeen, washSetSelection } from './clipboard.ts';

interface Slot { mime: string; text: string }

function installWash(clip: Slot, sel: Slot = { mime: '', text: '' }) {
  const sets: string[] = [];
  const selSets: string[] = [];
  (globalThis as unknown as { window: unknown }).window = {
    wash: {
      clipboardPeek: () => clip,
      clipboardSetText: (t: string) => { sets.push(t); clip = { mime: 'text/plain', text: t }; },
      selectionPeek: () => sel,
      selectionSetText: (t: string) => { selSets.push(t); sel = { mime: 'text/plain', text: t }; },
    },
  };
  return { sets, selSets };
}

test('a paste of something copied outside wash adopts it', () => {
  markSystemSeen('old system text');
  const { sets } = installWash({ mime: 'text/plain', text: 'wash copy' });
  assert.equal(resolvePaste('copied in a desktop app'), 'copied in a desktop app');
  assert.deepEqual(sets, ['copied in a desktop app'], 'it becomes the wash clipboard');
  assert.equal(systemSeen(), 'copied in a desktop app');
});

test('an unchanged system clipboard yields to a newer wash copy (another box)', () => {
  markSystemSeen('stale on this box');
  const { sets } = installWash({ mime: 'text/plain', text: 'copied on box A' });
  assert.equal(resolvePaste('stale on this box'), 'copied on box A');
  assert.deepEqual(sets, [], 'nothing adopted');
});

test('the first paste a browser sees counts as a change', () => {
  // Nothing marked yet in this run's fallback beyond what earlier tests
  // set, so use text no test has marked.
  installWash({ mime: 'text/plain', text: 'wash' });
  assert.equal(resolvePaste('never seen before'), 'never seen before');
});

test('a non-text wash clipboard (files, an image) leaves the system text', () => {
  markSystemSeen('system text');
  installWash({ mime: 'application/x-wash-paths', text: '{"op":"cut","paths":["/a"]}' });
  assert.equal(resolvePaste('system text'), 'system text');
  installWash({ mime: 'image/png', text: '' });
  assert.equal(resolvePaste('system text'), 'system text');
});

test('the selection drops repeats and never touches the clipboard', () => {
  const { sets, selSets } = installWash({ mime: 'text/plain', text: 'clip' });
  washSetSelection('highlighted');
  washSetSelection('highlighted');
  washSetSelection('');
  assert.deepEqual(selSets, ['highlighted']);
  assert.deepEqual(sets, []);
});
