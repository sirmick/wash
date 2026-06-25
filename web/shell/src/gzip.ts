// Native gzip inflate, shared by the asset (wash-fetch.ts) and bundle
// (assets.ts) accumulators. The router pre-compresses compressible assets
// and FE bundles (docs/QOS.md; internal/router/assetcache.go +
// registry.go) and flags them with encoding="gzip"; this undoes it.

export async function gunzip(bytes: Uint8Array): Promise<Uint8Array> {
  const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream('gzip'));
  const buf = await new Response(stream).arrayBuffer();
  return new Uint8Array(buf);
}
