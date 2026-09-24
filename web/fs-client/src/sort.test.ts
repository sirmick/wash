// Unit tests for the directory listing sort + hidden-file filter.
// Run with: node --test web/fs-client/src/sort.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { extensionOf, sortedFiltered, type SortableEntry, type SortOptions } from './sort.ts';

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

test('type key groups dirs first, then orders files by extension', () => {
  const list = [
    e('notes.txt', 'file'),
    e('photo.png', 'file'),
    e('zebra', 'dir'),
    e('main.go', 'file'),
    e('alpha', 'dir'),
  ];
  // Folders lead (a folder is not a kind of file), name-ordered among
  // themselves; then the files by extension: go < png < txt.
  assert.deepEqual(names(sortedFiltered(list, opts({ key: 'type' }))), [
    'alpha', 'zebra', 'main.go', 'photo.png', 'notes.txt',
  ]);
});

test('type key breaks an extension tie by name, case-insensitively', () => {
  const list = [e('Zeta.txt', 'file'), e('alpha.txt', 'file'), e('beta.txt', 'file')];
  assert.deepEqual(names(sortedFiltered(list, opts({ key: 'type' }))), [
    'alpha.txt', 'beta.txt', 'Zeta.txt',
  ]);
});

test('type key sorts extensionless files (and dotfiles) before the rest', () => {
  const list = [
    e('run.sh', 'file'),
    e('Makefile', 'file'),
    e('.bashrc', 'file'),
    e('a.md', 'file'),
  ];
  assert.deepEqual(names(sortedFiltered(list, opts({ key: 'type', showHidden: true }))), [
    '.bashrc', 'Makefile', 'a.md', 'run.sh',
  ]);
});

test('type key ignores extension case and sorts symlinks with the files', () => {
  const list = [e('B.PNG', 'file'), e('a.png', 'file'), e('link.txt', 'symlink')];
  assert.deepEqual(names(sortedFiltered(list, opts({ key: 'type' }))), [
    'a.png', 'B.PNG', 'link.txt',
  ]);
});

test('type key descending reverses the extensions but keeps dirs first', () => {
  // desc flips the comparison, not the dir/file grouping — same as every
  // other key.
  const list = [e('d', 'dir'), e('a.txt', 'file'), e('b.md', 'file')];
  assert.deepEqual(names(sortedFiltered(list, opts({ key: 'type', desc: true }))), [
    'd', 'a.txt', 'b.md',
  ]);
});

test('extensionOf reads the final suffix, lowercased, or nothing', () => {
  assert.equal(extensionOf('notes.txt'), 'txt');
  assert.equal(extensionOf('photo.JPEG'), 'jpeg');
  assert.equal(extensionOf('archive.tar.gz'), 'gz');
  assert.equal(extensionOf('Makefile'), '');
  assert.equal(extensionOf('.bashrc'), '');
  assert.equal(extensionOf('trailing.'), '');
  assert.equal(extensionOf(''), '');
});

test('empty input yields an empty array', () => {
  assert.deepEqual(sortedFiltered([], opts()), []);
});
