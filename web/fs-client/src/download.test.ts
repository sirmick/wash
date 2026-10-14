// Unit tests for the download handshake.
// Run with: node --test web/fs-client/src/download.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { createDownloads, type DownloadOptions } from './download.ts';
import type { BEMessage, Clock } from './bus.ts';

// Timers that never fire on their own; fire() runs whatever is pending.
function heldClock() {
  const pending = new Set<() => void>();
  const clock: Clock = {
    setTimeout: (fn) => { pending.add(fn); return fn; },
    clearTimeout: (h) => { pending.delete(h as () => void); },
  };
  return { clock, fire: () => { for (const fn of [...pending]) { pending.delete(fn); fn(); } } };
}

function harness(begin: BEMessage) {
  const saved: Array<{ name: string; bytes: number[] }> = [];
  const errors: string[] = [];
  const subs = new Map<number, (b: Uint8Array) => void>();
  const unsubscribed: number[] = [];
  const { clock, fire } = heldClock();
  const opts: DownloadOptions = {
    request: async () => begin,
    openRawChannel: (ch, onBytes) => { subs.set(ch, onBytes); return () => unsubscribed.push(ch); },
    save: (name, chunks) => saved.push({ name, bytes: chunks.flatMap((c) => [...c]) }),
    onError: (msg) => errors.push(msg),
    clock,
  };
  return { dl: createDownloads(opts), saved, errors, subs, unsubscribed, fire };
}

const tick = () => new Promise((r) => setTimeout(r, 0));

test('collects the channel bytes and saves them on done', async () => {
  const h = harness({ kind: 'download_begin_ok', download_id: 'd1', filename: 'a.txt' });
  const p = h.dl.run(['/x/a.txt']);
  await tick();
  assert.equal(h.dl.onMessage({ kind: 'download_channel', download_id: 'd1', channel_id: 7 }), true);
  await tick();
  h.subs.get(7)!(new Uint8Array([1, 2]));
  h.subs.get(7)!(new Uint8Array([3]));
  h.dl.onMessage({ kind: 'download_done', download_id: 'd1', status: 'done' });
  await p;
  assert.deepEqual(h.saved, [{ name: 'a.txt', bytes: [1, 2, 3] }]);
  assert.deepEqual(h.unsubscribed, [7]);
  assert.deepEqual(h.errors, []);
});

test('a failure before the channel opens unblocks the waiter', async () => {
  const h = harness({ kind: 'download_begin_ok', download_id: 'd2', filename: 'b' });
  const p = h.dl.run(['/x/b']);
  await tick();
  h.dl.onMessage({ kind: 'download_done', download_id: 'd2', status: 'failed' });
  await p;
  assert.deepEqual(h.saved, []);
  assert.deepEqual(h.errors, ['download: channel never opened']);
});

test('a channel that never comes times out', async () => {
  const h = harness({ kind: 'download_begin_ok', download_id: 'd3', filename: 'c' });
  const p = h.dl.run(['/x/c']);
  await tick();
  h.fire();
  await p;
  assert.deepEqual(h.errors, ['download: channel never opened']);
});

test('a refused begin reports the BE message', async () => {
  const h = harness({ kind: 'download_begin_err', code: 'outside_root', msg: 'outside the sandbox' });
  await h.dl.run(['/etc/passwd']);
  assert.deepEqual(h.errors, ['download: outside the sandbox']);
});

test('ignores other messages', () => {
  const h = harness({ kind: 'download_begin_ok' });
  assert.equal(h.dl.onMessage({ kind: 'list_ok' }), false);
});
