// Bundle accumulator. The router ships an app's JS bundle as raw
// bytes on a kind=bundle channel; the WS handler routes raw frames
// through here, and ChannelUnbind triggers a dynamic import.
//
// Replaces the v0.0 base64-in-JSON asset.fetch / asset.deliver pipe.

interface Pending {
  channelID: number;
  chunks: Uint8Array[];
  resolve: () => void;
  reject: (err: Error) => void;
  promise: Promise<void>;
}

// Keyed by instance_id — the router announces which channel maps to
// which instance via ShellChannelBind {kind:"bundle", instance_id}.
const pendingByInstance = new Map<string, Pending>();
const instanceByChannel = new Map<number, string>();

// beginBundle registers a fresh accumulator for instanceID waiting on
// channelID. Returns the promise that resolves once the import has
// run (or rejects on failure).
export function beginBundle(channelID: number, instanceID: string): Promise<void> {
  let resolve!: () => void;
  let reject!: (err: Error) => void;
  const promise = new Promise<void>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  const p: Pending = { channelID, chunks: [], resolve, reject, promise };
  pendingByInstance.set(instanceID, p);
  instanceByChannel.set(channelID, instanceID);
  return promise;
}

// pushBundleBytes accumulates raw frames arriving on a bundle channel.
// Returns true if the bytes were consumed, false otherwise (which the
// caller treats as a normal raw-channel frame).
export function pushBundleBytes(channelID: number, bytes: Uint8Array): boolean {
  const instanceID = instanceByChannel.get(channelID);
  if (instanceID == null) return false;
  const p = pendingByInstance.get(instanceID);
  if (!p) return false;
  p.chunks.push(bytes);
  return true;
}

// finishBundle is called from the ChannelUnbind handler when a bundle
// channel closes. Concatenates the chunks, builds a blob URL, dynamic-
// imports it (the bundle's customElements.define side effect makes
// the element tag live), then resolves the waiting promise.
export function finishBundle(channelID: number): void {
  const instanceID = instanceByChannel.get(channelID);
  if (instanceID == null) return;
  instanceByChannel.delete(channelID);
  const p = pendingByInstance.get(instanceID);
  if (!p) return;
  pendingByInstance.delete(instanceID);

  const blob = new Blob(p.chunks, { type: 'application/javascript' });
  const url = URL.createObjectURL(blob);
  import(/* @vite-ignore */ url)
    .then(() => {
      URL.revokeObjectURL(url);
      p.resolve();
    })
    .catch((err) => {
      URL.revokeObjectURL(url);
      p.reject(err instanceof Error ? err : new Error(String(err)));
    });
}
