// Upload collection + framing. Framework-free (no Solid); the DOM
// touchpoints (DataTransfer, FileSystemEntry, Blob) are standard web
// types referenced only for shape — the pure helpers (sanitizeRel,
// planUpload, topLevelLabels, totalBytes, encodeRecordHeader) carry
// the logic and are unit-tested with node:test. The thin DOM glue
// (entriesFromDataTransfer, readBlobChunks) is exercised by the fm
// e2e.
//
// Wire shape: fm streams the file BYTES over a raw wash channel (no
// base64, credit-flow backpressure), not the JSON bus. The byte
// stream is a sequence of self-delimiting records —
//
//   [u32 relPathLen LE][relPath utf8][u64 size LE][size bytes] …
//   [u32 0]                                          ← end marker
//
// The metadata (conflict check, begin, channel handshake, done) rides
// the JSON bus; only the payload goes binary. A directory drop/upload
// carries slash-separated relative paths (File.webkitRelativePath); a
// plain file upload carries a bare name.

// One file to upload, with its destination-relative path. For a plain
// file this is just the name; for a directory upload it includes the
// in-tree path ("photos/2024/img.jpg").
export interface UploadItem {
  relPath: string;
  file: File;
}

// 1 MiB raw → ~1.37 MiB base64, comfortably under the router's 16 MiB
// frame cap while keeping per-chunk overhead low.
export const UPLOAD_CHUNK_SIZE = 1 << 20;

// sanitizeRel mirrors the BE's fs.SanitizeRel: reject absolute paths
// and any path with empty / "." / ".." components so a relative path
// can't escape its destination. Returns the normalized "/"-joined
// path, or null if invalid. Keeps "/" separators (the wire form the
// BE splits on).
export function sanitizeRel(rel: string): string | null {
  if (!rel) return null;
  const norm = rel.replace(/\\/g, '/');
  if (norm.startsWith('/')) return null;
  const parts = norm.split('/');
  for (const p of parts) {
    if (p === '' || p === '.' || p === '..') return null;
  }
  return parts.join('/');
}

// totalBytes sums the byte sizes of an item set — the denominator of
// the bulk progress bar.
export function totalBytes(items: readonly UploadItem[]): number {
  return items.reduce((n, it) => n + (it.file?.size ?? 0), 0);
}

// topLevelLabels returns the unique first path components, in order —
// the human label for the bulk job ("upload 2 items": photos, notes).
export function topLevelLabels(rels: readonly string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const r of rels) {
    const top = r.split('/')[0];
    if (top && !seen.has(top)) {
      seen.add(top);
      out.push(top);
    }
  }
  return out;
}

export interface UploadPlan {
  entries: UploadItem[];
  totalBytes: number;
  labels: string[];
}

// planUpload applies the conflict policy to a collected item set: under
// "skip" it drops items whose relPath already exists at the
// destination (the conflict set), under "replace" it keeps everything.
// The returned totalBytes/labels reflect only the entries that will
// actually be sent, so the bulk progress denominator is accurate.
export function planUpload(
  items: readonly UploadItem[],
  conflicts: ReadonlySet<string>,
  policy: 'replace' | 'skip',
): UploadPlan {
  const entries = policy === 'skip' ? items.filter((it) => !conflicts.has(it.relPath)) : [...items];
  return {
    entries,
    totalBytes: totalBytes(entries),
    labels: topLevelLabels(entries.map((e) => e.relPath)),
  };
}

// encodeRecordHeader builds the per-file header that precedes a file's
// bytes on the raw channel: [u32 relPathLen LE][relPath utf8][u64 size
// LE]. The BE reads this, then reads exactly `size` payload bytes.
export function encodeRecordHeader(relPath: string, size: number): Uint8Array {
  const path = new TextEncoder().encode(relPath);
  const buf = new Uint8Array(4 + path.length + 8);
  const dv = new DataView(buf.buffer);
  dv.setUint32(0, path.length, true);
  buf.set(path, 4);
  dv.setBigUint64(4 + path.length, BigInt(size), true);
  return buf;
}

// uploadEndMarker is the zero-length-name record that signals
// end-of-stream — the BE finalizes the upload when it reads it.
export function uploadEndMarker(): Uint8Array {
  return new Uint8Array([0, 0, 0, 0]);
}

// BlobLike is the minimal slice-and-read surface readBlobChunks needs.
// The DOM's Blob/File satisfy it; tests pass a fake.
export interface BlobLike {
  readonly size: number;
  slice(start: number, end: number): { arrayBuffer(): Promise<ArrayBuffer> };
}

