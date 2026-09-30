// Native gzip inflate, shared by the asset (wash-fetch.ts) and bundle
// (assets.ts) accumulators. The router pre-compresses compressible assets
// and FE bundles (docs/QOS.md; internal/router/assetcache.go +
// registry.go) and flags them with encoding="gzip"; this undoes it.

// The ArrayBuffer type argument is load-bearing, not decoration: since TS 5.7
// a bare Uint8Array may be backed by a SharedArrayBuffer, which Blob does not
// accept. Every byte here arrives from `new Uint8Array(ev.data as ArrayBuffer)`
// or a local allocation, so naming the backing store is accurate — and keeps
// the Blob calls below type-checkable instead of merely untyped.
export async function gunzip(bytes: Uint8Array<ArrayBuffer>): Promise<Uint8Array<ArrayBuffer>> {
  const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream('gzip'));
  const buf = await new Response(stream).arrayBuffer();
  return new Uint8Array(buf);
}
