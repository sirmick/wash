// Unit tests for the directory listing sort + hidden-file filter.
// Run with: node --test web/fs-client/src/sort.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { sortedFiltered, type SortableEntry, type SortOptions } from './sort.ts';

// Tiny entry factory — only the sortable fields matter here.
function e(
  name: string,
  type: string,
  extra: Partial<SortableEntry> = {},
): SortableEntry {
  return { name, type, size: 0, mod_unix: 0, created_unix: 0, ...extra };
}

const names = (es: SortableEntry[]) => es.map((x) => x.name);
const opts = (o: Partial<SortOptions> = {}): SortOptions => ({
  key: 'name',
  desc: false,
  showHidden: false,
  ...o,
});

test('hidden dotfiles are filtered out unless showHidden', () => {
  const list = [e('.hidden', 'file'), e('visible', 'file')];
  assert.deepEqual(names(sortedFiltered(list, opts())), ['visible']);
  assert.deepEqual(names(sortedFiltered(list, opts({ showHidden: true }))), ['.hidden', 'visible']);
});

test('does not mutate the input array', () => {
  const list = [e('b', 'file'), e('a', 'file')];
  const before = names(list).slice();
  sortedFiltered(list, opts());
  assert.deepEqual(names(list), before);
});

test('directories sort before files for the name key', () => {
  const list = [e('zfile', 'file'), e('adir', 'dir'), e('bfile', 'file'), e('ydir', 'dir')];
  assert.deepEqual(names(sortedFiltered(list, opts())), ['adir', 'ydir', 'bfile', 'zfile']);
});

test('name sort is case-insensitive', () => {
  const list = [e('Banana', 'file'), e('apple', 'file'), e('Cherry', 'file')];
  assert.deepEqual(names(sortedFiltered(list, opts())), ['apple', 'Banana', 'Cherry']);
});

test('desc flips the order but dirs still lead (dir grouping is not inverted)', () => {
  const list = [e('a', 'file'), e('b', 'file'), e('d', 'dir')];
  // dir-before-file is a hard pre-comparison; desc only flips the within-group cmp.
  assert.deepEqual(names(sortedFiltered(list, opts({ desc: true }))), ['d', 'b', 'a']);
});

test('size key sorts numerically (not lexically)', () => {
  const list = [
    e('big', 'file', { size: 1000 }),
    e('small', 'file', { size: 9 }),
    e('mid', 'file', { size: 100 }),
  ];
  assert.deepEqual(names(sortedFiltered(list, opts({ key: 'size' }))), ['small', 'mid', 'big']);
});

test('mtime and ctime keys sort by their respective timestamps', () => {
  const list = [
    e('newest', 'file', { mod_unix: 300, created_unix: 1 }),
    e('oldest', 'file', { mod_unix: 100, created_unix: 3 }),
    e('mid', 'file', { mod_unix: 200, created_unix: 2 }),
  ];
  assert.deepEqual(names(sortedFiltered(list, opts({ key: 'mtime' }))), ['oldest', 'mid', 'newest']);
  assert.deepEqual(names(sortedFiltered(list, opts({ key: 'ctime' }))), ['newest', 'mid', 'oldest']);
});

test('type key does NOT force dirs first; it sorts by the type field, ties broken by name', () => {
  const list = [e('z', 'file'), e('a', 'file'), e('m', 'dir')];
  // 'dir' < 'file' lexically, so the dir leads here too — but via the
  // type comparison, not the dir-before-file pre-check. Within 'file',
  // ties break by name (a before z).
  assert.deepEqual(names(sortedFiltered(list, opts({ key: 'type' }))), ['m', 'a', 'z']);
});

test('empty input yields an empty array', () => {
  assert.deepEqual(sortedFiltered([], opts()), []);
});
