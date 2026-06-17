// file-client — the FE half of the image-bytes transport (internal/thumbs
// on the BE). It asks the app's own BE for a file's bytes (full or a
// thumbnail), collects them off a raw channel, and hands back a blob URL
// usable as an <img src>. No HTTP/ingress: this rides the existing wire, so
// it works on the in-browser VM too. See docs/IMAGES.md.
//
// The exchange mirrors fm's download egress:
//   FE → app   get_file     {req_id, path, dim}
//   app → FE   file_channel {req_id, channel_id, mime}   then raw bytes
//   app → FE   file_done    {req_id, status, error}
//
// Results are cached by (dim,path) as blob URLs (FIFO-evicted, revoked on
// drop) and requests are capped so a folder of thumbnails can't flood the
// single WS (head-of-line blocking — cf. fm upload). Pair with an
// IntersectionObserver in the view so only on-screen tiles are fetched.

interface WashRawApi {
  sendAppMsg(instanceID: string, data: unknown): void;
  openRawChannel(channelID: number, onBytes: (bytes: Uint8Array) => void): () => void;
}
const washApi = (): WashRawApi => (window as unknown as { wash: WashRawApi }).wash;

export interface FileClientOptions {
  /** the app's own instance id (props.instance). */
  instance: string;
  /** the host element to listen on for the file_channel/file_done pushes. */
  host: HTMLElement;
  /** max cached THUMBNAIL (dim>0) blob URLs before FIFO eviction (default 300). */
  maxCache?: number;
  /** max cached FULL-image (dim=0) blob URLs (default 16). Kept separate from
   *  thumbnails so thumbnail churn can't revoke the on-screen image. */
  maxFull?: number;
  /** max in-flight fetches (default 6) — the rest queue. */
  maxConcurrent?: number;
  /** per-request timeout in ms (default 30_000). */
  timeoutMs?: number;
}

export interface FileUrlOptions {
  /** >0 → request a thumbnail with this max edge; omit/0 → full bytes. */
  dim?: number;
}

export interface FileClient {
  /** resolve a blob URL for path (cached). Reject on BE error/timeout. */
  url(path: string, opts?: FileUrlOptions): Promise<string>;
  /** revoke every blob URL and detach the listener. */
  dispose(): void;
}

interface ChannelInfo {
  channelID: number;
  mime: string;
}

let seq = 0;

