// dbg.ts — WebSocket-based bridge to the wash demo server.
//
// One persistent WS to ws://<host>/ws. Browser declares role=browser
// in the hello frame; subsequent log/stage frames stream out. The
// server fans them to stdout, a log file, and any admin clients
// (e.g. `pnpm cli tail`).
//
// Public API is intentionally unchanged from the prior HTTP version
// (log, pushBytes, flush, installErrorCapture) — bridges and main.ts
// keep working without edits.
//
// Resilience: if the WS isn't open yet, frames queue in memory and
// flush on connect. If the WS closes (server restart), reconnect with
// exponential backoff and resume.

const WS_URL = `${window.location.protocol === 'https:' ? 'wss' : 'ws'}://${window.location.host}/ws`;

const dec = new TextDecoder('utf-8', { fatal: false });
const buffers = new Map<string, string>();

// Pending frames buffer — anything posted before the WS is open or
// during a reconnect lives here. Bounded so a stalled server can't
// blow up FE memory; oldest frames drop first.
const QUEUE_MAX = 4096;
const queue: string[] = [];

let ws: WebSocket | null = null;
let connected = false;
let connectAttempt = 0;
const PAGE_HINT = window.location.pathname.replace(/^\//, '') || 'index';

function send(frameJson: string): void {
  if (connected && ws && ws.readyState === WebSocket.OPEN) {
    ws.send(frameJson);
    return;
  }
  // Not connected — queue and drop oldest if over cap.
  queue.push(frameJson);
  while (queue.length > QUEUE_MAX) queue.shift();
}

function flushQueue(): void {
  while (queue.length > 0 && ws && ws.readyState === WebSocket.OPEN) {
    const frame = queue.shift()!;
    ws.send(frame);
  }
}

function connect(): void {
  try {
    ws = new WebSocket(WS_URL);
  } catch {
    scheduleReconnect();
    return;
  }
  ws.onopen = () => {
    connected = true;
    connectAttempt = 0;
    ws!.send(JSON.stringify({ t: 'hello', role: 'browser', page: PAGE_HINT, ua: navigator.userAgent }));
    flushQueue();
  };
  ws.onclose = () => {
    connected = false;
    scheduleReconnect();
  };
  ws.onerror = () => { /* close will follow */ };
  ws.onmessage = (e) => {
    let msg: any;
    try { msg = JSON.parse(e.data); } catch { return; }
    dispatchIncoming(msg);
  };
}

function scheduleReconnect(): void {
  connectAttempt++;
  // 250ms → 500ms → 1s → 2s → … capped at 5s.
  const delay = Math.min(250 * 2 ** Math.min(connectAttempt, 5), 5000);
  setTimeout(connect, delay);
}

// Incoming admin-side frames (ctl/input/event). The bridge subscribes
// to specific kinds via onMessage(); we keep a tiny pub-sub here so
// dbg.ts stays the only WS owner.
type IncomingHandler = (msg: any) => void;
const handlers = new Set<IncomingHandler>();

function dispatchIncoming(msg: any): void {
  for (const h of handlers) {
    try { h(msg); } catch { /* never let a handler kill the bus */ }
  }
}

/** Subscribe to server→browser frames (ctl, input, event, welcome).
 *  Returns an unsubscribe function. */
export function onMessage(h: IncomingHandler): () => void {
  handlers.add(h);
  return () => handlers.delete(h);
}

connect();

// ─────────────────────────── public API ────────────────────────────────

/** Push raw bytes (e.g. serial0 output). Splits on newlines, ships
 *  each completed line, buffers the last partial line until more
 *  bytes or a flush. */
export function pushBytes(category: string, bytes: Uint8Array): void {
  const text = dec.decode(bytes, { stream: true });
  if (text.length === 0) return;
  const carried = buffers.get(category) ?? '';
  const combined = carried + text;
  const parts = combined.split('\n');
  buffers.set(category, parts.pop() ?? '');
  for (const line of parts) {
    if (line === '') continue;
    send(JSON.stringify({ t: 'log', source: category, line, ts: Date.now() }));
  }
}

/** Force-flush a category's buffer. */
export function flush(category: string): void {
  const carried = buffers.get(category);
  if (!carried) return;
  buffers.set(category, '');
  send(JSON.stringify({ t: 'log', source: category, line: carried, ts: Date.now() }));
}

/** One-shot line: ship immediately, no buffering. */
export function log(category: string, line: string): void {
  send(JSON.stringify({ t: 'log', source: category, line, ts: Date.now() }));
}

/** Stage transition. State drives the title-bar dot color server-side
 *  (stdout pretty-print) and client-side (when tinyemu-bridge wires it). */
export function stage(label: string, state?: 'boot' | 'ready' | 'wash' | 'error'): void {
  send(JSON.stringify({ t: 'stage', label, state, ts: Date.now() }));
}

/** Install browser-level error capture so unhandled exceptions and
 *  promise rejections end up in the same WS stream. */
export function installErrorCapture(): void {
  window.addEventListener('error', (ev: ErrorEvent) => {
    const stack = ev.error?.stack ? ` :: ${String(ev.error.stack)}` : '';
    const where = ev.filename ? `${ev.filename}:${ev.lineno}:${ev.colno}` : '';
    log('error', `${ev.message}${where ? ' @ ' + where : ''}${stack}`);
  });
  window.addEventListener('unhandledrejection', (ev: PromiseRejectionEvent) => {
    const r: unknown = ev.reason;
    let msg = '';
    if (r instanceof Error) msg = `${r.message}${r.stack ? ' :: ' + r.stack : ''}`;
    else if (typeof r === 'string') msg = r;
    else msg = JSON.stringify(r);
    log('unhandled', msg);
  });
  for (const level of ['error', 'warn'] as const) {
    const orig = console[level].bind(console);
    console[level] = (...args: unknown[]) => {
      orig(...args);
      const line = args
        .map((a) => {
          if (a instanceof Error) return a.stack || a.message;
          if (typeof a === 'string') return a;
          try { return JSON.stringify(a); } catch { return String(a); }
        })
        .join(' ');
      log(`console.${level}`, line);
    };
  }
}
