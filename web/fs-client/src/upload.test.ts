// Unit tests for the framework-free upload helpers.
// Run with: node --test web/fs-client/src/upload.test.ts

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
  entriesFromDataTransfer,
  type UploadItem,
  type BlobLike,
} from './upload.ts';

// fakeItem builds a minimal UploadItem; the File is just a sized stub.
function fakeItem(relPath: string, size: number): UploadItem {
  return { relPath, file: { size } as unknown as File };
}

// ---- sanitizeRel ----

test('sanitizeRel accepts plain names and nested paths', () => {
  assert.equal(sanitizeRel('img.jpg'), 'img.jpg');
  assert.equal(sanitizeRel('photos/2024/img.jpg'), 'photos/2024/img.jpg');
});

test('sanitizeRel rejects traversal / absolute / empty components', () => {
  for (const bad of ['', '/abs', '..', '../x', 'a/../b', './x', 'a//b', 'a/.', '\\\\unc']) {
    assert.equal(sanitizeRel(bad), null, `expected ${JSON.stringify(bad)} → null`);
  }
});

test('sanitizeRel normalizes backslashes to forward slashes', () => {
  assert.equal(sanitizeRel('a\\b\\c.txt'), 'a/b/c.txt');
});

// ---- totalBytes ----

test('totalBytes sums file sizes', () => {
  assert.equal(totalBytes([fakeItem('a', 10), fakeItem('b', 5)]), 15);
  assert.equal(totalBytes([]), 0);
});

// ---- topLevelLabels ----

test('topLevelLabels returns unique first path components in order', () => {
  assert.deepEqual(
    topLevelLabels(['photos/a.jpg', 'photos/b.jpg', 'notes.txt', 'photos/c.jpg']),
    ['photos', 'notes.txt'],
  );
});

// ---- planUpload ----

test('planUpload under skip drops conflicting items and recomputes totals', () => {
  const items = [fakeItem('a.txt', 3), fakeItem('dup.txt', 7), fakeItem('b.txt', 5)];
  const plan = planUpload(items, new Set(['dup.txt']), 'skip');
  assert.deepEqual(
    plan.entries.map((e) => e.relPath),
    ['a.txt', 'b.txt'],
  );
  assert.equal(plan.totalBytes, 8);
  assert.deepEqual(plan.labels, ['a.txt', 'b.txt']);
});

test('planUpload under replace keeps everything', () => {
  const items = [fakeItem('a.txt', 3), fakeItem('dup.txt', 7)];
  const plan = planUpload(items, new Set(['dup.txt']), 'replace');
  assert.equal(plan.entries.length, 2);
  assert.equal(plan.totalBytes, 10);
});

// ---- encodeRecordHeader / uploadEndMarker (decoded the way the BE does) ----

test('encodeRecordHeader lays out [u32 len LE][path][u64 size LE]', () => {
  const hdr = encodeRecordHeader('a/b.txt', 1234);
  const dv = new DataView(hdr.buffer);
  const nameLen = dv.getUint32(0, true);
  assert.equal(nameLen, 7); // "a/b.txt"
  const name = new TextDecoder().decode(hdr.subarray(4, 4 + nameLen));
  assert.equal(name, 'a/b.txt');
  const size = dv.getBigUint64(4 + nameLen, true);
  assert.equal(size, 1234n);
  assert.equal(hdr.length, 4 + nameLen + 8);
});

test('encodeRecordHeader counts UTF-8 bytes, not code units', () => {
  const hdr = encodeRecordHeader('é.txt', 0); // é = 2 UTF-8 bytes
  const dv = new DataView(hdr.buffer);
  assert.equal(dv.getUint32(0, true), 6); // 2 + ".txt"
});

test('uploadEndMarker is a zero-length-name record', () => {
  const end = uploadEndMarker();
  assert.equal(new DataView(end.buffer).getUint32(0, true), 0);
});

// ---- readBlobChunks ----

// fakeBlob backs slice/arrayBuffer with an in-memory byte array.
function fakeBlob(bytes: Uint8Array): BlobLike {
  return {
    size: bytes.length,
    slice(start: number, end: number) {
      return { arrayBuffer: async () => bytes.slice(start, end).buffer };
    },
  };
}

test('readBlobChunks yields raw bytes in order that reassemble to the original', async () => {
  const bytes = new Uint8Array([1, 2, 3, 4, 5]);
  const chunks: Uint8Array[] = [];
  for await (const c of readBlobChunks(fakeBlob(bytes), 2)) chunks.push(c);
  assert.equal(chunks.length, 3); // 2 + 2 + 1
  assert.deepEqual(new Uint8Array(Buffer.concat(chunks)), bytes);
});

