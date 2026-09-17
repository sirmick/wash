// observe — the shell's side of the router's observe verb
// (docs/COMMANDER.md §4): one look at one instance. The router answers
// from what it holds (an app export, a pty tail, the state blob); when it
// holds nothing for an app that is eligible, the shell keeps looking —
// the app's FE Content-API provider, then the window's rendered text —
// so `auto` always yields something. A req_id-correlated promise over the
// ctrl channel, one per origin, like activity.ts.
//
// Wire (pkg/wire/observe.go):
//   shell → router: { t: "observe", req_id, instance_id, max_bytes? }
//   router → shell: { t: "observe.ok", req_id, observation } | { t: "observe.err", req_id, code, msg }

import { LOCAL_ORIGIN, type Origin } from './clients.ts';

export interface ObservedWindow {
  app: string;
  instance_id: string;
  window_id?: number;
  title?: string;
  state?: string;
  focused?: boolean;
}

export type ObservationSource = 'export' | 'pty-tail' | 'app-state' | 'provider' | 'dom' | 'none';

export interface Observation {
  /** which holding answered: the router's (export, pty-tail, app-state) or the shell's (provider, dom) */
  source: ObservationSource;
  /** the app may be observed; with source none the shell fell through and found nothing either */
  eligible?: boolean;
  /** moves whenever content would; opaque */
  revision?: string;
  content_type?: string;
  content?: string;
  truncated?: boolean;
  captured_at: number;
  window?: ObservedWindow;
  /** the origin the observation came from ('local' for the seat's own host) */
  host: string;
}

/** DEFAULT_MAX_BYTES mirrors the router's default tail (internal/observe). */
export const DEFAULT_MAX_BYTES = 16 * 1024;
export const MAX_BYTES = 128 * 1024;

type SendCtrl = (msg: unknown) => void;

interface Pending {
  resolve: (v: Observation) => void;
  reject: (err: Error) => void;
  origin: Origin;
}

const pending = new Map<number, Pending>();
let nextReqID = 1;

function hostOf(origin: Origin): string {
  return origin === LOCAL_ORIGIN ? 'local' : origin;
}

/** observe asks one origin's router for an observation of one of its
 *  instances (the bare id that router minted). */
export function observe(send: SendCtrl, origin: Origin, instanceID: string, maxBytes?: number): Promise<Observation> {
  const reqID = nextReqID++;
  return new Promise<Observation>((resolve, reject) => {
    pending.set(reqID, { resolve, reject, origin });
    send({ t: 'observe', req_id: reqID, instance_id: instanceID, ...(maxBytes ? { max_bytes: maxBytes } : {}) });
  });
}

// ---- replies, called from main.tsx's ctrl dispatcher ----

export function handleObserveOK(origin: Origin, msg: { req_id: number; observation: Omit<Observation, 'host'> }): void {
  const p = pending.get(msg.req_id);
  if (!p) return;
  pending.delete(msg.req_id);
  p.resolve({ ...msg.observation, host: hostOf(origin) });
}

export function handleObserveErr(msg: { req_id: number; code: string; msg?: string }): void {
  const p = pending.get(msg.req_id);
  if (!p) return;
  pending.delete(msg.req_id);
  p.reject(new Error(`${msg.code}${msg.msg ? ': ' + msg.msg : ''}`));
}

/** rejectPendingFor fails every observation waiting on an origin whose
 *  connection went away. */
export function rejectPendingFor(origin: Origin): void {
  for (const [id, p] of pending) {
    if (p.origin === origin) {
      pending.delete(id);
      p.reject(new Error('disconnected'));
    }
  }
}

// ---- the shell's fallbacks ----

/** What the shell can read of a mounted window, injected so the rule is
 *  testable without a DOM: the app's Content-API provider (or the saved
 *  state the shell mirrors), and the window's rendered text. */
export interface Holdings {
  provider(): { source: 'app' | 'backing-store' | 'none'; content?: unknown; error?: string };
  text(): string | undefined;
}

/** fallback completes a router observation that came back `none` for an
 *  eligible app: the provider's value as JSON, else the window's text,
 *  else the observation as it was. Content is bounded and redacted. */
export function fallback(o: Observation, h: Holdings, maxBytes?: number): Observation {
  if (o.source !== 'none' || !o.eligible) return o;
  const max = !maxBytes || maxBytes <= 0 || maxBytes > MAX_BYTES ? DEFAULT_MAX_BYTES : maxBytes;
  const captured_at = Date.now();
  let p: ReturnType<Holdings['provider']>;
  try { p = h.provider(); } catch { p = { source: 'none' }; }
  if (p.content !== undefined) {
    const text = encodeContent(p.content);
    const [content, truncated] = cut(redact(text), max);
    return { ...o, source: p.source === 'app' ? 'provider' : 'app-state', content_type: 'application/json', content, truncated, captured_at, revision: `fe:${hash(text)}` };
  }
  let t: string | undefined;
  try { t = h.text(); } catch { t = undefined; }
  const text = normaliseText(t ?? '');
  if (text === '') return o;
  const [content, truncated] = cut(redact(text), max);
  return { ...o, source: 'dom', content_type: 'text/plain', content, truncated, captured_at, revision: `dom:${hash(text)}` };
}

function encodeContent(value: unknown): string {
  const seen = new WeakSet<object>();
  try {
    return JSON.stringify(value, (_k, item) => {
      if (typeof item === 'object' && item !== null) {
        if (seen.has(item)) return '[Circular]';
        seen.add(item);
      }
      return item;
    }) ?? '';
  } catch (err) {
    return `[content could not be encoded: ${err instanceof Error ? err.message : String(err)}]`;
  }
}

/** normaliseText makes rendered text readable as lines: runs of blanks
 *  within a line collapse, blank runs collapse, edges trim. */
export function normaliseText(s: string): string {
  const lines = s.replace(/\r/g, '').split('\n').map((l) => l.replace(/[ \t ]+/g, ' ').trim());
  const out: string[] = [];
  let blank = 0;
  for (const l of lines) {
    if (l === '') { if (++blank > 1) continue; } else blank = 0;
    out.push(l);
  }
  while (out.length && out[0] === '') out.shift();
  while (out.length && out[out.length - 1] === '') out.pop();
  return out.join('\n');
}

/** cut keeps the first max UTF-8 bytes of s on a character boundary. */
function cut(s: string, max: number): [string, boolean] {
  const enc = new TextEncoder();
  if (enc.encode(s).length <= max) return [s, false];
  let lo = 0, hi = s.length;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (enc.encode(s.slice(0, mid)).length <= max) lo = mid; else hi = mid - 1;
  }
  return [s.slice(0, lo), true];
}

// The same credential shapes the router scrubs (internal/observe.Redact):
// a labelled value (api_key= / token: / password: / Authorization: Bearer …,
// quoted or not), vendor key prefixes, AWS access keys.
const labelled = /((?:api[_-]?key|token|secret|password|passwd|authorization)["']?\s*[:=]\s*["']?(?:bearer\s+)?|bearer\s+)([^\s"']+)/gi;
const bare = /\b(?:sk|sk-ant|sk-proj|ghp|gho|xox[abp])[-_][A-Za-z0-9_-]{8,}|\bAKIA[0-9A-Z]{16}\b/g;

export function redact(s: string): string {
  return s.replace(labelled, '$1[redacted]').replace(bare, '[redacted]');
}

/** hash is a small FNV-1a over the text: a revision that moves with it. */
function hash(s: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h.toString(16);
}
