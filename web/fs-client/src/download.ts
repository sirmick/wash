// Download egress, FE half: the BE (internal/download) streams one or more
// confined paths back over a raw channel; this collects the bytes and
// hands them to save. A lone file arrives as-is, a directory or a
// multi-selection as one zip. Framework-free like the rest of the package:
// the raw channel, the save and the timers are injected, so fm and edit
// share it and the handshake is unit-testable without a browser.
//
//	request download_begin {paths}  → download_begin_ok {download_id, filename}
//	push    download_channel        {download_id, channel_id}
//	…bytes on the raw channel…
//	push    download_done           {download_id, status}
//
// download_done is the finalize signal: the BE sends it in the bulk lane
// behind the last byte, so every chunk is in hand when it lands.

import type { BEMessage, Clock } from './bus.ts';

export interface DownloadOptions {
  request(req: Record<string, unknown>): Promise<BEMessage>;
  /** Subscribe to a raw channel's bytes; returns the unsubscribe. */
  openRawChannel(channelID: number, onBytes: (bytes: Uint8Array) => void): () => void;
  /** Drop the finished bytes into the browser's downloads (saveDownload). */
  save(name: string, chunks: Uint8Array[]): void;
  /** A failure worth a status line; the BE has already toasted. */
  onError(msg: string): void;
  clock?: Clock;
}

export interface Downloads {
  run(paths: string[]): Promise<void>;
  /** Consumes download_channel / download_done pushes; false for anything else. */
  onMessage(m: BEMessage): boolean;
}

const realClock: Clock = {
  setTimeout: (fn, ms) => globalThis.setTimeout(fn, ms),
  clearTimeout: (h) => globalThis.clearTimeout(h as ReturnType<typeof setTimeout>),
};

// How long the BE has to announce the channel. No timeout spans the
// stream itself — a big zip can run a while.
const CHANNEL_WAIT_MS = 15_000;

export function createDownloads(opts: DownloadOptions): Downloads {
  const clock = opts.clock ?? realClock;
  const channels = new Map<string, (channelID: number) => void>();
  const done = new Map<string, (status: string) => void>();

  const run = async (paths: string[]) => {
    if (paths.length === 0) return;
    const begin = await opts.request({ kind: 'download_begin', paths });
    if (begin.kind !== 'download_begin_ok') {
      opts.onError(`download: ${String(begin.msg ?? begin.code ?? 'failed')}`);
      return;
    }
    const id = String(begin.download_id);
    const filename = String(begin.filename);
    // Registered before awaiting anything, so a download_done that beats
    // the channel (the BE failed to open it) still finds its waiter.
    const donePromise = new Promise<string>((resolve) => done.set(id, resolve));
    const channelID = await new Promise<number>((resolve) => {
      const timer = clock.setTimeout(() => {
        channels.delete(id);
        resolve(-1);
      }, CHANNEL_WAIT_MS);
      channels.set(id, (ch) => {
        clock.clearTimeout(timer);
        channels.delete(id);
        resolve(ch);
      });
    });
    if (channelID < 0) {
      done.delete(id);
      opts.onError('download: channel never opened');
      return;
    }
    const chunks: Uint8Array[] = [];
    // Copy: the shell may reuse the backing buffer after the callback.
    const unsubscribe = opts.openRawChannel(channelID, (bytes) => chunks.push(bytes.slice()));
    try {
      if ((await donePromise) === 'done') opts.save(filename, chunks);
      else opts.onError(`download: ${filename} failed`);
    } finally {
      unsubscribe();
      done.delete(id);
    }
  };

  const onMessage = (m: BEMessage): boolean => {
    const id = String(m.download_id);
    switch (m.kind) {
      case 'download_channel':
        channels.get(id)?.(Number(m.channel_id));
        return true;
      case 'download_done':
        // Also unblocks a still-pending channel waiter (the BE failed
        // before the channel opened) with the -1 sentinel.
        channels.get(id)?.(-1);
        done.get(id)?.(String(m.status));
        return true;
    }
    return false;
  };

  return { run, onMessage };
}
