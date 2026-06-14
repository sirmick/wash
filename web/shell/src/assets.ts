// Bundle accumulator. The router ships an app's JS bundle as raw
// bytes on a kind=bundle channel; the WS handler routes raw frames
// through here, and ChannelUnbind triggers a dynamic import.

import { wlog } from './diag';
import { type Origin, LOCAL_ORIGIN, compoundInstanceId, compoundChannelId } from './clients';

interface Pending {
  channelID: number;
  chunks: Uint8Array[];
  resolve: () => void;
  reject: (err: Error) => void;
  promise: Promise<void>;
  // Origin of the router that served this bundle, so the import can be
  // tagged for per-origin element mangling (web/lib defineWashApp).
  origin: Origin;
}

// Keyed by COMPOUND ids (origin-tagged) — the router announces which
// channel maps to which instance via ShellChannelBind {kind:"bundle",
// instance_id}, and channel/instance ids are per-connection, so two
// routers' bundle channels must not collide in these shared maps.
const pendingByInstance = new Map<string, Pending>(); // compoundInstanceId → Pending
const instanceByChannel = new Map<string, string>(); // compoundChannelId → compoundInstanceId

// beginBundle registers a fresh accumulator for instanceID waiting on
// channelID. Returns the promise that resolves once the import has
// run (or rejects on failure).
export function beginBundle(channelID: number, instanceID: string, origin: Origin = LOCAL_ORIGIN): Promise<void> {
  let resolve!: () => void;
  let reject!: (err: Error) => void;
  const promise = new Promise<void>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  const p: Pending = { channelID, chunks: [], resolve, reject, promise, origin };
  const ik = compoundInstanceId(origin, instanceID);
  pendingByInstance.set(ik, p);
  instanceByChannel.set(compoundChannelId(origin, channelID), ik);
  return promise;
}

// Bundle imports are serialized through one chain so the shared
// __washImportOrigin global — read by web/lib defineWashApp during the
// import's synchronous customElements.define — is never set for the wrong
// bundle when two arrive concurrently. It is set only for REMOTE origins;
// local/standalone imports leave it unset so their path is byte-for-byte
// unchanged.
let importChain: Promise<void> = Promise.resolve();

function runImport(url: string, origin: Origin): Promise<void> {
  const g = globalThis as unknown as { __washImportOrigin?: string };
  const next = importChain.then(async () => {
    if (origin && origin !== LOCAL_ORIGIN) g.__washImportOrigin = origin;
    else delete g.__washImportOrigin;
    try {
      await import(/* @vite-ignore */ url);
    } finally {
      delete g.__washImportOrigin;
    }
  });
  // Keep the chain alive even if one import rejects.
  importChain = next.catch(() => {});
  return next;
}

// pushBundleBytes accumulates raw frames arriving on a bundle channel.
// Returns true if the bytes were consumed, false otherwise (which the
// caller treats as a normal raw-channel frame).
export function pushBundleBytes(channelID: number, bytes: Uint8Array, origin: Origin = LOCAL_ORIGIN): boolean {
  const ik = instanceByChannel.get(compoundChannelId(origin, channelID));
  if (ik == null) return false;
  const p = pendingByInstance.get(ik);
  if (!p) return false;
  p.chunks.push(bytes);
  return true;
}

// finishBundle is called from the ChannelUnbind handler when a bundle
// channel closes. Concatenates the chunks, builds a blob URL, dynamic-
// imports it (the bundle's customElements.define side effect makes
// the element tag live), then resolves the waiting promise.
export function finishBundle(channelID: number, origin: Origin = LOCAL_ORIGIN): void {
  const ck = compoundChannelId(origin, channelID);
  const instanceID = instanceByChannel.get(ck);
  if (instanceID == null) return;
  instanceByChannel.delete(ck);
  const p = pendingByInstance.get(instanceID);
  if (!p) return;
  pendingByInstance.delete(instanceID);

  const blob = new Blob(p.chunks, { type: 'application/javascript' });
  const url = URL.createObjectURL(blob);

  let settled = false;
  // Watchdog: if a future regression pauses async module resolution
  // (e.g. hidden-tab throttling on a Blob-URL `import()`), the
  // bundle would never define its custom element and the desktop
  // would silently stay blank. Scream after 5s so the symptom is
  // visible without devtools.
  const watchdog = setTimeout(() => {
    if (settled) return;
    wlog(`bundle PENDING after 5s: inst=${instanceID} ch=${channelID} viz=${document.visibilityState} focus=${document.hasFocus()}`);
  }, 5000);

  runImport(url, p.origin)
    .then(() => {
      settled = true;
      clearTimeout(watchdog);
      URL.revokeObjectURL(url);
      p.resolve();
    })
    .catch((err) => {
      settled = true;
      clearTimeout(watchdog);
      URL.revokeObjectURL(url);
      const stack = err instanceof Error ? err.stack ?? err.message : String(err);
      wlog(`bundle FAILED: inst=${instanceID} stack=${stack}`);
      p.reject(err instanceof Error ? err : new Error(String(err)));
    });
}
