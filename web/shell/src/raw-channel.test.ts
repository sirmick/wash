// Tests for the shell's raw-channel delivery + credit hygiene
// (REVIEW-DATAPATH F7/F8). deliverRaw must report whether a real subscriber
// consumed the bytes (so the caller grants credit only then), and must cap
// bytes parked for a channel that never gets a subscriber.
//
// Run: node --test --conditions=browser web/shell/src/raw-channel.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { deliverRaw, subscribeRaw, closeRawSubscriber } from './api.ts';
import { LOCAL_ORIGIN } from './clients.ts';

test('deliverRaw reports consumed=true only when a subscriber exists', () => {
  const ch = 101;
  const got: number[] = [];
  const unsub = subscribeRaw(LOCAL_ORIGIN, ch, (b) => got.push(b.length));

  const consumed = deliverRaw(LOCAL_ORIGIN, ch, new Uint8Array([1, 2, 3]));
  assert.equal(consumed, true, 'a live subscriber consumes');
  assert.deepEqual(got, [3]);
  unsub();
  closeRawSubscriber(LOCAL_ORIGIN, ch);
});

test('deliverRaw parks when no subscriber (consumed=false), flushes on subscribe', () => {
  const ch = 102;
  const c1 = deliverRaw(LOCAL_ORIGIN, ch, new Uint8Array([1, 2]));
  const c2 = deliverRaw(LOCAL_ORIGIN, ch, new Uint8Array([3, 4, 5]));
  assert.equal(c1, false, 'no subscriber → parked');
  assert.equal(c2, false);

  const got: number[] = [];
  const unsub = subscribeRaw(LOCAL_ORIGIN, ch, (b) => got.push(b.length));
  assert.deepEqual(got, [2, 3], 'parked bytes flush FIFO on subscribe');
  unsub();
  closeRawSubscriber(LOCAL_ORIGIN, ch);
});

test('pendingRaw is capped: oldest dropped past the cap, with a warning', () => {
  const ch = 103;
  const warnings: string[] = [];
  const origWarn = console.warn;
  console.warn = (...a: unknown[]) => { warnings.push(a.join(' ')); };
  try {
    // Park > 1 MiB with no subscriber: 3 chunks of 512 KiB = 1.5 MiB.
    const chunk = () => new Uint8Array(512 * 1024);
    deliverRaw(LOCAL_ORIGIN, ch, chunk()); // 0.5 MiB
    deliverRaw(LOCAL_ORIGIN, ch, chunk()); // 1.0 MiB — still within cap
    deliverRaw(LOCAL_ORIGIN, ch, chunk()); // 1.5 MiB — over cap → drop oldest
  } finally {
    console.warn = origWarn;
  }
  assert.ok(warnings.some((w) => w.includes('over') && w.includes('cap')), 'a cap warning was logged');

  // On subscribe, the retained bytes are ≤ the cap (oldest chunk dropped).
  let total = 0;
  const unsub = subscribeRaw(LOCAL_ORIGIN, ch, (b) => { total += b.length; });
  assert.ok(total <= 1 << 20, `retained ${total} bytes should be ≤ 1 MiB cap`);
  assert.ok(total > 0, 'not everything dropped');
  unsub();
  closeRawSubscriber(LOCAL_ORIGIN, ch);
});

test('closeRawSubscriber clears parked bytes', () => {
  const ch = 104;
  deliverRaw(LOCAL_ORIGIN, ch, new Uint8Array([9]));
  closeRawSubscriber(LOCAL_ORIGIN, ch);
  // A fresh subscribe sees nothing (parked bytes were cleared).
  const got: number[] = [];
  const unsub = subscribeRaw(LOCAL_ORIGIN, ch, (b) => got.push(b.length));
  assert.deepEqual(got, []);
  unsub();
  closeRawSubscriber(LOCAL_ORIGIN, ch);
});