// readBlobChunks yields a blob's raw bytes in order, chunkSize at a
// time. Reading a slice at a time (vs the whole file) bounds memory and
// paces the writeRaw stream against the FileReader. A zero-byte file
// yields nothing — its header (size 0) already tells the BE to create
// an empty file.
export async function* readBlobChunks(
  blob: BlobLike,
  chunkSize: number = UPLOAD_CHUNK_SIZE,
): AsyncGenerator<Uint8Array> {
  const size = blob.size;
  let off = 0;
  while (off < size) {
    const end = Math.min(off + chunkSize, size);
    const buf = await blob.slice(off, end).arrayBuffer();
    off = end;
    yield new Uint8Array(buf);
  }
}

// entriesFromFileList flattens an <input type=file> selection into
// UploadItems. A `webkitdirectory` picker populates webkitRelativePath
// with the in-tree path; a plain multiple picker leaves it empty, so
// we fall back to the bare name. Invalid (escaping) relative paths are
// dropped defensively — they can't be uploaded safely.
export function entriesFromFileList(files: ArrayLike<File>): UploadItem[] {
  const out: UploadItem[] = [];
  for (let i = 0; i < files.length; i++) {
    const f = files[i];
    const raw = (f as { webkitRelativePath?: string }).webkitRelativePath || f.name;
    const rel = sanitizeRel(raw);
    if (rel) out.push({ relPath: rel, file: f });
  }
  return out;
}

// ----- DOM drag-drop glue (e2e-covered) -----

// entriesFromDataTransfer flattens a drop's items into UploadItems,
// recursing into dropped directories via the webkitGetAsEntry API.
//
// IMPORTANT: webkitGetAsEntry must be called synchronously while the
// drop event's DataTransferItemList is still alive — so we snapshot the
// root entries in a tight non-async loop before any await, then walk
// them asynchronously.
export async function entriesFromDataTransfer(items: DataTransferItemList): Promise<UploadItem[]> {
  const roots: FileSystemEntry[] = [];
  const looseFiles: File[] = [];
  for (let i = 0; i < items.length; i++) {
    const it = items[i];
    if (it.kind !== 'file') continue;
    const entry = it.webkitGetAsEntry?.();
    if (entry) {
      roots.push(entry);
    } else {
      const f = it.getAsFile();
      if (f) looseFiles.push(f);
    }
  }
  const out: UploadItem[] = entriesFromFileList(looseFiles);
  for (const entry of roots) {
    await walkEntry(entry, '', out);
  }
  return out;
}

// walkEntry is RESILIENT by design: a single unreadable file or
// directory (a permission error, a file deleted between drag and read, a
// flaky readEntries) must not abort the whole drop — the user dragged a
// tree in and expects everything readable to land. So each file read and
// each directory listing is guarded; a failure skips that entry (or that
// subtree) and the walk continues with its siblings, so walkEntry never
// rejects. Without this, one bad leaf rejected the entire
// entriesFromDataTransfer promise — which collectFromDrop fires with no
// catch — so NOTHING uploaded and the failure was silent.
async function walkEntry(entry: FileSystemEntry, prefix: string, out: UploadItem[]): Promise<void> {
  if (entry.isFile) {
    let file: File;
    try {
      file = await fileFromEntry(entry as FileSystemFileEntry);
    } catch {
      return; // unreadable file — skip it, keep the rest of the drop
    }
    const rel = sanitizeRel(prefix + entry.name);
    if (rel) out.push({ relPath: rel, file });
    return;
  }
  if (entry.isDirectory) {
    let children: FileSystemEntry[];
    try {
      children = await readAllEntries((entry as FileSystemDirectoryEntry).createReader());
    } catch {
      return; // unreadable directory — skip the subtree, keep the rest
    }
    for (const c of children) {
      await walkEntry(c, prefix + entry.name + '/', out); // each child guards itself
    }
  }
}

function fileFromEntry(entry: FileSystemFileEntry): Promise<File> {
  return new Promise((resolve, reject) => entry.file(resolve, reject));
}

// readAllEntries drains a directory reader. readEntries returns the
// children in batches and signals completion with an empty batch, so
// we loop until it's dry.
function readAllEntries(reader: FileSystemDirectoryReader): Promise<FileSystemEntry[]> {
  return new Promise((resolve, reject) => {
    const all: FileSystemEntry[] = [];
    const pump = () => {
      reader.readEntries((batch) => {
        if (batch.length === 0) {
          resolve(all);
          return;
        }
        all.push(...batch);
        pump();
      }, reject);
    };
    pump();
  });
}
