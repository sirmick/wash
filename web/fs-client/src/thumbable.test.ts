// Unit tests for the shared thumbnailable-extension predicate.
// Run with: node --test web/fs-client/src/thumbable.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { THUMBABLE_EXTS, isThumbableName } from './thumbable.ts';

test('isThumbableName: the stdlib-decodable formats, case-insensitive', () => {
  assert.equal(isThumbableName('cat.png'), true);
  assert.equal(isThumbableName('cat.JPG'), true);
  assert.equal(isThumbableName('cat.jpeg'), true);
  assert.equal(isThumbableName('cat.gif'), true);
});

test('isThumbableName: formats thumbs cannot decode are excluded', () => {
  assert.equal(isThumbableName('vector.svg'), false);
  assert.equal(isThumbableName('photo.webp'), false);
  assert.equal(isThumbableName('notes.txt'), false);
  assert.equal(isThumbableName('noext'), false);
});

test('THUMBABLE_EXTS mirrors the internal/thumbs decoder set', () => {
  assert.deepEqual([...THUMBABLE_EXTS].sort(), ['gif', 'jpeg', 'jpg', 'png']);
});
