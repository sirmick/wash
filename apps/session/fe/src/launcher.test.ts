// Unit tests for the launcher's pure decisions (launcher.ts): recent-row
// naming, palette merging of apps + recent files, pin resolution, and the
// arrow-key model the start menu and palette share.
//
// Run: node --test --conditions=browser apps/session/fe/src/launcher.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  RECENT_PREFIX,
  appMatches,
  paletteEntries,
  pinnedRows,
  recentDir,
  recentMatches,
  recentName,
  recentPathOf,
  recentRowID,
  stepSelection,
  type RecentEntry,
} from './launcher.ts';

const apps = [
  { id: 'com.wash.term', name: 'Terminal', icon: 'terminal' },
  { id: 'com.wash.fm', name: 'Files', icon: 'folder' },
  { id: 'com.wash.edit', name: 'Editor', icon: 'file-pen' },
];

const recent: RecentEntry[] = [
  { path: '/home/u/notes.md', app_id: 'com.wash.edit', at: 3 },
  { path: '/home/u/pics/photo.png', app_id: 'com.wash.imageview', at: 2 },
  { path: '/srv/', app_id: 'com.wash.fm', at: 1 },
];

test('recent row ids round-trip and never collide with app ids', () => {
  const id = recentRowID('/home/u/notes.md');
  assert.equal(id, `${RECENT_PREFIX}/home/u/notes.md`);
  assert.equal(recentPathOf(id), '/home/u/notes.md');
  assert.equal(recentPathOf('com.wash.term'), null);
});

test('recentName / recentDir split a path; trailing slashes and root are handled', () => {
  assert.equal(recentName('/home/u/notes.md'), 'notes.md');
  assert.equal(recentDir('/home/u/notes.md'), '/home/u');
  assert.equal(recentName('/srv/'), 'srv');
  assert.equal(recentDir('/srv/'), '/');
  assert.equal(recentName('/'), '/');
  assert.equal(recentName('relative.txt'), 'relative.txt');
  assert.equal(recentDir('relative.txt'), '/');
});

test('recentMatches searches the whole path, case-insensitively', () => {
  assert.deepEqual(recentMatches(recent, 'NOTES').map((r) => r.path), ['/home/u/notes.md']);
  assert.deepEqual(recentMatches(recent, 'home/u').map((r) => r.path), ['/home/u/notes.md', '/home/u/pics/photo.png']);
  assert.equal(recentMatches(recent, '').length, 3);
  assert.equal(recentMatches(recent, 'nomatch').length, 0);
});

test('appMatches hits id or name', () => {
  assert.deepEqual(appMatches(apps, 'term').map((a) => a.id), ['com.wash.term']);
  assert.deepEqual(appMatches(apps, 'EDIT').map((a) => a.id), ['com.wash.edit']);
  assert.equal(appMatches(apps, '').length, 3);
});

test('paletteEntries: apps sorted by name first, then recent rows newest-first', () => {
  const out = paletteEntries(apps, recent, '');
  assert.deepEqual(
    out.map((e) => e.id),
    ['com.wash.edit', 'com.wash.fm', 'com.wash.term', recentRowID('/home/u/notes.md'), recentRowID('/home/u/pics/photo.png'), recentRowID('/srv/')],
  );
  const r = out[3];
  assert.equal(r.name, 'notes.md');
  assert.equal(r.subtitle, '/home/u');
  assert.equal(r.icon, 'file-text');
  assert.equal(r.recent?.app_id, 'com.wash.edit');
});

test('paletteEntries: a query filters both halves; an empty query caps recent rows', () => {
  const q = paletteEntries(apps, recent, 'notes');
  assert.deepEqual(q.map((e) => e.id), [recentRowID('/home/u/notes.md')]);
  const both = paletteEntries(apps, recent, 'e');
  assert.ok(both.some((e) => e.id === 'com.wash.edit'));
  assert.ok(both.some((e) => e.id === recentRowID('/home/u/notes.md')));
  const capped = paletteEntries(apps, recent, '', 1);
  assert.equal(capped.filter((e) => e.recent).length, 1);
  assert.equal(capped.find((e) => e.recent)?.recent?.path, '/home/u/notes.md');
});

test('pinnedRows keeps pin order and drops unregistered ids', () => {
  const rows = pinnedRows(apps, ['com.wash.edit', 'com.wash.gone', 'com.wash.term']);
  assert.deepEqual(rows.map((a) => a.id), ['com.wash.edit', 'com.wash.term']);
  assert.deepEqual(pinnedRows(apps, []), []);
});

test('stepSelection wraps on arrows, jumps on Home/End, ignores other keys', () => {
  assert.equal(stepSelection('ArrowDown', 0, 3), 1);
  assert.equal(stepSelection('ArrowDown', 2, 3), 0);
  assert.equal(stepSelection('ArrowUp', 0, 3), 2);
  assert.equal(stepSelection('Home', 2, 3), 0);
  assert.equal(stepSelection('End', 0, 3), 2);
  assert.equal(stepSelection('Enter', 0, 3), null);
  assert.equal(stepSelection('ArrowDown', 0, 0), null);
});
