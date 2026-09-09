import { test } from 'node:test';
import assert from 'node:assert/strict';
import { arrowLeft, arrowRight, nextRow, pageSizeFor, type NavRow } from './keynav.ts';

// /a (expanded dir) → /a/x, /a/y ; /b (collapsed dir) ; /c (file) ; /d (file)
const ROWS: NavRow[] = [
  { path: '/a', depth: 0, isDir: true, expanded: true },
  { path: '/a/x', depth: 1, isDir: false, expanded: false },
  { path: '/a/y', depth: 1, isDir: false, expanded: false },
  { path: '/b', depth: 0, isDir: true, expanded: false },
  { path: '/c', depth: 0, isDir: false, expanded: false },
  { path: '/d', depth: 0, isDir: false, expanded: false },
];

test('down/up step one row and clamp at the edges', () => {
  assert.equal(nextRow(ROWS, '/a', 'down', 10), '/a/x');
  assert.equal(nextRow(ROWS, '/a/x', 'up', 10), '/a');
  assert.equal(nextRow(ROWS, '/d', 'down', 10), null, 'already at the bottom');
  assert.equal(nextRow(ROWS, '/a', 'up', 10), null, 'already at the top');
});

test('with no cursor, down lands on the first row and up on the last', () => {
  assert.equal(nextRow(ROWS, null, 'down', 10), '/a');
  assert.equal(nextRow(ROWS, null, 'up', 10), '/d');
  assert.equal(nextRow(ROWS, null, 'pageDown', 3), '/a');
  assert.equal(nextRow(ROWS, null, 'end', 3), '/d');
});

test('a cursor that is no longer a visible row behaves like no cursor', () => {
  assert.equal(nextRow(ROWS, '/gone', 'down', 10), '/a');
});

test('home/end jump to the first/last row; no-op when already there', () => {
  assert.equal(nextRow(ROWS, '/c', 'home', 10), '/a');
  assert.equal(nextRow(ROWS, '/c', 'end', 10), '/d');
  assert.equal(nextRow(ROWS, '/a', 'home', 10), null);
  assert.equal(nextRow(ROWS, '/d', 'end', 10), null);
});

test('page moves step by pageSize and clamp', () => {
  assert.equal(nextRow(ROWS, '/a', 'pageDown', 3), '/b');
  assert.equal(nextRow(ROWS, '/b', 'pageDown', 3), '/d', 'clamped to the last row');
  assert.equal(nextRow(ROWS, '/d', 'pageUp', 3), '/a/y');
  assert.equal(nextRow(ROWS, '/a/x', 'pageUp', 3), '/a', 'clamped to the first row');
  assert.equal(nextRow(ROWS, '/a', 'pageDown', 0), '/a/x', 'a degenerate page size still moves one row');
});

test('empty list never yields a row', () => {
  assert.equal(nextRow([], null, 'down', 10), null);
  assert.equal(nextRow([], '/a', 'end', 10), null);
});

test('ArrowRight expands a collapsed folder', () => {
  assert.deepEqual(arrowRight(ROWS, '/b'), { kind: 'expand', path: '/b' });
});

test('ArrowRight on an expanded folder steps into its first child', () => {
  assert.deepEqual(arrowRight(ROWS, '/a'), { kind: 'move', path: '/a/x' });
});

test('ArrowRight on an expanded but empty folder, a file, or no cursor does nothing', () => {
  const rows: NavRow[] = [
    { path: '/e', depth: 0, isDir: true, expanded: true },
    { path: '/f', depth: 0, isDir: false, expanded: false },
  ];
  assert.deepEqual(arrowRight(rows, '/e'), { kind: 'none' });
  assert.deepEqual(arrowRight(ROWS, '/c'), { kind: 'none' });
  assert.deepEqual(arrowRight(ROWS, null), { kind: 'none' });
});

test('ArrowLeft collapses an expanded folder', () => {
  assert.deepEqual(arrowLeft(ROWS, '/a'), { kind: 'collapse', path: '/a' });
});

test('ArrowLeft on a child (or a collapsed folder inside a folder) jumps to the parent row', () => {
  assert.deepEqual(arrowLeft(ROWS, '/a/y'), { kind: 'move', path: '/a' });
  const nested: NavRow[] = [
    { path: '/p', depth: 0, isDir: true, expanded: true },
    { path: '/p/q', depth: 1, isDir: true, expanded: true },
    { path: '/p/q/r', depth: 2, isDir: true, expanded: false },
  ];
  assert.deepEqual(arrowLeft(nested, '/p/q/r'), { kind: 'move', path: '/p/q' });
});

test('ArrowLeft at the top level (collapsed folder or file) does nothing', () => {
  assert.deepEqual(arrowLeft(ROWS, '/b'), { kind: 'none' });
  assert.deepEqual(arrowLeft(ROWS, '/c'), { kind: 'none' });
  assert.deepEqual(arrowLeft(ROWS, null), { kind: 'none' });
});

test('pageSizeFor leaves one row of context and falls back when unmeasured', () => {
  assert.equal(pageSizeFor(220, 22), 9);
  assert.equal(pageSizeFor(30, 22), 1);
  assert.equal(pageSizeFor(0, 22), 10);
  assert.equal(pageSizeFor(220, 0), 10);
});
