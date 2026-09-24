// Quick-open ranking + the recent list (quick-open.ts).

import { test } from 'node:test';
import assert from 'node:assert/strict';

import { RECENT_CAP, dropRecent, fuzzyScore, pushRecent, rankFiles } from './quick-open.ts';

test('pushRecent puts the path first, dedups, and caps', () => {
  assert.deepEqual(pushRecent([], '/a'), ['/a']);
  assert.deepEqual(pushRecent(['/a', '/b'], '/b'), ['/b', '/a']);
  assert.deepEqual(pushRecent(['/a', '/b'], '/c'), ['/c', '/a', '/b']);
  const many = Array.from({ length: RECENT_CAP + 5 }, (_, i) => `/f${i}`);
  const out = pushRecent(many, '/new');
  assert.equal(out.length, RECENT_CAP);
  assert.equal(out[0], '/new');
  // An empty path is not a file; nothing is added.
  assert.deepEqual(pushRecent(['/a'], ''), ['/a']);
  // Input untouched.
  const src = ['/a'];
  pushRecent(src, '/b');
  assert.deepEqual(src, ['/a']);
});

test('dropRecent forgets a path', () => {
  assert.deepEqual(dropRecent(['/a', '/b', '/a'], '/a'), ['/b']);
});

test('fuzzyScore: subsequence or nothing, case-insensitive', () => {
  assert.equal(fuzzyScore('xyz', 'main.go'), null);
  assert.notEqual(fuzzyScore('mg', 'main.go'), null);
  assert.notEqual(fuzzyScore('MAIN', 'src/main.go'), null);
  assert.equal(fuzzyScore('', 'anything'), 0);
  // Query longer than the candidate can never match.
  assert.equal(fuzzyScore('abcdefgh', 'abc'), null);
});

test('fuzzyScore prefers basename, word starts and runs', () => {
  const s = (q: string, c: string) => fuzzyScore(q, c)!;
  // The file named after the query beats a path that merely contains it.
  assert.ok(s('main', 'cmd/main.go') > s('main', 'internal/domain/mainframe_test.go'));
  // A word-start abbreviation beats scattered letters.
  assert.ok(s('fp', 'web/lib/src/file-picker.tsx') > s('fp', 'apps/fm/fe/src/main.tsx.map'));
  // Shorter wins on a tie of content.
  assert.ok(s('app', 'app.go') > s('app', 'apps/edit/be/app.go'));
});

test('rankFiles orders best first and keeps input order on ties', () => {
  const files = ['docs/NET.md', 'apps/net/be/app.go', 'apps/net/fe/src/main.tsx', 'internal/net/net.go'];
  const r = rankFiles('net', files);
  // Both files NAMED net.* outrank the ones that only live under net/.
  assert.deepEqual(new Set(r.slice(0, 2)), new Set(['docs/NET.md', 'internal/net/net.go']));
  assert.equal(r[3], 'apps/net/fe/src/main.tsx');
  assert.equal(r.length, 4);
  assert.deepEqual(rankFiles('', files, 2), files.slice(0, 2));
  assert.deepEqual(rankFiles('zzz', files), []);
  assert.equal(rankFiles('a', files, 1).length, 1);
});
