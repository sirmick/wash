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

// ---- per-host summaries (the host-grouped rail, M1c) ----
//
// A badge says "something", a summary says "what". §3 of the plan sets the
// bar with its own example — "2 agents on build01, 1 waiting on you" — so
// these render the smallest sentence that answers it. Empty string means
// "this host has nothing to say", which is what keeps a quiet host out of
// the rail entirely.
//
// Awareness only, by construction: a summary is text. Acting on it means
// opening the owning app on that host, which is what M2–M5 move in-app.

/** plural renders "1 job" / "2 jobs" without a library. */
export function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`;
}

export function notifySummary(state: unknown): string {
  const n = unreadNotifications(state);
  return n > 0 ? `${n} unread` : '';
}

export function bulkSummary(state: unknown): string {
  const n = activeBulkJobs(state);
  return n > 0 ? `${plural(n, 'job')} in flight` : '';
}

export function privSummary(state: unknown): string {
  const n = pendingPrivReqs(state);
  return n > 0 ? `${plural(n, 'request')} awaiting approval` : '';
}

/**
 * agentSummary splits waiting from working, because they mean opposite
 * things: one is a claim on your attention, the other is reassurance.
 */
export function agentSummary(state: unknown): string {
  const s = state as AgentStateView | null;
  const rows = s?.rows ?? [];
  const waiting = rows.filter((r) => r.state === 'needs-input').length + (s?.asks ?? []).length;
  const working = rows.filter((r) => r.state !== 'needs-input').length;
  const parts: string[] = [];
  if (waiting > 0) parts.push(`${waiting} waiting on you`);
  if (working > 0) parts.push(`${plural(working, 'agent')} working`);
  return parts.join(' · ');
}

/**
 * netSummary reports a host's network state whenever it is not simply
 * fine — including the terminal outcomes, which the collapsed badge
 * folds away.
 */
export function netSummary(state: unknown): string {
  const s = state as NetStateView | null;
  if (!s?.status) return '';
  if (s.status === 'await-confirm') return 'awaiting confirmation';
  if (s.status === 'failed' || s.status === 'reverted') return s.status;
  return '';
}

/** How a host's link is doing, as the Hosts widget reports it. */
export type HostLinkStatus = 'up' | 'reconnecting' | 'down';

/** One remote host's row inside a section. */
export interface HostGroupRow {
  origin: string;
  /** Badge text; '' hides it. */
  badge: string;
  /** One-line answer to "what about this host?"; '' when nothing. */
  summary: string;
  status: HostLinkStatus;
}

/**
 * remoteHostRows builds the per-host rows for one section: every REMOTE
 * host with something to say, in rail order.
 *
 * LOCAL is excluded — the seat's own state is the section's main body, and
 * repeating it as a group would say "this machine" twice.
 *
 * A host reported `down` is dropped rather than greyed: its counts are no
 * longer about anything (§3.2(4)). `reconnecting` is kept, and the caller
 * greys it — a greyed stale count is honest, a confident one is a lie.
 * (The shell also drops a detached origin from the map outright; this
 * covers the window where the supervisor has reported `down` but the
 * connection has not been torn down yet.)
 */
export function remoteHostRows(
  map: HostgwMap,
  service: string,
  badgeOf: (state: unknown) => string,
  summaryOf: (state: unknown) => string,
  statusOf: (origin: string) => HostLinkStatus,
): HostGroupRow[] {
  const out: HostGroupRow[] = [];
  for (const origin of orderOrigins(map)) {
    if (origin === LOCAL_ORIGIN) continue;
    const status = statusOf(origin);
    if (status === 'down') continue;
    const state = stateFor(map, origin, service);
    if (state === undefined) continue;
    const badge = badgeOf(state);
    const summary = summaryOf(state);
    if (badge === '' && summary === '') continue;
    out.push({ origin, badge, summary, status });
  }
  return out;
}

/** countBadge adapts a counter into a badgeOf for remoteHostRows. */
export function countBadge(count: (state: unknown) => number): (state: unknown) => string {
  return (state) => badgeText(count(state));
}

// ---- service → section, and the counters the rail watches ----

/**
 * netUrgency turns net's verb into a number so it can ride the same
 * rise-detection as the counts: 1 while a change awaits confirmation (the
 * lock-out window — the one net state that must interrupt), 0 otherwise.
 * A failure or a revert is already over; it earns a badge, not a pop-open.
 */
export function netUrgency(state: unknown): number {
  return (state as NetStateView | null)?.status === 'await-confirm' ? 1 : 0;
}

/**
 * sectionForService maps a hostgw service key to its rail section id. They
 * mostly match, and "agent" → "agents" is exactly why this is a function
 * and not string interpolation at the call site.
 */
export function sectionForService(service: string): string {
  return service === SERVICE_AGENT ? 'agents' : service;
}

/**
 * AWARENESS_COUNTERS is every (service, counter) pair the rail watches for
 * a rise. One list, so a new service is one entry rather than a new branch
 * in the auto-expand logic.
 */
export const AWARENESS_COUNTERS: ReadonlyArray<readonly [string, (state: unknown) => number]> = [
  [SERVICE_NOTIFY, unreadNotifications],
  [SERVICE_BULK, activeBulkJobs],
  [SERVICE_PRIV, pendingPrivReqs],
  [SERVICE_AGENT, waitingAgents],
  [SERVICE_NET, netUrgency],
];
