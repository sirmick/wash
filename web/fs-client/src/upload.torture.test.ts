// Torture tests for the upload helpers — large batches and adversarial
// inputs aimed at the error/edge paths the happy-path tests in
// upload.test.ts don't reach. Run with:
//   node --test web/fs-client/src/upload.torture.test.ts
//
// The centerpiece is a JS reimplementation of the BE's framed-record
// reader (apps/fm/be/upload.go:runUploadReader). The FE's encoder and
// the BE's decoder are two halves of one wire protocol; if they drift,
// a directory upload of hundreds of files silently corrupts or aborts
// mid-stream. We pin the contract here at scale rather than discovering
// the drift through a flaky e2e.

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  sanitizeRel,
  totalBytes,
  topLevelLabels,
  planUpload,
  encodeRecordHeader,
  uploadEndMarker,
  readBlobChunks,
  entriesFromFileList,
  UPLOAD_CHUNK_SIZE,
  type UploadItem,
  type BlobLike,
} from './upload.ts';

// ---- a faithful JS port of the BE record reader ----
//
// Mirrors runUploadReader's loop: read [u32 nameLen LE]; 0 → end; read
// nameLen UTF-8 bytes; read [u64 size LE]; read exactly size payload
// bytes. Anything that runs off the end of the buffer is a truncated
// stream (the BE's "upload stream ended early"). Returns the decoded
// records so a test can assert the FE encoder produced exactly what the
// BE will read back.
const MAX_REL_LEN = 4096; // mirrors maxUploadRelLen in upload.go

interface DecodedRecord {
  relPath: string;
  bytes: Uint8Array;
}

function decodeUploadStream(stream: Uint8Array): DecodedRecord[] {
  const dv = new DataView(stream.buffer, stream.byteOffset, stream.byteLength);
  const dec = new TextDecoder();
  const out: DecodedRecord[] = [];
  let off = 0;
  const need = (n: number) => {
    if (off + n > stream.length) throw new Error('upload stream ended early');
  };
  for (;;) {
    need(4);
    const nameLen = dv.getUint32(off, true);
    off += 4;
    if (nameLen === 0) break; // end marker
    if (nameLen > MAX_REL_LEN) throw new Error('relpath too long');
    need(nameLen);
    const relPath = dec.decode(stream.subarray(off, off + nameLen));
    off += nameLen;
    need(8);
    const size = Number(dv.getBigUint64(off, true));
    off += 8;
    need(size);
    const bytes = stream.subarray(off, off + size);
    off += size;
    out.push({ relPath, bytes });
  }
  return out;
}

// buildStream concatenates [header][payload] for each item plus the end
// marker — exactly what runUpload writes to the raw channel.
function buildStream(records: { relPath: string; bytes: Uint8Array }[]): Uint8Array {
  const parts: Uint8Array[] = [];
  for (const r of records) {
    parts.push(encodeRecordHeader(r.relPath, r.bytes.length));
    parts.push(r.bytes);
  }
  parts.push(uploadEndMarker());
  const total = parts.reduce((n, p) => n + p.length, 0);
  const out = new Uint8Array(total);
  let off = 0;
  for (const p of parts) {
    out.set(p, off);
    off += p.length;
  }
  return out;
}

// ---- wire-protocol round-trip at scale ----

test('torture: 500-record stream round-trips through the BE reader byte-exact', () => {
  const records: { relPath: string; bytes: Uint8Array }[] = [];
  for (let i = 0; i < 500; i++) {
    // Vary names (unicode, deep nesting, spaces) and payload sizes
    // (incl. zero-byte and bytes that look like a header/end-marker, so
    // a framing bug that re-syncs on payload content would surface).
    const relPath = `dir-${i % 7}/sub ${i % 3}/файл-${i}.dat`;
    const size = i % 5 === 0 ? 0 : (i * 37) % 257;
    const bytes = new Uint8Array(size);
    for (let b = 0; b < size; b++) bytes[b] = (i + b) & 0xff;
    records.push({ relPath, bytes });
  }
  const decoded = decodeUploadStream(buildStream(records));
  assert.equal(decoded.length, records.length);
  for (let i = 0; i < records.length; i++) {
    assert.equal(decoded[i].relPath, records[i].relPath, `relPath #${i}`);
    assert.deepEqual(decoded[i].bytes, records[i].bytes, `bytes #${i}`);
  }
});

test('torture: payload containing an embedded end-marker does not desync the reader', () => {
  // A file whose bytes are [0,0,0,0, …] would prematurely terminate a
  // reader that scanned for the marker instead of honouring the
  // length prefix. The length-delimited framing must read straight
  // through it.
  const evil = new Uint8Array([0, 0, 0, 0, 1, 2, 3, 0, 0, 0, 0]);
  const records = [
    { relPath: 'before.bin', bytes: new Uint8Array([9, 9]) },
    { relPath: 'evil.bin', bytes: evil },
    { relPath: 'after.bin', bytes: new Uint8Array([7]) },
  ];
  const decoded = decodeUploadStream(buildStream(records));
  assert.deepEqual(decoded.map((d) => d.relPath), ['before.bin', 'evil.bin', 'after.bin']);
  assert.deepEqual(decoded[1].bytes, evil);
});

test('torture: a truncated stream (channel closed mid-record) is detected, not silently accepted', () => {
  const full = buildStream([{ relPath: 'a.bin', bytes: new Uint8Array([1, 2, 3, 4, 5]) }]);
  // Lop off the end marker and part of the payload — the BE would
  // surface "upload stream ended early"; our reader must throw, never
  // return a short-but-clean record set.
  for (const cut of [full.length - 1, full.length - 4, full.length - 6, 5]) {
    assert.throws(() => decodeUploadStream(full.subarray(0, cut)), /ended early|too long/);
  }
});