test('readBlobChunks yields nothing for a zero-byte file', async () => {
  const chunks: Uint8Array[] = [];
  for await (const c of readBlobChunks(fakeBlob(new Uint8Array(0)))) chunks.push(c);
  assert.equal(chunks.length, 0);
});

// ---- entriesFromFileList ----

test('entriesFromFileList prefers webkitRelativePath, falls back to name', () => {
  const files = [
    { name: 'a.txt', size: 1 },
    { name: 'img.jpg', size: 2, webkitRelativePath: 'photos/img.jpg' },
  ] as unknown as File[];
  const items = entriesFromFileList(files);
  assert.deepEqual(
    items.map((i) => i.relPath),
    ['a.txt', 'photos/img.jpg'],
  );
});

test('entriesFromFileList drops entries with unsafe relative paths', () => {
  const files = [
    { name: 'ok.txt', size: 1 },
    { name: 'evil', size: 1, webkitRelativePath: '../escape' },
  ] as unknown as File[];
  const items = entriesFromFileList(files);
  assert.deepEqual(
    items.map((i) => i.relPath),
    ['ok.txt'],
  );
});

// ---- entriesFromDataTransfer: resilient directory walk ----
//
// Fakes of the FileSystemEntry API so the recursion (and its error
// handling) can be unit-tested without a real OS drag. `fail:true` makes
// a file's .file() or a directory's readEntries() reject — the path that
// must be survived, not propagated.

function fileEntry(name: string, opts: { fail?: boolean } = {}): FileSystemEntry {
  return {
    isFile: true,
    isDirectory: false,
    name,
    file: (resolve: (f: File) => void, reject: (e: unknown) => void) =>
      opts.fail ? reject(new Error('unreadable file')) : resolve({ name, size: 1 } as unknown as File),
  } as unknown as FileSystemEntry;
}

function dirEntry(name: string, children: FileSystemEntry[], opts: { fail?: boolean } = {}): FileSystemEntry {
  return {
    isFile: false,
    isDirectory: true,
    name,
    createReader: () => {
      let drained = false;
      return {
        readEntries: (resolve: (e: FileSystemEntry[]) => void, reject: (e: unknown) => void) => {
          if (opts.fail) return reject(new Error('unreadable dir'));
          if (drained) return resolve([]);
          drained = true;
          resolve(children);
        },
      };
    },
  } as unknown as FileSystemEntry;
}

function itemList(entries: FileSystemEntry[]): DataTransferItemList {
  const items = entries.map((e) => ({ kind: 'file', webkitGetAsEntry: () => e, getAsFile: () => null }));
  return items as unknown as DataTransferItemList;
}

test('entriesFromDataTransfer skips an unreadable file but keeps its siblings', async () => {
  const tree = dirEntry('d', [fileEntry('a.txt'), fileEntry('bad.txt', { fail: true }), fileEntry('c.txt')]);
  const items = await entriesFromDataTransfer(itemList([tree]));
  assert.deepEqual(items.map((i) => i.relPath), ['d/a.txt', 'd/c.txt']);
});

test('entriesFromDataTransfer skips an unreadable subdirectory but keeps readable siblings', async () => {
  const tree = dirEntry('root', [
    fileEntry('top.txt'),
    dirEntry('locked', [fileEntry('secret.txt')], { fail: true }),
    dirEntry('open', [fileEntry('inner.txt')]),
  ]);
  const items = await entriesFromDataTransfer(itemList([tree]));
  assert.deepEqual(items.map((i) => i.relPath), ['root/top.txt', 'root/open/inner.txt']);
});

test('entriesFromDataTransfer survives a wholly-unreadable root and still collects the others', async () => {
  const items = await entriesFromDataTransfer(
    itemList([fileEntry('boom.txt', { fail: true }), dirEntry('d', [fileEntry('ok.txt')]), fileEntry('loose.txt')]),
  );
  assert.deepEqual(items.map((i) => i.relPath), ['d/ok.txt', 'loose.txt']);
});

test('entriesFromDataTransfer drops traversal-unsafe names mid-walk without aborting', async () => {
  // A FE-side sanitizeRel reject (defensive — webkitGetAsEntry names
  // shouldn't contain "..") must skip just that entry.
  const tree = dirEntry('d', [fileEntry('..'), fileEntry('good.txt')]);
  const items = await entriesFromDataTransfer(itemList([tree]));
  assert.deepEqual(items.map((i) => i.relPath), ['d/good.txt']);
});
