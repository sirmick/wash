// activity — the shell's side of the router's activity journal
// (docs/COMMANDER.md §3). Query, stats and clear are req_id-correlated
// promises over the ctrl channel, one per origin; the tail is a push the
// router sends while it is on, fanned out here to subscribers.
//
// Every host journals itself, so an entry's `host` is stamped HERE from the
// origin it arrived on: a remote router calls itself "local" too.
//
// Wire (pkg/wire/activity.go):
//   shell → router: { t: "activity.query", req_id, from?, to?, kinds?, apps?, text?, limit?, cursor? }
//   router → shell: { t: "activity.query.ok", req_id, entries, cursor? } | { t: "activity.query.err", req_id, code, msg }
//   shell → router: { t: "activity.tail", on }      router → shell: { t: "activity.entry", entry }
//   shell → router: { t: "activity.stats", req_id } router → shell: { t: "activity.stats.ok", req_id, stats }
//   shell → router: { t: "activity.clear", req_id } router → shell: { t: "activity.clear.ok", req_id }

import { LOCAL_ORIGIN, type Origin } from './clients';

export interface ActivityIntent {
  kind: 'focus' | 'resume' | 'open' | string;
  origin?: string;
  app_id?: string;
  instance_id?: string;
  window_id?: number;
  session_id?: string;
  row_key?: string;
  path?: string;
}

export interface ActivityEntry {
  ts: number;
  seq: number;
  /** the origin this entry came from ('local' for the seat's own host) */
  host: string;
  kind: string;
  app?: string;
  instance?: string;
  window?: number;
  title?: string;
  line: string;
  truncated?: boolean;
  ref?: Record<string, unknown>;
  intent?: ActivityIntent;
}

export interface ActivityStats {
  enabled: boolean;
  days: number;
  bytes: number;
  dropped: number;
  today: Record<string, number>;
  seq: number;
  path?: string;
}

export interface ActivityQuery {
  from?: number;
  to?: number;
  kinds?: string[];
  apps?: string[];
  text?: string;
  limit?: number;
  cursor?: string;
}

export interface ActivityPage {
  host: string;
  entries: ActivityEntry[];
  cursor?: string;
}

type SendCtrl = (msg: unknown) => void;

interface Pending {
  resolve: (v: unknown) => void;
  reject: (err: Error) => void;
  origin: Origin;
}

const pending = new Map<number, Pending>();
let nextReqID = 1;

function hostOf(origin: Origin): string {
  return origin === LOCAL_ORIGIN ? 'local' : origin;
}

function stamp(origin: Origin, e: ActivityEntry): ActivityEntry {
  return { ...e, host: hostOf(origin) };
}

function request<T>(send: SendCtrl, origin: Origin, msg: Record<string, unknown>): Promise<T> {
  const reqID = nextReqID++;
  return new Promise<T>((resolve, reject) => {
    pending.set(reqID, { resolve: resolve as (v: unknown) => void, reject, origin });
    send({ ...msg, req_id: reqID });
  });
}

/** activityQuery asks one origin's router for a page of its journal. */
export function activityQuery(send: SendCtrl, origin: Origin, q: ActivityQuery): Promise<ActivityPage> {
  return request<ActivityPage>(send, origin, { t: 'activity.query', ...q });
}

export function activityStats(send: SendCtrl, origin: Origin): Promise<ActivityStats> {
  return request<ActivityStats>(send, origin, { t: 'activity.stats' });
}

export function activityClear(send: SendCtrl, origin: Origin): Promise<void> {
  return request<void>(send, origin, { t: 'activity.clear' });
}

// ---- replies, called from main.tsx's ctrl dispatcher ----

export function handleActivityQueryOK(origin: Origin, msg: { req_id: number; entries: ActivityEntry[]; cursor?: string }): void {
  const p = pending.get(msg.req_id);
  if (!p) return;
  pending.delete(msg.req_id);
  p.resolve({ host: hostOf(origin), entries: (msg.entries ?? []).map((e) => stamp(origin, e)), cursor: msg.cursor } satisfies ActivityPage);
}

export function handleActivityQueryErr(msg: { req_id: number; code: string; msg?: string }): void {
  const p = pending.get(msg.req_id);
  if (!p) return;
  pending.delete(msg.req_id);
  p.reject(new Error(`${msg.code}${msg.msg ? ': ' + msg.msg : ''}`));
}

export function handleActivityStatsOK(origin: Origin, msg: { req_id: number; stats: ActivityStats }): void {
  const p = pending.get(msg.req_id);
  if (!p) return;
  pending.delete(msg.req_id);
  p.resolve({ ...msg.stats, path: msg.stats.path });
  void origin;
}

export function handleActivityClearOK(msg: { req_id: number }): void {
  const p = pending.get(msg.req_id);
  if (!p) return;
  pending.delete(msg.req_id);
  p.resolve(undefined);
}

/** rejectPendingFor fails every request waiting on an origin whose
 *  connection went away, so a Timeline does not wait for ever. */
export function rejectPendingFor(origin: Origin): void {
  for (const [id, p] of pending) {
    if (p.origin === origin) {
      pending.delete(id);
      p.reject(new Error('disconnected'));
    }
  }
}

// ---- tail ----

const subscribers = new Set<(e: ActivityEntry) => void>();

/** onActivity subscribes to live entries from every origin. The caller
 *  (main.tsx) turns the routers' tails on while anyone is subscribed. */
export function onActivity(cb: (e: ActivityEntry) => void): () => void {
  subscribers.add(cb);
  return () => { subscribers.delete(cb); };
}

export function tailWanted(): boolean {
  return subscribers.size > 0;
}

export function handleActivityEntry(origin: Origin, msg: { entry: ActivityEntry }): void {
  const e = stamp(origin, msg.entry);
  for (const cb of subscribers) cb(e);
}
