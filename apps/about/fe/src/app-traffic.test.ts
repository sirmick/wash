import { test } from 'node:test';
import assert from 'node:assert/strict';
import { trafficRows, shortName } from './app-traffic.ts';

// [Interactive, Bulk, Background, Control]
const row = (app_id: string, tx_bytes: number[], tx_frames: number[]) => ({ app_id, tx_bytes, tx_frames });

test('busiest app first, wash prefix trimmed', () => {
  const rows = trafficRows(
    [
      row('com.wash.term', [10, 500, 0, 0], [1, 5, 0, 0]),
      row('com.wash.ai', [0, 9000, 0, 0], [0, 9, 0, 0]),
    ],
    [10, 9500, 0, 0],
    [1, 14, 0, 0],
  );
  assert.equal(rows[0].label, 'ai');
  assert.equal(rows[0].total, 9000);
  assert.equal(rows[1].label, 'term');
  assert.equal(rows[1].total, 510);
  // Everything is attributed here, so there is no remainder row.
  assert.equal(rows.length, 2);
});

test('the unattributed remainder becomes the router row', () => {
  const rows = trafficRows(
    [row('com.wash.term', [0, 1000, 0, 0], [0, 4, 0, 0])],
    [700, 1000, 0, 250],
    [9, 4, 0, 3],
  );
  const rest = rows.find((r) => r.derived);
  assert.ok(rest, 'no remainder row');
  assert.equal(rest.label, 'router (lifecycle)');
  // Interactive and Control are entirely the router's; Bulk is all the app's.
  assert.deepEqual(rest.bytes, [700, 0, 0, 250]);
  assert.deepEqual(rest.frames, [9, 0, 0, 3]);
  assert.equal(rest.total, 950);
});

test('the rows reconcile with the class totals they came from', () => {
  const classBytes = [1200, 8000, 40, 300];
  const rows = trafficRows(
    [
      row('com.wash.term', [200, 5000, 0, 0], [2, 20, 0, 0]),
      row('com.wash.fm', [100, 0, 40, 0], [1, 0, 1, 0]),
    ],
    classBytes,
    [12, 20, 1, 4],
  );
  const summed = [0, 0, 0, 0];
  for (const r of rows) for (let i = 0; i < 4; i++) summed[i] += r.bytes[i];
  assert.deepEqual(summed, classBytes, 'per-app rows plus the remainder must equal the class totals');
});

test('an app that has sent nothing is not a row', () => {
  const rows = trafficRows([row('com.wash.about', [0, 0, 0, 0], [0, 0, 0, 0])], [0, 0, 0, 0], [0, 0, 0, 0]);
  assert.deepEqual(rows, []);
});

test('a snapshot that skews negative clamps instead of showing a negative row', () => {
  // The app's bytes are counted when the envelope is relayed and the class
  // total when the frame reaches the wire, so in-flight bytes can exceed it.
  const rows = trafficRows([row('com.wash.ai', [0, 5000, 0, 0], [0, 5, 0, 0])], [0, 4000, 0, 0], [0, 4, 0, 0]);
  const rest = rows.find((r) => r.derived);
  assert.equal(rest, undefined, 'a negative remainder must not be rendered at all');
});

test('missing or short arrays do not throw', () => {
  assert.deepEqual(trafficRows(undefined, undefined, undefined), []);
  const rows = trafficRows([{ app_id: 'com.wash.term', tx_bytes: [5], tx_frames: [1] }], [5], [1]);
  assert.equal(rows[0].total, 5);
  assert.deepEqual(rows[0].bytes, [5, 0, 0, 0]);
});

test('an id outside wash keeps its namespace', () => {
  assert.equal(shortName('com.wash.term'), 'term');
  assert.equal(shortName('org.example.thing'), 'org.example.thing');
});
