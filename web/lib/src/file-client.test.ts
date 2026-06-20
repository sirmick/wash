// Unit tests for createFileClient — the FE half of the image-bytes transport.
// Framework-free but DOM-dependent, so we stub the few globals it touches
// (window.wash, URL.createObjectURL/revoke, an EventTarget host) and drive the
// file_channel/raw-bytes/file_done exchange by hand. Run via `make fe-unit`.

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { createFileClient } from './file-client.ts';

// ---- global stubs ----
let blobs: Blob[] = [];
let urlSeq = 0;
const revoked: string[] = [];
const urlOf = new Map<string, Blob>();
(URL as unknown as { createObjectURL: (b: Blob) => string }).createObjectURL = (b: Blob) => {
  blobs.push(b);
  const u = `blob:${++urlSeq}`;
  urlOf.set(u, b);
  return u;
};
(URL as unknown as { revokeObjectURL: (u: string) => void }).revokeObjectURL = (u: string) => revoked.push(u);

interface FakeWash {
  reqs: Array<{ kind: string; req_id: string; path: string; dim: number }>;
  channels: Map<number, (b: Uint8Array) => void>;
  sendAppMsg(instance: string, data: unknown): void;
  openRawChannel(channelID: number, onBytes: (b: Uint8Array) => void): () => void;
}

function installWash(): FakeWash {
  const w: FakeWash = {
    reqs: [],
    channels: new Map(),
    sendAppMsg(_instance, data) {
      w.reqs.push(data as FakeWash['reqs'][number]);
    },
    openRawChannel(channelID, onBytes) {
      w.channels.set(channelID, onBytes);
      return () => w.channels.delete(channelID);
    },
  };
  (globalThis as unknown as { window: unknown }).window = {
    wash: w,
    setTimeout: (fn: () => void, ms: number) => setTimeout(fn, ms),
    clearTimeout: (id: unknown) => clearTimeout(id as ReturnType<typeof setTimeout>),
  };
  return w;
}

const tick = () => new Promise((r) => setTimeout(r, 0));

// Deliver a full successful response for the Nth pending request: open a
// channel, push the bytes, then signal done.
let chanSeq = 100;
async function serve(host: EventTarget, w: FakeWash, reqIndex: number, bytes: number[], mime = 'image/png') {
  const req = w.reqs[reqIndex];
  const channelID = ++chanSeq;
  host.dispatchEvent(new CustomEvent('wash:msg', { detail: { kind: 'file_channel', req_id: req.req_id, channel_id: channelID, mime } }));
  await tick(); // let the client subscribe to the channel
  w.channels.get(channelID)!(new Uint8Array(bytes));
  host.dispatchEvent(new CustomEvent('wash:msg', { detail: { kind: 'file_done', req_id: req.req_id, status: 'done' } }));
}

test('fetches bytes over a channel and resolves a blob URL', async () => {
  blobs = [];
  const w = installWash();
  const host = new EventTarget();
  const fc = createFileClient({ instance: 'i-1', host });
  const pending = fc.url('/a.png', { dim: 160 });
  await tick();
  assert.equal(w.reqs.length, 1);
  assert.deepEqual({ kind: w.reqs[0].kind, path: w.reqs[0].path, dim: w.reqs[0].dim }, { kind: 'get_file', path: '/a.png', dim: 160 });
  await serve(host, w, 0, [1, 2, 3, 4, 5]);
  const u = await pending;
  assert.match(u, /^blob:/);
  assert.equal(blobs[0].size, 5, 'blob assembled from the raw bytes');
  assert.equal(blobs[0].type, 'image/png');
  fc.dispose();
});

test('caches by (dim,path): a repeat request does not re-fetch', async () => {
  const w = installWash();
  const host = new EventTarget();
  const fc = createFileClient({ instance: 'i-2', host });
  const p1 = fc.url('/a.png', { dim: 160 });
  await tick();
  const p2 = fc.url('/a.png', { dim: 160 }); // served by the in-flight promise
  await serve(host, w, 0, [9, 9]);
  assert.equal(await p1, await p2);
  assert.equal(w.reqs.length, 1, 'only one BE request for two identical url() calls');
  fc.dispose();
});

test('full (dim=0) and thumbnail (dim>0) are separate cache buckets', async () => {
  const w = installWash();
  const host = new EventTarget();
  const fc = createFileClient({ instance: 'i-3', host });
  const full = fc.url('/a.png'); // dim 0
  const thumb = fc.url('/a.png', { dim: 160 });
  await tick();
  assert.equal(w.reqs.length, 2);
  assert.deepEqual(w.reqs.map((r) => r.dim).sort(), [0, 160]);
  await serve(host, w, 0, [1]);
  await serve(host, w, 1, [2, 2]);
  assert.notEqual(await full, await thumb);
  fc.dispose();
});

test('thumbnail cache evicts + revokes the oldest beyond maxCache', async () => {
  const w = installWash();
  const host = new EventTarget();
  revoked.length = 0;
  const fc = createFileClient({ instance: 'i-4', host, maxCache: 1 });
  const a = fc.url('/a.png', { dim: 96 });
  await tick();
  await serve(host, w, 0, [1]);
  const aUrl = await a;
  const b = fc.url('/b.png', { dim: 96 });
  await tick();
  await serve(host, w, 1, [2]);
  await b;
  assert.ok(revoked.includes(aUrl), 'oldest thumbnail URL revoked once the bucket overflows');
  fc.dispose();
});

test('concurrency cap queues excess requests', async () => {
  const w = installWash();
  const host = new EventTarget();
  const fc = createFileClient({ instance: 'i-5', host, maxConcurrent: 1 });
  const a = fc.url('/a.png', { dim: 96 });
  const b = fc.url('/b.png', { dim: 96 });
  await tick();
  assert.equal(w.reqs.length, 1, 'second request is held until a slot frees');
  await serve(host, w, 0, [1]);
  await a;
  await tick();
  assert.equal(w.reqs.length, 2, 'queued request issues after the first completes');
  await serve(host, w, 1, [2]);
  await b;
  fc.dispose();
});

test('times out and lets a later call retry', async () => {
  const w = installWash();
  const host = new EventTarget();
  const fc = createFileClient({ instance: 'i-6', host, timeoutMs: 20 });
  await assert.rejects(fc.url('/slow.png', { dim: 96 }), /timeout/);
  // cache entry was cleared on failure → a fresh call re-requests.
  const retry = fc.url('/slow.png', { dim: 96 });
  await tick();
  assert.equal(w.reqs.length, 2, 'a failed fetch is not cached');
  await serve(host, w, 1, [7]);
  assert.match(await retry, /^blob:/);
  fc.dispose();
});

test('an early file_done (pre-channel failure) rejects', async () => {
  const w = installWash();
  const host = new EventTarget();
  const fc = createFileClient({ instance: 'i-7', host });
  const p = fc.url('/missing.png', { dim: 96 });
  await tick();
  host.dispatchEvent(
    new CustomEvent('wash:msg', { detail: { kind: 'file_done', req_id: w.reqs[0].req_id, status: 'failed', error: 'no such file' } }),
  );
  await assert.rejects(p, /no such file/);
  fc.dispose();
});

test('dispose revokes every cached blob URL', async () => {
  const w = installWash();
  const host = new EventTarget();
  revoked.length = 0;
  const fc = createFileClient({ instance: 'i-8', host });
  const a = fc.url('/a.png', { dim: 96 });
  await tick();
  await serve(host, w, 0, [1]);
  const aUrl = await a;
  fc.dispose();
  assert.ok(revoked.includes(aUrl), 'dispose revokes outstanding blob URLs');
});
