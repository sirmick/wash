// Tests for the FE-side wire codec. Mirror of Go's frame_test.go +
// the QOS.md class-bit invariants.
//
// Run with: cd web/shell && npx tsx --test src/wire.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  CLASS_BACKGROUND,
  CLASS_BULK,
  CLASS_CONTROL,
  CLASS_INTERACTIVE,
  classOf,
  decodeFrame,
  encodeFrame,
  flagsWithClass,
  FLAG_END,
} from './wire.ts';

const enc = new TextEncoder();

test('encode/decode round-trip preserves payload + channel', () => {
  const payload = enc.encode(JSON.stringify({ t: 'identity', app_id: 'com.wash.test' }));
  const bytes = encodeFrame({ flags: FLAG_END, channel: 1, payload });
  const out = decodeFrame(bytes);
  assert.equal(out.channel, 1);
  assert.deepEqual(out.payload, payload);
});

test('class bits round-trip for every class value', () => {
  for (const c of [CLASS_INTERACTIVE, CLASS_BULK, CLASS_BACKGROUND, CLASS_CONTROL] as const) {
    const payload = enc.encode('x');
    const flags = flagsWithClass(FLAG_END, c);
    const bytes = encodeFrame({ flags, channel: 17, payload });
    const out = decodeFrame(bytes);
    assert.equal(classOf(out.flags), c, `class ${c} should round-trip`);
  }
});

test('default flag value (just FLAG_END) reads as Interactive', () => {
  assert.equal(classOf(FLAG_END), CLASS_INTERACTIVE);
});

test('flagsWithClass is idempotent (final class wins)', () => {
  const f = flagsWithClass(flagsWithClass(FLAG_END, CLASS_BULK), CLASS_INTERACTIVE);
  assert.equal(classOf(f), CLASS_INTERACTIVE);
  // END bit must be preserved.
  assert.equal(f & FLAG_END, FLAG_END);
});

test('encode rejects reserved flag bits (≥ bit 3)', () => {
  const payload = new Uint8Array();
  assert.throws(() => encodeFrame({ flags: FLAG_END | 0x08, channel: 0, payload }), /reserved/);
});

test('encode rejects channel id out of 24-bit space', () => {
  const payload = new Uint8Array();
  assert.throws(() => encodeFrame({ flags: FLAG_END, channel: 1 << 24, payload }), /channel/);
});

test('encode rejects END not set', () => {
  const payload = new Uint8Array();
  assert.throws(() => encodeFrame({ flags: 0, channel: 0, payload }), /END/);
});

test('decode rejects oversize length', () => {
  // Manually build a header that claims 17 MiB (above MAX_PAYLOAD).
  const hdr = new Uint8Array(8);
  hdr[0] = FLAG_END;
  new DataView(hdr.buffer).setUint32(4, 17 * 1024 * 1024, false);
  assert.throws(() => decodeFrame(hdr), /oversize/);
});

test('class bits at 0x06 do not trip reserved check', () => {
  const payload = new Uint8Array();
  // Should NOT throw — class bits are NOT reserved.
  const f = flagsWithClass(FLAG_END, CLASS_BULK);
  const bytes = encodeFrame({ flags: f, channel: 0, payload });
  const out = decodeFrame(bytes);
  assert.equal(classOf(out.flags), CLASS_BULK);
});
