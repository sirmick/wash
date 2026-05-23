// Tests for the FE-side credit tracker.
// Run with: cd web/shell && npx tsx --test src/credit.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { CreditTracker, CREDIT_REPLENISH_THRESHOLD } from './credit.ts';

test('absorbed under threshold does NOT send credit', () => {
  const sent: Array<[number, number]> = [];
  const tracker = new CreditTracker((ch, n) => sent.push([ch, n]));
  tracker.absorbed(5, 100);
  tracker.absorbed(5, 200);
  assert.equal(sent.length, 0);
  assert.equal(tracker.pending(5), 300);
});

test('absorbed crossing threshold sends one credit for the full total', () => {
  const sent: Array<[number, number]> = [];
  const tracker = new CreditTracker((ch, n) => sent.push([ch, n]));
  // Push just below threshold.
  tracker.absorbed(5, CREDIT_REPLENISH_THRESHOLD - 1);
  assert.equal(sent.length, 0);
  // Tip over.
  tracker.absorbed(5, 2);
  assert.equal(sent.length, 1);
  assert.deepEqual(sent[0], [5, CREDIT_REPLENISH_THRESHOLD + 1]);
  // Counter resets after the send.
  assert.equal(tracker.pending(5), 0);
});

test('per-channel counters are independent', () => {
  const sent: Array<[number, number]> = [];
  const tracker = new CreditTracker((ch, n) => sent.push([ch, n]));
  tracker.absorbed(5, CREDIT_REPLENISH_THRESHOLD); // crosses → sends
  tracker.absorbed(7, 100); // below threshold → no send
  assert.equal(sent.length, 1);
  assert.equal(sent[0][0], 5);
  assert.equal(tracker.pending(7), 100);
});

test('forget drops the running count without sending', () => {
  const sent: Array<[number, number]> = [];
  const tracker = new CreditTracker((ch, n) => sent.push([ch, n]));
  tracker.absorbed(5, 100);
  tracker.forget(5);
  assert.equal(tracker.pending(5), 0);
  assert.equal(sent.length, 0);
});

test('a single huge absorb sends one credit covering all of it', () => {
  // Models a `cat bigfile` chunk: 256 KiB at once. The tracker
  // should send a single credit for the entire chunk, not split it.
  const sent: Array<[number, number]> = [];
  const tracker = new CreditTracker((ch, n) => sent.push([ch, n]));
  tracker.absorbed(5, 256 * 1024);
  assert.equal(sent.length, 1);
  assert.deepEqual(sent[0], [5, 256 * 1024]);
});