// ---- readBlobChunks: chunk-boundary torture ----

function fakeBlob(bytes: Uint8Array): BlobLike {
  return {
    size: bytes.length,
    slice(start: number, end: number) {
      return { arrayBuffer: async () => bytes.slice(start, end).buffer };
    },
  };
}

test('torture: readBlobChunks reassembles exactly across every size around the chunk boundary', async () => {
  const chunk = 64;
  // 0, 1, chunk-1, chunk, chunk+1, 2*chunk, 2*chunk+1, … — the
  // off-by-one sizes where a slice loop typically drops or duplicates
  // the tail byte.
  const sizes = [0, 1, chunk - 1, chunk, chunk + 1, 2 * chunk, 2 * chunk + 1, 5 * chunk + 13];
  for (const size of sizes) {
    const bytes = new Uint8Array(size);
    for (let i = 0; i < size; i++) bytes[i] = (i * 31) & 0xff;
    const got: Uint8Array[] = [];
    for await (const c of readBlobChunks(fakeBlob(bytes), chunk)) got.push(c);
    assert.equal(got.length, Math.ceil(size / chunk), `chunk count for size ${size}`);
    assert.deepEqual(new Uint8Array(Buffer.concat(got)), bytes, `reassembly for size ${size}`);
  }
});

test('torture: the default chunk size is respected for a multi-chunk blob', async () => {
  const size = UPLOAD_CHUNK_SIZE * 2 + 1;
  const got: Uint8Array[] = [];
  for await (const c of readBlobChunks(fakeBlob(new Uint8Array(size)))) got.push(c);
  assert.equal(got.length, 3);
  assert.equal(got[0].length, UPLOAD_CHUNK_SIZE);
  assert.equal(got[2].length, 1);
});

// ---- planUpload / labels / totals at scale ----

function fakeItem(relPath: string, size: number): UploadItem {
  return { relPath, file: { size } as unknown as File };
}

test('torture: planUpload(skip) drops exactly the conflicting half and keeps totals consistent', () => {
  const items: UploadItem[] = [];
  const conflicts = new Set<string>();
  let expectKeptBytes = 0;
  for (let i = 0; i < 400; i++) {
    const rel = `batch/file-${i}.bin`;
    const size = i + 1;
    items.push(fakeItem(rel, size));
    if (i % 2 === 0) conflicts.add(rel);
    else expectKeptBytes += size;
  }
  const plan = planUpload(items, conflicts, 'skip');
  assert.equal(plan.entries.length, 200);
  assert.ok(plan.entries.every((e) => !conflicts.has(e.relPath)), 'no conflicting entry survived');
  assert.equal(plan.totalBytes, expectKeptBytes);
  assert.equal(plan.totalBytes, totalBytes(plan.entries), 'plan total matches its own entries');
  // All under one top dir → a single label.
  assert.deepEqual(plan.labels, ['batch']);
});

test('torture: planUpload(replace) is a faithful, independent copy of the full set', () => {
  const items = Array.from({ length: 300 }, (_, i) => fakeItem(`d${i % 9}/f${i}`, i));
  const plan = planUpload(items, new Set(['d0/f0']), 'replace');
  assert.equal(plan.entries.length, 300);
  // planUpload must not alias the caller's array (it mutates selection
  // elsewhere); mutating the result must not touch the input.
  plan.entries.pop();
  assert.equal(items.length, 300, 'input array not aliased');
});

test('torture: topLevelLabels dedups hundreds of paths to their distinct roots, in first-seen order', () => {
  const rels: string[] = [];
  for (let i = 0; i < 600; i++) rels.push(`top${i % 12}/n${i}.x`);
  const labels = topLevelLabels(rels);
  assert.equal(labels.length, 12);
  assert.deepEqual(labels, Array.from({ length: 12 }, (_, i) => `top${i}`));
});

// ---- sanitizeRel / entriesFromFileList: adversarial batch ----

test('torture: sanitizeRel rejects a battery of traversal/escape encodings', () => {
  const bad = [
    '', '/', '/abs/path', '..', '../', '../../etc/passwd', 'a/../../b', 'a/./b',
    'a//b', 'a/', 'a/..', './a', '.', 'x/./y/../z', '\\', '\\abs', 'a\\..\\b',
  ];
  for (const b of bad) assert.equal(sanitizeRel(b), null, `expected ${JSON.stringify(b)} → null`);
  // …while a deep but legitimate path is accepted unchanged.
  const deep = Array.from({ length: 40 }, (_, i) => `d${i}`).join('/') + '/leaf.txt';
  assert.equal(sanitizeRel(deep), deep);
});

test('torture: entriesFromFileList keeps valid files and drops every unsafe one, preserving order', () => {
  const files: File[] = [];
  const expectKept: string[] = [];
  for (let i = 0; i < 300; i++) {
    if (i % 3 === 0) {
      // unsafe: traversal via webkitRelativePath — must be dropped.
      files.push({ name: `evil${i}`, size: 1, webkitRelativePath: `../escape/${i}` } as unknown as File);
    } else {
      const rel = `ok/sub/file-${i}.txt`;
      files.push({ name: `file-${i}.txt`, size: 1, webkitRelativePath: rel } as unknown as File);
      expectKept.push(rel);
    }
  }
  const items = entriesFromFileList(files);
  assert.deepEqual(items.map((i) => i.relPath), expectKept);
});
