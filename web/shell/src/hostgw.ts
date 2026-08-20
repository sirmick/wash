// Host-awareness state, merged across origins (docs/SIDEBAR.md M1).
//
// com.wash.hostgw runs on every router, subscribes to its own host's
// background services, and republishes each state push to its own FE.
// The router fans an app's FE-bound message to every attached shell — and
// A's shell is an attached shell on B — so B's awareness state arrives
// here over the connection that already exists.
//
// This module is the shell-side landing spot. hostgw is a background app
// with no element, so its traffic cannot go through deliverToInstance
// (which would park it in pendingMessages forever); main.tsx intercepts
// it in deliverAppMsg and hands it here, tagged with the delivering
// client's origin.
//
// The merged shape is (origin → service → state). Every message REPLACES
// one cell wholesale: hostgw always pushes whole snapshots, so there is
// nothing to merge and no counter to increment (SIDEBAR.md §3.2(4) — a
// confident stale count is a lie, so the rail recomputes from snapshots
// and re-reads everything on every (re)subscribe).

import { Sub } from './api.ts';
import { type Origin } from './clients.ts';

/** Reserved app id of the awareness gateway. */
export const HOSTGW_APP_ID = 'com.wash.hostgw';

/** Envelope kind hostgw republishes under. */
export const HOSTGW_STATE_KIND = 'hostgw.state';

/**
 * One host's awareness state: service name → that service's latest
 * snapshot. The bodies are opaque here — the rail's widgets know their
 * own shapes, the shell only routes.
 */
export type HostServices = ReadonlyMap<string, unknown>;

/** The merged map the session chrome reads: origin → service → state. */
export type HostgwMap = ReadonlyMap<Origin, HostServices>;

const EMPTY: HostgwMap = new Map();

// The live map, republished as a NEW outer Map on every change so
// subscribers (and Solid signals downstream) see a fresh identity and
// re-render. Cheap: a handful of origins, at most six services each.
const hostgwSub = new Sub<HostgwMap>(EMPTY);

/** hostgwState returns the current merged map. */
export function hostgwState(): HostgwMap {
  return hostgwSub.value;
}

/** onHostgwState subscribes; fires immediately with the current map. */
export function onHostgwState(cb: (m: HostgwMap) => void): () => void {
  return hostgwSub.on(cb);
}

/**
 * ingestHostgwMsg folds one republished frame into the map. Returns true
 * if the payload was a hostgw.state envelope (so the caller knows the
 * message was consumed and must not be routed anywhere else).
 *
 * Shape-checked rather than trusted: main.tsx routes here off the
 * instance→app-id map, so we already know the SENDER is hostgw, but a
 * malformed frame from it still must not put an undefined service in the
 * map and break every reader downstream.
 */
export function ingestHostgwMsg(origin: Origin, data: unknown): boolean {
  const msg = data as { kind?: string; service?: string; state?: unknown } | null;
  if (!msg || msg.kind !== HOSTGW_STATE_KIND) return false;
  const service = msg.service;
  if (typeof service !== 'string' || service === '') return false;

  const next = new Map(hostgwSub.value);
  const services = new Map(next.get(origin) ?? []);
  services.set(service, msg.state);
  next.set(origin, services);
  hostgwSub.set(next);
  return true;
}

/**
 * dropHostgwOrigin forgets a host's whole cell. Called when an origin
 * detaches: its counts are no longer about anything, and keeping them
 * would show a live badge for a host that is gone. `reconnecting` is a
 * different case and deliberately NOT handled here — a blip keeps the
 * data and greys it in the presentation layer (SIDEBAR.md §3.2(4)).
 */
export function dropHostgwOrigin(origin: Origin): void {
  if (!hostgwSub.value.has(origin)) return;
  const next = new Map(hostgwSub.value);
  next.delete(origin);
  hostgwSub.set(next);
}

/** Test seam: forget everything (module state outlives one test file). */
export function resetHostgw(): void {
  hostgwSub.set(EMPTY);
}
