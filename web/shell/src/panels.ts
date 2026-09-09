// Settings-panel loader — fetch an app's panel.js bundle from the
// router and blob-import it so its custom element becomes defined,
// without spawning the owning app. Combines wash-fetch.ts's
// request/accumulate dance with assets.ts's blob-URL import.
//
// Wire dance (see wire.ShellPanelRead / ShellPanelReadOK / …):
//   shell → router:  { t: "panel.read",     req_id, app_id }
//   router → shell:  { t: "panel.read.ok",  req_id, channel_id, size }
//   router → shell:  { t: "channel.bind",   channel_id, kind: "bundle" }
//   router → shell:  <raw panel.js bytes on channel_id>
//   router → shell:  { t: "channel.unbind", channel_id, reason }
//   on error:        { t: "panel.read.err", req_id, code, msg }

import { wlog } from './diag.ts';

interface Pending {
  reqID: number;
  appID: string;
  channelID?: number; // set by handlePanelReadOK; bytes/finish keyed off it
  // size is the byte count the router promised in panel.read.ok. The
  // import fires when that many bytes have arrived rather than when the
  // Unbind lands, which is what lets the data ride a lower priority
  // lane than the control frame that follows it — the same rule
  // assets.ts uses for app bundles.
  size: number;
  chunks: Uint8Array[];
  resolve: () => void;
  reject: (err: Error) => void;
}

const pendingByReqID = new Map<number, Pending>();
const pendingByChannelID = new Map<number, Pending>();
// One load per app id: the panel element only needs defining once, so
// repeat calls share the first call's promise (resolved or in flight).
const loadedByAppID = new Map<string, Promise<void>>();
let nextReqID = 1;

type SendCtrl = (msg: unknown) => void;

/** loadSettingsPanel asks the router for appID's panel bundle, imports
 *  it (defining its custom element), and resolves once the import has
 *  run. Idempotent per app id — concurrent/repeat calls share one load.
 *  Rejects if the router replies panel.read.err. */
export function loadSettingsPanel(send: SendCtrl, appID: string): Promise<void> {
  const existing = loadedByAppID.get(appID);
  if (existing) return existing;
  const p = new Promise<void>((resolve, reject) => {
    const reqID = nextReqID++;
    const pending: Pending = { reqID, appID, size: 0, chunks: [], resolve, reject };
    pendingByReqID.set(reqID, pending);
    send({ t: 'panel.read', req_id: reqID, app_id: appID });
  });
  // On failure, drop the cache entry so a later retry can re-request.
  p.catch(() => loadedByAppID.delete(appID));
  loadedByAppID.set(appID, p);
  return p;
}

/** handlePanelReadOK records the channel→pending mapping so incoming
 *  raw frames flow into the accumulator. */
export function handlePanelReadOK(msg: { req_id: number; channel_id: number; size: number }): void {
  const p = pendingByReqID.get(msg.req_id);
  if (!p) return;
  p.channelID = msg.channel_id;
  p.size = msg.size;
  pendingByChannelID.set(msg.channel_id, p);
  // A zero-byte panel has all of its bytes already.
  maybeImportPanel(p);
}

/** handlePanelReadErr rejects the matching pending load. */
export function handlePanelReadErr(msg: { req_id: number; code: string; msg?: string }): void {
  const p = pendingByReqID.get(msg.req_id);
  if (!p) return;
  pendingByReqID.delete(msg.req_id);
  p.reject(new Error(`panel.read.err [${msg.code}]: ${msg.msg ?? ''}`));
}

/** pushPanelBytes feeds raw bytes from a panel channel into the
 *  accumulator. Returns true if consumed. */
export function pushPanelBytes(channelID: number, bytes: Uint8Array): boolean {
  const p = pendingByChannelID.get(channelID);
  if (!p) return false;
  p.chunks.push(bytes);
  maybeImportPanel(p);
  return true;
}

/** finishPanel is called from the channel.unbind handler. Byte-count
 *  completion (maybeImportPanel) normally gets there first; this stays
 *  as the path for a stream that ends short — a router-side read error
 *  mid-transfer — so the caller is rejected instead of hanging on a
 *  promise that can never settle. No-op for non-panel channels. */
export function finishPanel(channelID: number): void {
  const p = pendingByChannelID.get(channelID);
  if (!p) return;
  pendingByChannelID.delete(channelID);
  pendingByReqID.delete(p.reqID);
  p.reject(new Error(`panel stream ended after ${byteCount(p)} of ${p.size} bytes`));
}

function byteCount(p: Pending): number {
  return p.chunks.reduce((n, c) => n + c.byteLength, 0);
}

/** importPanelBytes is the DOM step: wrap the accumulated chunks in a
 *  blob URL and evaluate the module, whose customElements.define side
 *  effect is the whole point. Named as a seam because a blob URL cannot
 *  be imported outside a browser, which would otherwise leave the
 *  completion rule around it untestable. */
export type PanelImporter = (chunks: Uint8Array[]) => Promise<void>;

let importPanelBytes: PanelImporter = (chunks) => {
  const blob = new Blob(chunks as BlobPart[], { type: 'application/javascript' });
  const url = URL.createObjectURL(blob);
  return import(/* @vite-ignore */ url).then(
    () => { URL.revokeObjectURL(url); },
    (err) => { URL.revokeObjectURL(url); throw err; },
  );
};

/** setPanelImporter replaces the DOM step. For tests only. */
export function setPanelImporter(fn: PanelImporter): void {
  importPanelBytes = fn;
}

/** maybeImportPanel imports the panel module once every promised byte
 *  has arrived. Idempotent: removal from the maps is the guard. */
function maybeImportPanel(p: Pending): void {
  if (p.channelID === undefined) return;
  if (byteCount(p) < p.size) return;
  pendingByChannelID.delete(p.channelID);
  pendingByReqID.delete(p.reqID);

  importPanelBytes(p.chunks)
    .then(() => {
      p.resolve();
    })
    .catch((err) => {
      const stack = err instanceof Error ? err.stack ?? err.message : String(err);
      wlog(`panel bundle FAILED: app=${p.appID} stack=${stack}`);
      p.reject(err instanceof Error ? err : new Error(String(err)));
    });
}