export function createFileClient(opts: FileClientOptions): FileClient {
  const maxThumb = opts.maxCache ?? 300;
  // Full-resolution images get their OWN small cache, kept SEPARATE from the
  // thumbnail cache. Otherwise churning thumbnails (a folder of thousands)
  // would FIFO-evict and revoke the blob URL of the image currently on
  // screen — the displayed <img> would break. The current image is always
  // the most-recently-added to fullCache, so it never gets evicted.
  const maxFull = opts.maxFull ?? 16;
  const maxConcurrent = opts.maxConcurrent ?? 6;
  const timeoutMs = opts.timeoutMs ?? 30_000;

  // req_id → resolver for each of the two BE pushes.
  const onChannel = new Map<string, (info: ChannelInfo) => void>();
  const onDone = new Map<string, (r: { status: string; error?: string }) => void>();

  // Two `${dim}:${path}` → blob-URL-promise caches, bucketed by full
  // (dim===0) vs thumbnail (dim>0). resolvedUrl mirrors both once settled so
  // eviction/dispose can revoke. Map iteration order = insertion order → FIFO.
  const fullCache = new Map<string, Promise<string>>();
  const thumbCache = new Map<string, Promise<string>>();
  const resolvedUrl = new Map<string, string>();

  let active = 0;
  const waiters: Array<() => void> = [];
  const acquire = (): Promise<void> =>
    new Promise((res) => {
      if (active < maxConcurrent) {
        active++;
        res();
      } else {
        waiters.push(() => {
          active++;
          res();
        });
      }
    });
  const release = () => {
    active--;
    waiters.shift()?.();
  };

  const onMsg = (ev: Event) => {
    const m = (ev as CustomEvent).detail as
      | { kind?: string; req_id?: string; channel_id?: number; mime?: string; status?: string; error?: string }
      | undefined;
    if (!m?.req_id) return;
    if (m.kind === 'file_channel') {
      onChannel.get(m.req_id)?.({ channelID: Number(m.channel_id), mime: String(m.mime ?? 'application/octet-stream') });
    } else if (m.kind === 'file_done') {
      onDone.get(m.req_id)?.({ status: String(m.status ?? 'failed'), error: m.error });
    }
  };
  opts.host.addEventListener('wash:msg', onMsg);

  const fetchOne = async (path: string, dim: number): Promise<string> => {
    await acquire();
    const reqID = `${opts.instance}-fc-${++seq}`;
    const chunks: Uint8Array[] = [];
    let unsubscribe: (() => void) | null = null;
    let timer: number | undefined;

    const channelPromise = new Promise<ChannelInfo>((res) => onChannel.set(reqID, res));
    const donePromise = new Promise<{ status: string; error?: string }>((res) => onDone.set(reqID, res));
    const timeout = new Promise<never>((_, rej) => {
      timer = window.setTimeout(() => rej(new Error('file fetch timeout')), timeoutMs);
    });

    try {
      washApi().sendAppMsg(opts.instance, { kind: 'get_file', req_id: reqID, path, dim });
      // A failure before any channel opens arrives as file_done only, so
      // race the channel against an early done.
      const first = await Promise.race([
        channelPromise.then((info) => ({ t: 'channel' as const, info })),
        donePromise.then((d) => ({ t: 'done' as const, d })),
        timeout,
      ]);
      if (first.t === 'done') {
        throw new Error(first.d.error || 'file fetch failed');
      }
      // Subscribe; the shell buffers any bytes that landed before now and
      // flushes them on subscribe, so nothing is lost between the
      // file_channel push and here.
      unsubscribe = washApi().openRawChannel(first.info.channelID, (bytes) => {
        chunks.push(bytes.slice()); // copy: the shell may reuse the buffer
      });
      const done = await Promise.race([donePromise, timeout]);
      if (done.status !== 'done') {
        throw new Error(done.error || 'file fetch failed');
      }
      return URL.createObjectURL(new Blob(chunks as BlobPart[], { type: first.info.mime }));
    } finally {
      if (timer !== undefined) clearTimeout(timer);
      unsubscribe?.();
      onChannel.delete(reqID);
      onDone.delete(reqID);
      release();
    }
  };

  const url = (path: string, o?: FileUrlOptions): Promise<string> => {
    const dim = o?.dim && o.dim > 0 ? Math.round(o.dim) : 0;
    const k = `${dim}:${path}`;
    const cache = dim === 0 ? fullCache : thumbCache;
    const cap = dim === 0 ? maxFull : maxThumb;
    const hit = cache.get(k);
    if (hit) return hit;
    const p = fetchOne(path, dim)
      .then((u) => {
        resolvedUrl.set(k, u);
        // Evict only within this bucket — never let thumbnails revoke a full
        // image (or vice versa).
        if (cache.size > cap) {
          const oldest = cache.keys().next().value as string | undefined;
          if (oldest !== undefined && oldest !== k) {
            cache.delete(oldest);
            const ou = resolvedUrl.get(oldest);
            if (ou) {
              URL.revokeObjectURL(ou);
              resolvedUrl.delete(oldest);
            }
          }
        }
        return u;
      })
      .catch((e) => {
        cache.delete(k); // let a later call retry
        throw e;
      });
    cache.set(k, p);
    return p;
  };

  const dispose = () => {
    opts.host.removeEventListener('wash:msg', onMsg);
    for (const u of resolvedUrl.values()) URL.revokeObjectURL(u);
    resolvedUrl.clear();
    fullCache.clear();
    thumbCache.clear();
    onChannel.clear();
    onDone.clear();
  };

  return { url, dispose };
}
