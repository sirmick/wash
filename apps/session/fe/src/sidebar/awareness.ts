// Rail awareness maths (docs/SIDEBAR.md M1b).
//
// The rail's remaining job is to answer "what needs me, and where". Its
// input is the merged host-awareness map the shell exposes as
// window.wash.hostgwState(): origin → service → that service's latest
// snapshot, fed by a com.wash.hostgw on every attached router.
//
// Everything here is a pure function of that map, for two reasons. It is
// where the double-counting bug would live — the same local state now
// arrives twice (via hostgw AND via the legacy per-service kinds the
// widget bodies still read), so a badge must have exactly ONE source, and
// it is this one. And per-host decomposition is the same operation the
// host-grouped rail needs, so counting and grouping share a definition
// instead of drifting apart.
//
// Counts are recomputed from snapshots, never incremented from events
// (SIDEBAR.md §3.2(4)): a snapshot replaces its cell wholesale, so a
// count derived from it cannot go stale in a way a counter would.

/** The awareness map, as the shell hands it over. */
export type HostgwMap = ReadonlyMap<string, ReadonlyMap<string, unknown>>;

/** The seat's own router. Unprefixed on the wire; first in the rail. */
export const LOCAL_ORIGIN = 'local';

/** Service keys hostgw publishes under. */
export const SERVICE_NOTIFY = 'notify';
export const SERVICE_BULK = 'bulk';
export const SERVICE_PRIV = 'priv';
export const SERVICE_NET = 'net';
export const SERVICE_AGENT = 'agent';

/** One host's contribution to a badge. */
export interface HostCount {
  origin: string;
  count: number;
}

// ---- the service state shapes, narrowed to what awareness reads ----
//
// Deliberately partial: the rail needs counts, not the full records the
// widget bodies render. A service growing a field cannot break a badge.

interface NotifyStateView {
  notifications?: Array<{ read?: boolean }>;
}

interface BulkStateView {
  jobs?: Array<{ status?: string }>;
}

interface PrivStateView {
  queue?: Array<{ status?: string }>;
}

interface NetStateView {
  status?: string;
}

interface AgentStateView {
  rows?: Array<{ state?: string }>;
  asks?: unknown[];
}

/**
 * orderOrigins lists the hosts present in the map, LOCAL first and the
 * rest alphabetical. Local-first is the rail's reading order (§3.2(1));
 * stable ordering for the rest keeps a host from jumping around as
 * unrelated hosts come and go.
 */
export function orderOrigins(map: HostgwMap): string[] {
  const rest: string[] = [];
  let hasLocal = false;
  for (const origin of map.keys()) {
    if (origin === LOCAL_ORIGIN) hasLocal = true;
    else rest.push(origin);
  }
  rest.sort();
  return hasLocal ? [LOCAL_ORIGIN, ...rest] : rest;
}

/** stateFor reads one (origin, service) cell, or undefined. */
export function stateFor(map: HostgwMap, origin: string, service: string): unknown {
  return map.get(origin)?.get(service);
}

/**
 * countByHost applies a per-host counter to every host that has state for
 * a service, dropping the zeroes. What remains is exactly the hosts worth
 * mentioning — which is both the badge's input and the host-grouped
 * rail's row list.
 */
export function countByHost(
  map: HostgwMap,
  service: string,
  count: (state: unknown) => number,
): HostCount[] {
  const out: HostCount[] = [];
  for (const origin of orderOrigins(map)) {
    const state = stateFor(map, origin, service);
    if (state === undefined) continue;
    const n = count(state);
    if (n > 0) out.push({ origin, count: n });
  }
  return out;
}

/** totalCount sums countByHost — the merged number a badge shows. */
export function totalCount(
  map: HostgwMap,
  service: string,
  count: (state: unknown) => number,
): number {
  return countByHost(map, service, count).reduce((sum, h) => sum + h.count, 0);
}

/** badgeText renders a count as badge text; empty hides the badge. */
export function badgeText(n: number): string {
  return n > 0 ? String(n) : '';
}

// ---- the per-service counters ----

/** Unread notifications. Read ones have been acknowledged. */
export function unreadNotifications(state: unknown): number {
  const s = state as NotifyStateView | null;
  return (s?.notifications ?? []).filter((n) => !n.read).length;
}

/**
 * In-flight bulk jobs. Terminal rows (done/failed/cancelled) are
 * informational and auto-evicting, so they don't earn a badge.
 */
export function activeBulkJobs(state: unknown): number {
  const s = state as BulkStateView | null;
  return (s?.jobs ?? []).filter((j) => j.status === 'queued' || j.status === 'running').length;
}

/**
 * Escalations still waiting on a human. Approved-and-done or already
 * rejected requests are settled and demand nothing.
 */
export function pendingPrivReqs(state: unknown): number {
  const s = state as PrivStateView | null;
  return (s?.queue ?? []).filter((r) => r.status === 'queued').length;
}

/**
 * Agents blocked on a human: rows in needs-input, plus pending permission
 * questions. A working agent is visible inside the section, not on its
 * header — the badge is for "someone is waiting on you".
 */
export function waitingAgents(state: unknown): number {
  const s = state as AgentStateView | null;
  const rows = (s?.rows ?? []).filter((r) => r.state === 'needs-input').length;
  return rows + (s?.asks ?? []).length;
}

/**
 * netBadgeForHost is the odd one out: net's badge is a verb, not a count.
 * "!" while a change awaits confirmation (the lock-out window the user
 * MUST see), the status word on a terminal outcome, empty when settled.
 */
export function netBadgeForHost(state: unknown): string {
  const s = state as NetStateView | null;
  if (s?.status === 'await-confirm') return '!';
  if (s?.status === 'failed' || s?.status === 'reverted') return s.status;
  return '';
}

/**
 * netBadge merges the per-host verbs: an await-confirm anywhere outranks
 * everything (someone is about to lock themselves out of a machine), then
 * the first terminal outcome in reading order. Merging by severity rather
 * than by concatenation keeps the badge one glyph wide.
 */
export function netBadge(map: HostgwMap): string {
  let fallback = '';
  for (const origin of orderOrigins(map)) {
    const badge = netBadgeForHost(stateFor(map, origin, SERVICE_NET));
    if (badge === '!') return '!';
    if (badge !== '' && fallback === '') fallback = badge;
  }
  return fallback;
}

/**
 * netHostsNeedingAttention lists the hosts whose net state says something,
 * for the host-grouped rail. Separate from netBadge because the badge
 * collapses and the group does not.
 */
export function netHostsNeedingAttention(map: HostgwMap): Array<{ origin: string; badge: string }> {
  const out: Array<{ origin: string; badge: string }> = [];
  for (const origin of orderOrigins(map)) {
    const badge = netBadgeForHost(stateFor(map, origin, SERVICE_NET));
    if (badge !== '') out.push({ origin, badge });
  }
  return out;
}
