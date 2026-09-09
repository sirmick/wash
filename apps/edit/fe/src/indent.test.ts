// Indentation detection and the save-time cleanups (indent.ts).

import { test } from 'node:test';
import assert from 'node:assert/strict';

import { DEFAULT_INDENT, detectIndent, indentLabel, indentString, normalizeForSave } from './indent.ts';

test('detectIndent reads spaces and their width', () => {
  const two = 'function f() {\n  a();\n  if (b) {\n    c();\n  }\n}\n';
  assert.deepEqual(detectIndent(two), { unit: 'spaces', width: 2 });
  const four = 'def f():\n    a()\n    if b:\n        c()\n';
  assert.deepEqual(detectIndent(four), { unit: 'spaces', width: 4 });
});

test('detectIndent reads tabs, and alignment spaces inside them do not vote', () => {
  const go = 'func f() {\n\ta()\n\tif b {\n\t\tc()\n\t}\n}\n';
  assert.deepEqual(detectIndent(go), { unit: 'tabs', width: 4 });
  // A continuation line aligned with spaces AFTER a tab is still a tab file.
  const mixed = 'func f() {\n\ta()\n \t b()\n\tc()\n}\n';
  assert.equal(detectIndent(mixed)!.unit, 'tabs');
});

test('detectIndent returns null when there is nothing to go on', () => {
  assert.equal(detectIndent(''), null);
  assert.equal(detectIndent('one\ntwo\nthree\n'), null);
  // Blank lines, even ones made of spaces, are not indentation.
  assert.equal(detectIndent('a\n   \n\nb\n'), null);
});

test('detectIndent only reads the first maxLines lines', () => {
  const head = 'a\n'.repeat(10);
  const tail = '\tx\n'.repeat(50);
  assert.equal(detectIndent(head + tail, 5), null);
  assert.equal(detectIndent(head + tail, 100)!.unit, 'tabs');
});

test('indentString and indentLabel', () => {
  assert.equal(indentString({ unit: 'tabs', width: 4 }), '\t');
  assert.equal(indentString({ unit: 'spaces', width: 3 }), '   ');
  // A nonsense width still produces something insertable.
  assert.equal(indentString({ unit: 'spaces', width: 0 }), ' ');
  assert.equal(indentLabel({ unit: 'tabs', width: 8 }), 'Tab');
  assert.equal(indentLabel(DEFAULT_INDENT), 'Spaces: 2');
});

test('normalizeForSave does nothing unless asked', () => {
  const s = 'a  \nb\t\n\n\n';
  assert.equal(normalizeForSave(s, {}), s);
});

test('normalizeForSave trims trailing whitespace on every line', () => {
  assert.equal(normalizeForSave('a  \n\tb\t \nc', { trimTrailing: true }), 'a\n\tb\nc');
  // Leading indentation is untouched — only what trails.
  assert.equal(normalizeForSave('  a  \n', { trimTrailing: true }), '  a\n');
});

test('normalizeForSave ensures exactly one final newline', () => {
  assert.equal(normalizeForSave('a', { finalNewline: true }), 'a\n');
  assert.equal(normalizeForSave('a\n', { finalNewline: true }), 'a\n');
  assert.equal(normalizeForSave('a\n\n\n', { finalNewline: true }), 'a\n');
  // An empty buffer stays empty: saving it must not create a file with
  // a newline in it.
  assert.equal(normalizeForSave('', { finalNewline: true }), '');
});

test('normalizeForSave applies both in the order that composes', () => {
  // Trailing spaces on the last line then a missing newline.
  assert.equal(normalizeForSave('a\nb   ', { trimTrailing: true, finalNewline: true }), 'a\nb\n');
});
