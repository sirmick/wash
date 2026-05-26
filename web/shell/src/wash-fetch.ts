// washFetch — pull a single file from the router's embedded asset FS
// over the existing WS bus, with no involvement from the host static
// server. Mirrors the bundle accumulator (assets.ts) shape but with a
// req_id/path request instead of an instance_id binding.
//
// Wire dance (see wire.ShellAssetRead / ShellAssetReadOK / ShellAssetReadErr):
//   shell → router:  { t: "asset.read",      req_id, path }
//   router → shell:  { t: "asset.read.ok",   req_id, channel_id, size, mime }
//   router → shell:  { t: "channel.bind",    channel_id, kind: "asset" }
//   router → shell:  <raw bytes on channel_id, possibly many frames>
//   router → shell:  { t: "channel.unbind",  channel_id, reason }
//   on error:        { t: "asset.read.err",  req_id, code, msg }

interface Pending {
  reqID: number;
  channelID?: number;     // set by handleAssetReadOK; bytes/finish keyed off it
  chunks: Uint8Array[];
  mime: string;
  size: number;
  resolve: (v: { bytes: Uint8Array; mime: string }) => void;
  reject: (err: Error) => void;
}

const pendingByReqID = new Map<number, Pending>();
const pendingByChannelID = new Map<number, Pending>();
let nextReqID = 1;

type SendCtrl = (msg: unknown) => void;

/** washFetch(path): asks the router for /path, resolves with the file
 *  bytes and mime once the asset channel closes. Rejects if the router
 *  replies with asset.read.err. */
export function washFetch(send: SendCtrl, path: string): Promise<{ bytes: Uint8Array; mime: string }> {
  const reqID = nextReqID++;
  return new Promise((resolve, reject) => {
    const p: Pending = { reqID, chunks: [], mime: '', size: 0, resolve, reject };
    pendingByReqID.set(reqID, p);
    send({ t: 'asset.read', req_id: reqID, path });
  });
}

/** handleAssetReadOK is called by main.tsx's ctrl dispatcher when an
 *  asset.read.ok frame arrives. Records the channel-id mapping so
 *  incoming raw frames flow into the pending accumulator. */
export function handleAssetReadOK(msg: { req_id: number; channel_id: number; size: number; mime?: string }): void {
  const p = pendingByReqID.get(msg.req_id);
  if (!p) return;
  p.channelID = msg.channel_id;
  p.mime = msg.mime ?? 'application/octet-stream';
  p.size = msg.size;
  pendingByChannelID.set(msg.channel_id, p);
}

/** handleAssetReadErr rejects the matching pending fetch. */
export function handleAssetReadErr(msg: { req_id: number; code: string; msg?: string }): void {
  const p = pendingByReqID.get(msg.req_id);
  if (!p) return;
  pendingByReqID.delete(msg.req_id);
  p.reject(new Error(`asset.read.err [${msg.code}]: ${msg.msg ?? ''}`));
}

/** pushAssetBytes feeds raw bytes from an asset channel into the
 *  accumulator. Returns true if consumed (so main.tsx's raw-frame
 *  dispatcher can fall through to bundle/raw-channel handlers
 *  otherwise). */
export function pushAssetBytes(channelID: number, bytes: Uint8Array): boolean {
  const p = pendingByChannelID.get(channelID);
  if (!p) return false;
  p.chunks.push(bytes);
  return true;
}

/** finishAsset is called from the channel.unbind handler. If the
 *  channel was an asset stream, concatenates the chunks and resolves
 *  the originating washFetch. No-op for non-asset channels. */
export function finishAsset(channelID: number): void {
  const p = pendingByChannelID.get(channelID);
  if (!p) return;
  pendingByChannelID.delete(channelID);
  pendingByReqID.delete(p.reqID);
  const total = p.chunks.reduce((n, c) => n + c.byteLength, 0);
  const out = new Uint8Array(total);
  let off = 0;
  for (const c of p.chunks) { out.set(c, off); off += c.byteLength; }
  p.resolve({ bytes: out, mime: p.mime });
}
