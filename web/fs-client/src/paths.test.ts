// Unit tests for wash-fm pure path/format helpers.
// Run with: node --test apps/fm/fe/src/paths.test.ts
// (Node 22+ strips TypeScript types natively — no tsx/build step.)

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { joinPath, parentPath, baseName, ancestorChain, humanSize, formatDate, octalPerm } from './paths.ts';

test('joinPath: appends with a separator', () => {
  assert.equal(joinPath('/home', 'mick'), '/home/mick');
  assert.equal(joinPath('/home/mick', 'a.txt'), '/home/mick/a.txt');
});

test('joinPath: a parent already ending in / is not doubled', () => {
  assert.equal(joinPath('/', 'etc'), '/etc');
  assert.equal(joinPath('/home/', 'mick'), '/home/mick');
});

test('parentPath: empty and root collapse to root', () => {
  assert.equal(parentPath(''), '/');
  assert.equal(parentPath('/'), '/');
});

test('parentPath: a top-level entry parents to root', () => {
  assert.equal(parentPath('/etc'), '/');
});

test('parentPath: nested paths drop the last segment', () => {
  assert.equal(parentPath('/home/mick'), '/home');
  assert.equal(parentPath('/home/mick/a.txt'), '/home/mick');
});

test('baseName: returns the trailing segment', () => {
  assert.equal(baseName('/home/mick/a.txt'), 'a.txt');
  assert.equal(baseName('/etc'), 'etc');
});

test('baseName: a name with no slash is returned whole', () => {
  assert.equal(baseName('a.txt'), 'a.txt');
});

test('humanSize: bytes under 1K are shown raw', () => {
  assert.equal(humanSize(0), '0 B');
  assert.equal(humanSize(1023), '1023 B');
});

test('humanSize: KB and MB use one decimal', () => {
  assert.equal(humanSize(1024), '1.0 KB');
  assert.equal(humanSize(1536), '1.5 KB');
  assert.equal(humanSize(1024 * 1024), '1.0 MB');
});

test('humanSize: GB uses two decimals (its largest tier)', () => {
  assert.equal(humanSize(1024 * 1024 * 1024), '1.00 GB');
  // Terabyte-scale values still report in GB — there is no TB tier.
  assert.equal(humanSize(2 * 1024 ** 4), '2048.00 GB');
});

test('formatDate: zero/falsy renders empty string', () => {
  assert.equal(formatDate(0), '');
});

test('formatDate: this-year shows "Mon DD HH:MM" with space-padded day', () => {
  const now = new Date();
  const d = new Date(now.getFullYear(), 11, 5, 4, 9, 0); // Dec 5, 04:09
  assert.equal(formatDate(Math.floor(d.getTime() / 1000)), 'Dec  5 04:09');
});

test('formatDate: a prior year shows "Mon DD  YYYY" (two spaces)', () => {
  const now = new Date();
  const d = new Date(now.getFullYear() - 3, 11, 15, 0, 0, 0); // Dec 15, prior year
  assert.equal(formatDate(Math.floor(d.getTime() / 1000)), `Dec 15  ${now.getFullYear() - 3}`);
});

test('octalPerm: leading-zero octal of the low 9 bits', () => {
  assert.equal(octalPerm(0o755), '0755');
  assert.equal(octalPerm(0o644), '0644');
  // High bits (file type / setuid) are masked off.
  assert.equal(octalPerm(0o100644), '0644');
});

test('ancestorChain: a nested path expands to each ancestor, root first', () => {
  assert.deepEqual(ancestorChain('/a/b/c'), ['/', '/a', '/a/b', '/a/b/c']);
});

test('ancestorChain: a top-level entry is just root + itself', () => {
  assert.deepEqual(ancestorChain('/etc'), ['/', '/etc']);
});

test('ancestorChain: root (and any segment-less path) is just ["/"]', () => {
  assert.deepEqual(ancestorChain('/'), ['/']);
  assert.deepEqual(ancestorChain(''), ['/']);
});

test('ancestorChain: redundant/trailing slashes are collapsed (empty segments dropped)', () => {
  assert.deepEqual(ancestorChain('/a//b/'), ['/', '/a', '/a/b']);
});

test('ancestorChain: each element is the parent of the next (chain invariant)', () => {
  const chain = ancestorChain('/x/y/z');
  for (let i = 1; i < chain.length; i++) {
    assert.equal(parentPath(chain[i]), chain[i - 1]);
  }
});
