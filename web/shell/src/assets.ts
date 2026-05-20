// Asset assembler: the shell sends asset.fetch and accumulates
// asset.deliver chunks until end=true, then dynamically imports the
// resulting bundle. The bundle self-registers its custom element.

interface Pending {
  chunks: Uint8Array[];
  mime: string;
  resolve: () => void;
  reject: (err: Error) => void;
}

const inflight = new Map<string, Pending>();

// fetchAndImport sends an asset.fetch, accumulates chunks, builds a
// Blob URL, and dynamic-imports the module. The bundle's side effect
// (customElements.define) makes the element tag live.
export function fetchAndImport(
  sendCtrl: (msg: unknown) => void,
  instanceID: string,
  name: string,
): Promise<void> {
  const key = keyOf(instanceID, name);
  if (inflight.has(key)) {
    return new Promise(() => {}); // already in flight; the handler will resolve all waiters
  }
  return new Promise<void>((resolve, reject) => {
    inflight.set(key, { chunks: [], mime: '', resolve, reject });
    sendCtrl({ t: 'asset.fetch', instance_id: instanceID, name });
  });
}

// onAssetDeliver is called by the WS handler for every asset.deliver.
// On end=true it kicks off the dynamic import.
export function onAssetDeliver(msg: any): void {
  const key = keyOf(msg.instance_id, msg.name);
  const p = inflight.get(key);
  if (!p) {
    console.warn('wash: asset.deliver for unknown', key);
    return;
  }
  if (msg.mime && !p.mime) p.mime = msg.mime;
  p.chunks.push(base64ToBytes(msg.bytes));
  if (msg.end) {
    inflight.delete(key);
    const blob = new Blob(p.chunks, { type: p.mime || 'application/javascript' });
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
}

function keyOf(instanceID: string, name: string): string {
  return instanceID + '|' + name;
}

function base64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}
