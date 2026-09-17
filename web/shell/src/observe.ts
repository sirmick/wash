// observe — the shell's side of the router's observe verb
// (docs/COMMANDER.md §4): one look at one instance, from what that
// instance's router already holds. A req_id-correlated promise over the
// ctrl channel, one per origin, like activity.ts.
//
// Wire (pkg/wire/observe.go):
//   shell → router: { t: "observe", req_id, instance_id, max_bytes? }
//   router → shell: { t: "observe.ok", req_id, observation } | { t: "observe.err", req_id, code, msg }

import { LOCAL_ORIGIN, type Origin } from './clients';

export interface ObservedWindow {
  app: string;
  instance_id: string;
  window_id?: number;
  title?: string;
  state?: string;
  focused?: boolean;
}

export interface Observation {
  /** which of the router's holdings answered */
  source: 'export' | 'pty-tail' | 'app-state' | 'none';
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

/** observe asks one origin's router for an observation of one of its instances. */
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
