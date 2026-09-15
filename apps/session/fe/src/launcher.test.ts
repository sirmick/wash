// Unit tests for the launcher's pure decisions (launcher.ts): recent-row
// naming, palette merging of apps + recent files, pin resolution, and the
// arrow-key model the start menu and palette share.
//
// Run: node --test --conditions=browser apps/session/fe/src/launcher.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  RECENT_PREFIX,
  RECENT_FLYOUT_CAP,
  agentRecentAction,
  agentRecentLabel,
  appMatches,
  paletteEntries,
  pinnedRows,
  recentDir,
  recentMatches,
  recentName,
  recentPathOf,
  recentRowID,
  recentGroups,
  stepSelection,
  withLiveRows,
  type AgentRecent,
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

test('recentMatches and the palette never offer a name entry — a station has no path to open', () => {
  const withStation: RecentEntry[] = [...recent, { path: '', name: 'Groove Salad', app_id: 'com.wash.radio', at: 9 }];
  assert.equal(recentMatches(withStation, '').length, 3);
  assert.equal(recentMatches(withStation, 'groove').length, 0);
  assert.ok(paletteEntries(apps, withStation, '').every((e) => !e.recent || e.recent.path));
});

test('agentRecentAction agrees with the Agents history list: reattach, focus, skip, resume', () => {
  assert.equal(agentRecentAction({ session_id: 's', detached: true, live: true, row_key: 'acp:1' }), 'reattach');
  assert.equal(agentRecentAction({ session_id: 's', live: true, row_key: 'acp:1' }), 'focus');
  assert.equal(agentRecentAction({ session_id: 's', live: true }), 'none');
  assert.equal(agentRecentAction({ session_id: 's', detached: true }), 'resume');
  assert.equal(agentRecentAction({ session_id: 's' }), 'resume');
});

test('agentRecentLabel prefers the session title, else agent and folder', () => {
  assert.equal(agentRecentLabel({ session_id: 's', title: 'Fix the banner', agent: 'codex', dir: 'wash' }), 'Fix the banner');
  assert.equal(agentRecentLabel({ session_id: 's', agent: 'codex', dir: 'wash' }), 'codex · wash');
  assert.equal(agentRecentLabel({ session_id: 's' }), 'agent');
});

test('recentGroups: Files, Edit, Agent, Radio always, then other apps with files', () => {
  const store: RecentEntry[] = [
    ...recent,
    { path: '', name: 'Groove Salad', app_id: 'com.wash.radio', at: 5 },
    { path: '', name: 'Drone Zone', app_id: 'com.wash.radio', at: 6 },
  ];
  const agents: AgentRecent[] = [
    { session_id: 'live-no-row', live: true },
    { session_id: 'done', agent: 'codex', dir: 'wash' },
  ];
  const name = (id: string) => apps.find((a) => a.id === id)?.name;
  const groups = recentGroups(store, agents, (id) => (id === 'com.wash.imageview' ? 'Image Viewer' : name(id)));
  assert.deepEqual(groups.map((g) => g.label), ['Files', 'Edit', 'Agent', 'Radio', 'Image Viewer']);
  const [files, edit, agent, radio, images] = groups;
  assert.deepEqual(files.items.map((i) => i.label), ['srv']);
  assert.deepEqual(edit.items.map((i) => i.label), ['notes.md']);
  // A live session with no row cannot be opened from here: left out.
  assert.deepEqual(agent.items.map((i) => i.key), ['a:done']);
  assert.equal(agent.items[0].kind === 'agent' && agent.items[0].action, 'resume');
  assert.deepEqual(radio.items.map((i) => i.label), ['Drone Zone', 'Groove Salad']);
  assert.deepEqual(images.items.map((i) => i.label), ['photo.png']);
});

test('recentGroups: empty named groups still show, and each flyout is capped', () => {
  const many: RecentEntry[] = Array.from({ length: RECENT_FLYOUT_CAP + 3 }, (_, i) => ({
    path: `/f${i}`,
    app_id: 'com.wash.edit',
    at: i,
  }));
  const groups = recentGroups(many, [], () => undefined, []);
  assert.deepEqual(groups.map((g) => g.id), ['com.wash.fm', 'com.wash.edit', 'com.wash.agents', 'com.wash.radio']);
  assert.equal(groups[0].items.length, 0);
  assert.equal(groups[0].empty, 'No recent folders');
  assert.equal(groups[1].items.length, RECENT_FLYOUT_CAP);
  assert.equal(groups[1].items[0].label, `f${RECENT_FLYOUT_CAP + 2}`, 'newest first');
});

test('withLiveRows: the roster, not the lagging history flags, picks the verb', () => {
  // History still says "live with a window"; the roster row says detached.
  const stale: AgentRecent[] = [
    { session_id: 's1', live: true, row_key: 'acp:1' },
    { session_id: 's2', live: true, row_key: 'acp:2' },
    { session_id: 's3' },
  ];
  const rows = [
    { key: 'acp:1', session_id: 's1', detached: true },
    { key: 'acp:3', session_id: 's3' },
  ];
  const fixed = withLiveRows(stale, rows);
  assert.equal(agentRecentAction(fixed[0]), 'reattach');
  // No row: left as the history says.
  assert.equal(agentRecentAction(fixed[1]), 'focus');
  // A row the history did not know was live yet.
  assert.equal(agentRecentAction(fixed[2]), 'focus');
  const groups = recentGroups([], stale, () => undefined, rows);
  assert.deepEqual(
    groups[2].items.map((i) => (i.kind === 'agent' ? i.action : '')),
    ['reattach', 'focus', 'focus'],
  );
});
