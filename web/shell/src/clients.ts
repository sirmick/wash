// Multi-router client registry (docs/REMOTE.md, R2).
//
// The shell talks to one router today (the local one) and, with the
// remote-apps work, to one extra router per remote host reached over an
// `ssh -L` tunnel. Each connection is a RouterClient (router-client.ts).
// This module is the tab-global registry that maps an Origin → its
// client, plus the codec that tags app-facing ids with their origin so
// the single global `window.wash` can route a call back to the router
// that owns the addressed instance.
//
// Design (FE plan): internal shell maps stay keyed by BARE router ids
// and live on the owning RouterClient, so ids never collide across
// connections (channel 42 on A is a different map entry than channel 42
// on B). The ONLY place an origin tag appears is the app-facing boundary
// — `data-wash-instance` and the `window.wash` methods — because there a
// single global object must decide which client to dispatch to.
//
// The LOCAL origin is left UNPREFIXED: a local app sees exactly the bare
// instance id it always did, so the single-host path is byte-for-byte
// unchanged. Only remote origins carry a prefix.

import type { RouterClient } from './router-client';

export type Origin = string;

/** The origin of the shell's own (local) router. Unprefixed on the wire. */
export const LOCAL_ORIGIN: Origin = 'local';

// Separator between origin and bare id in a compound app-facing id.
// U+241F (visible "unit separator") is printable, attribute-safe, and
// effectively impossible in a hostname or a router-assigned id, so a
// round-trip is unambiguous and a stray separator can't be mistaken.
const SEP = '␟';

/**
 * compoundInstanceId tags a bare router-assigned id with its origin for
 * the app-facing surface. LOCAL is returned unchanged so local apps see
 * the same bare id as before.
 */
export function compoundInstanceId(origin: Origin, bare: string): string {
  return origin === LOCAL_ORIGIN ? bare : origin + SEP + bare;
}

/**
 * parseInstanceId splits an app-facing id back into its origin and the
 * bare id the owning router understands. An id with no separator is a
 * LOCAL id (the unprefixed common case).
 */
export function parseInstanceId(id: string): { origin: Origin; bare: string } {
  const i = id.indexOf(SEP);
  if (i < 0) return { origin: LOCAL_ORIGIN, bare: id };
  return { origin: id.slice(0, i), bare: id.slice(i + 1) };
}

// ---- Registry ----

const clientByOrigin = new Map<Origin, RouterClient>();

// channelToClient routes a raw-channel id (as seen by window.wash's
// openRawChannel/writeRaw, which take a bare numeric id) back to the
// client that bound it. Populated by each client's channel.bind handling
// once remote clients exist (M1f); tab-global because the window.wash raw
// API has no instance context to disambiguate by.
export const channelToClient = new Map<number, RouterClient>();

/** registerClient adds (or replaces) the client for an origin. */
export function registerClient(origin: Origin, client: RouterClient): void {
  clientByOrigin.set(origin, client);
}

/** unregisterClient drops a client (remote host disconnected/evicted). */
export function unregisterClient(origin: Origin): void {
  clientByOrigin.delete(origin);
}

/** clientForOrigin returns the client for an origin, or undefined. */
export function clientForOrigin(origin: Origin): RouterClient | undefined {
  return clientByOrigin.get(origin);
}

/** clientForInstance resolves the owning client from a compound id. */
export function clientForInstance(id: string): RouterClient | undefined {
  return clientByOrigin.get(parseInstanceId(id).origin);
}

/** origins lists every connected origin (local first if present). */
export function origins(): Origin[] {
  return [...clientByOrigin.keys()];
}
