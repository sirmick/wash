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

import { wlog } from './diag';

interface Pending {
  reqID: number;
  appID: string;
  channelID?: number; // set by handlePanelReadOK; bytes/finish keyed off it
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
    const pending: Pending = { reqID, appID, chunks: [], resolve, reject };
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
  pendingByChannelID.set(msg.channel_id, p);
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
  return true;
}

/** finishPanel is called from the channel.unbind handler. If the
 *  channel was a panel stream, concatenates the chunks, blob-imports
 *  the module (its customElements.define side effect makes the panel
 *  element live), and resolves the originating loadSettingsPanel.
 *  No-op for non-panel channels. */
export function finishPanel(channelID: number): void {
  const p = pendingByChannelID.get(channelID);
  if (!p) return;
  pendingByChannelID.delete(channelID);
  pendingByReqID.delete(p.reqID);

  const blob = new Blob(p.chunks as BlobPart[], { type: 'application/javascript' });
  const url = URL.createObjectURL(blob);
  import(/* @vite-ignore */ url)
    .then(() => {
      URL.revokeObjectURL(url);
      p.resolve();
    })
    .catch((err) => {
      URL.revokeObjectURL(url);
      const stack = err instanceof Error ? err.stack ?? err.message : String(err);
      wlog(`panel bundle FAILED: app=${p.appID} stack=${stack}`);
      p.reject(err instanceof Error ? err : new Error(String(err)));
    });
}
