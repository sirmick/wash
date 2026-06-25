// Tests for the FE gzip inflate (the asset + bundle content-coding undo).
// Run with: cd web/shell && npx tsx --test src/gzip.test.ts
//
// node:zlib gzipSync produces a standard gzip stream — the same format the
// Go router emits via compress/gzip — so this round-trip proves the FE
// DecompressionStream path decodes router-compressed assets/bundles.

import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { gzipSync } from 'node:zlib';

import { gunzip } from './gzip.ts';

test('gunzip inflates router-style gzip back to the original text bytes', async () => {
  const original = new TextEncoder().encode('<svg><path d="M0 0 L1 1"/></svg>'.repeat(1000));
  const gz = new Uint8Array(gzipSync(Buffer.from(original)));
  assert.ok(gz.length < original.length, 'expected the test fixture to actually compress');
  const out = await gunzip(gz);
  assert.deepEqual(Array.from(out), Array.from(original));
});

test('gunzip round-trips binary (non-text) data exactly', async () => {
  const original = new Uint8Array(4096);
  for (let i = 0; i < original.length; i++) original[i] = (i * 7) & 0xff;
  const gz = new Uint8Array(gzipSync(Buffer.from(original)));
  const out = await gunzip(gz);
  assert.equal(out.length, original.length);
  assert.deepEqual(Array.from(out), Array.from(original));
});
