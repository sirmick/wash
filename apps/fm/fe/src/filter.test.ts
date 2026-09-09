import { test } from 'node:test';
import assert from 'node:assert/strict';
import { filterRows, isTypeToFilterKey, matchRanges, nameMatches, splitByRanges } from './filter.ts';

const rows = [
  { path: '/r' },
  { path: '/r/docs' },
  { path: '/r/docs/inner.txt' },
  { path: '/r/Report.md' },
  { path: '/r/notes.txt' },
  { path: '/other' },
];

test('an empty query keeps every row (and returns a copy)', () => {
  const out = filterRows(rows, '/r', '   ');
  assert.deepEqual(out, rows);
  assert.notEqual(out, rows);
});

test('the current folder\'s direct children are filtered by case-insensitive substring', () => {
  const out = filterRows(rows, '/r', 'RePo').map((r) => r.path);
  assert.deepEqual(out, ['/r', '/r/Report.md', '/other']);
});

test('descendants follow their top-level ancestor under the folder', () => {
  const out = filterRows(rows, '/r', 'doc').map((r) => r.path);
  assert.deepEqual(out, ['/r', '/r/docs', '/r/docs/inner.txt', '/other']);
  // inner.txt matches "inner" but its top-level ancestor (docs) does not.
  assert.deepEqual(filterRows(rows, '/r', 'inner').map((r) => r.path), ['/r', '/other']);
});

test('rows outside the folder (ancestors, siblings of ancestors) are untouched', () => {
  const out = filterRows(rows, '/r/docs', 'zzz').map((r) => r.path);
  assert.deepEqual(out, ['/r', '/r/docs', '/r/Report.md', '/r/notes.txt', '/other']);
});

test('filtering at the root folder works', () => {
  const out = filterRows(rows, '/', 'oth').map((r) => r.path);
  assert.deepEqual(out, ['/other']);
});

test('nameMatches mirrors the row rule', () => {
  assert.equal(nameMatches('Report.md', 'port'), true);
  assert.equal(nameMatches('Report.md', 'xyz'), false);
  assert.equal(nameMatches('anything', ''), true);
});

test('matchRanges finds every non-overlapping case-insensitive occurrence', () => {
  assert.deepEqual(matchRanges('aXbxc', 'x'), [[1, 2], [3, 4]]);
  assert.deepEqual(matchRanges('aaaa', 'aa'), [[0, 2], [2, 4]]);
  assert.deepEqual(matchRanges('none', 'zz'), []);
  assert.deepEqual(matchRanges('none', ''), []);
});

test('splitByRanges alternates plain and matched segments', () => {
  const parts = splitByRanges('Report.md', matchRanges('Report.md', 'ort'));
  assert.deepEqual(parts, [
    { text: 'Rep', match: false },
    { text: 'ort', match: true },
    { text: '.md', match: false },
  ]);
  assert.deepEqual(splitByRanges('abc', []), [{ text: 'abc', match: false }]);
  assert.deepEqual(splitByRanges('abc', [[0, 3]]), [{ text: 'abc', match: true }]);
});

test('type-to-filter opens on a bare printable key only', () => {
  const none = { ctrl: false, meta: false, alt: false };
  assert.equal(isTypeToFilterKey('a', none), true);
  assert.equal(isTypeToFilterKey('.', none), true);
  assert.equal(isTypeToFilterKey(' ', none), false, 'Space toggles the cursor row');
  assert.equal(isTypeToFilterKey('Enter', none), false);
  assert.equal(isTypeToFilterKey('ArrowDown', none), false);
  assert.equal(isTypeToFilterKey('a', { ...none, ctrl: true }), false);
  assert.equal(isTypeToFilterKey('a', { ...none, meta: true }), false);
  assert.equal(isTypeToFilterKey('a', { ...none, alt: true }), false);
});
