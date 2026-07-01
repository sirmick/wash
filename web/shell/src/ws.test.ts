// Tests for Conn's /auth/check reconnect preflight — the logic that
// tells "auth gone, stop looping" apart from "transient drop, keep
// reconnecting". We exercise authGone() directly with an injected
// fetch, so no real WebSocket / network is involved.
//
// Run with: cd web/shell && npx tsx --test src/ws.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { Conn, type ConnEvent } from './ws.ts';
import type { SocketLike } from './virtio.ts';
import {
  CLASS_CONTROL,
  decodeCtrl,
  decodeFrame,
  encodeCtrl,
  encodeFrame,
  flagsWithClass,
  FLAG_END,
} from './wire.ts';

// Minimal shape of node:test's context we use (its types aren't resolvable
// under the bundler tsconfig; the runner supplies the real object).
type TestCtx = { after(fn: () => void): void };

// stubSocket is an inert SocketLike so the Conn constructor's connect()
// call doesn't touch a real WebSocket. It never fires lifecycle events.
function stubSocket(): SocketLike {
  return {
    binaryType: 'arraybuffer',
    bufferedAmount: 0,
    onopen: null,
    onerror: null,
    onmessage: null,
    onclose: null,
    send() {},
    close() {},
  } as unknown as SocketLike;
}

// fakeResp builds just enough of a Response for authGone().
function fakeResp(init: { status: number; type?: string; body?: unknown }): Response {
  return {
    status: init.status,
    type: init.type ?? 'basic',
    json: async () => init.body,
  } as unknown as Response;
}

// newConn builds a Conn over the stub factory with an injected fetch
// and returns it; authGone is private, so callers cast to any.
function newConn(fetchImpl: typeof fetch): Conn {
  return new Conn(() => stubSocket(), () => {}, () => {}, { fetchImpl });
}

test('authGone: 401 with login_url → unauthenticated + redirect target', async () => {
  const conn = newConn(async () => fakeResp({ status: 401, body: { authenticated: false, login_url: '/login' } }));
  const gone = await (conn as any).authGone();
  assert.equal(gone, true);
  assert.equal(conn.loginRedirect(), '/login');
});

test('authGone: 401 with null login_url → unauthenticated, no redirect', async () => {
  const conn = newConn(async () => fakeResp({ status: 401, body: { authenticated: false, login_url: null } }));
  const gone = await (conn as any).authGone();
  assert.equal(gone, true);
  assert.equal(conn.loginRedirect(), null);
});

test('authGone: 204 → still authed, keep reconnecting', async () => {
  const conn = newConn(async () => fakeResp({ status: 204 }));
  assert.equal(await (conn as any).authGone(), false);
});

test('authGone: opaque redirect → unauthenticated, default /login', async () => {
  const conn = newConn(async () => fakeResp({ status: 0, type: 'opaqueredirect' }));
  const gone = await (conn as any).authGone();
  assert.equal(gone, true);
  assert.equal(conn.loginRedirect(), '/login');
});

test('authGone: network error → not declared dead (keep retrying)', async () => {
  const conn = newConn(async () => { throw new Error('connection refused'); });
  assert.equal(await (conn as any).authGone(), false);
});

// ---- offline send queue ----
//
// Frames produced while the socket is down must not vanish: they queue
// and flush FIFO when the replacement socket opens (the reconnect
// window previously dropped every keystroke / save_state silently).

// recordingSocket captures sends and exposes the lifecycle hooks so a
// test can drive open/close transitions by hand.
function recordingSocket(sent: Uint8Array[]): SocketLike {
  return {
    binaryType: 'arraybuffer',
    bufferedAmount: 0,
    onopen: null,
    onerror: null,
    onmessage: null,
    onclose: null,
    send(data: ArrayBuffer | Uint8Array) {
      sent.push(data instanceof Uint8Array ? data : new Uint8Array(data));
    },
    close() {},
  } as unknown as SocketLike;
}

test('sendCtrl before open queues; flushes FIFO on open', () => {
  const sent: Uint8Array[] = [];
  const socks: SocketLike[] = [];
  const conn = new Conn(
    () => {
      const s = recordingSocket(sent);
      socks.push(s);
      return s;
    },
    () => {},
    () => {},
  );
  // Socket created but onopen not yet fired → state 'connecting'.
  conn.sendCtrl({ t: 'first' });
  conn.sendCtrl({ t: 'second' });
  assert.equal(sent.length, 0);
  assert.equal(conn.pendingCount(), 2);

  socks[0].onopen!(new Event('open'));
  assert.equal(conn.pendingCount(), 0);
  assert.equal(sent.length, 2);
  // FIFO: 'first' flushed before 'second'.
  const texts = sent.map((b) => new TextDecoder().decode(b));
  assert.ok(texts[0].includes('first'));
  assert.ok(texts[1].includes('second'));
});

test('sends during reconnect window queue and flush on reopen', async () => {
  const sent: Uint8Array[] = [];
  const socks: SocketLike[] = [];
  const conn = new Conn(
    () => {
      const s = recordingSocket(sent);
      socks.push(s);
      return s;
    },
    () => {},
    () => {},
  );
  socks[0].onopen!(new Event('open'));
  conn.sendRaw(7, new Uint8Array([1, 2, 3]));
  assert.equal(sent.length, 1);

  // Drop the socket: state → 'reconnecting'; sends queue.
  socks[0].onclose!(new Event('close') as CloseEvent);
  conn.sendRaw(7, new Uint8Array([4, 5, 6]));
  conn.sendCtrl({ t: 'queued' });
  assert.equal(sent.length, 1);
  assert.equal(conn.pendingCount(), 2);

  // Wait out the first 250ms backoff; the factory hands out socket #2.
  await new Promise((r) => setTimeout(r, 300));
  assert.equal(socks.length, 2);
  socks[1].onopen!(new Event('open'));
  assert.equal(conn.pendingCount(), 0);
  assert.equal(sent.length, 3);
});

test('queue overflow drops everything rather than a torn middle', () => {
  const sent: Uint8Array[] = [];
  const conn = new Conn(() => recordingSocket(sent), () => {}, () => {});
  // state 'connecting' — everything queues. Push past the cap.
  const chunk = new Uint8Array(256 * 1024);
  conn.sendRaw(1, chunk);
  conn.sendRaw(1, chunk);
  conn.sendRaw(1, chunk);
  assert.ok(conn.pendingCount() > 0);
  conn.sendRaw(1, chunk); // crosses 1MiB
  assert.equal(conn.pendingCount(), 0);
  assert.equal(sent.length, 0);
});

test('queue overflow emits a lost-input event', () => {
  const sent: Uint8Array[] = [];
  const events: ConnEvent[] = [];
  const conn = new Conn(() => recordingSocket(sent), () => {}, () => {});
  conn.onEvent((e) => events.push(e));
  const chunk = new Uint8Array(256 * 1024);
  for (let i = 0; i < 4; i++) conn.sendRaw(1, chunk); // crosses 1MiB on the 4th
  const lost = events.filter((e) => e.kind === 'lost-input');
  assert.equal(lost.length, 1);
  assert.match(lost[0].msg, /dropped 4 unsent frame/);
});

// ---- heartbeat + wake + diagnostics ----
//
// These drive the heartbeat with tiny timing so a unit test runs in
// milliseconds. The socket factory hands out recordingSockets the test
// drives by hand (onopen/onclose/onmessage), so no real WebSocket is used.

function lastCtrl(sent: Uint8Array[]): any {
  for (let i = sent.length - 1; i >= 0; i--) {
    const f = decodeFrame(sent[i]);
    if (f.channel === 0) return decodeCtrl(f.payload);
  }
  return null;
}

function pongBytes(seq: number): ArrayBuffer {
  const frame = encodeFrame({
    flags: flagsWithClass(FLAG_END, CLASS_CONTROL),
    channel: 0,
    payload: encodeCtrl({ t: 'pong', seq }),
  });
  return frame.buffer as ArrayBuffer;
}

// hbConn builds a heartbeat-enabled Conn over a driveable socket and returns
// the conn plus the socket list and the captured event/ctrl-handler streams.
// Registers conn.close() as test cleanup so the heartbeat interval + any
// armed reconnect timer don't keep the node:test process alive.
function hbConn(t: { after(fn: () => void): void }, opts?: { pongTimeoutMs?: number; heartbeatMs?: number; wakeProbeMs?: number }) {
  const sent: Uint8Array[] = [];
  const socks: SocketLike[] = [];
  const ctrl: any[] = [];
  const events: ConnEvent[] = [];
  const conn = new Conn(
    () => {
      const s = recordingSocket(sent);
      socks.push(s);
      return s;
    },
    (m) => ctrl.push(m),
    () => {},
    {
      heartbeat: true,
      heartbeatMs: opts?.heartbeatMs ?? 100_000, // periodic beat parked far away by default
      pongTimeoutMs: opts?.pongTimeoutMs ?? 10_000,
      wakeProbeMs: opts?.wakeProbeMs ?? 10_000,
    },
  );
  conn.onEvent((e) => events.push(e));
  t.after(() => conn.close());
  return { conn, sent, socks, ctrl, events };
}

test('heartbeat: open sends a ping; matching pong records RTT and clears it', (t: TestCtx) => {
  const { conn, sent, socks } = hbConn(t);
  socks[0].onopen!(new Event('open'));
  const ping = lastCtrl(sent);
  assert.equal(ping.t, 'ping');
  assert.equal(typeof ping.seq, 'number');
  assert.equal(conn.diag().rttMs, null); // no pong yet

  socks[0].onmessage!({ data: pongBytes(ping.seq) } as MessageEvent);
  const d = conn.diag();
  assert.ok(d.rttMs !== null && d.rttMs >= 0, 'rtt recorded after pong');
  assert.notEqual(d.sincePongMs, Infinity);
});

test('heartbeat: pong is consumed by Conn, never reaches the app handler', (t: TestCtx) => {
  const { socks, ctrl, sent } = hbConn(t);
  socks[0].onopen!(new Event('open'));
  const ping = lastCtrl(sent);
  socks[0].onmessage!({ data: pongBytes(ping.seq) } as MessageEvent);
  assert.equal(ctrl.length, 0, 'pong must not be delivered to the ctrl handler');
});

test('heartbeat: no pong → zombie event + force redial', async (t: TestCtx) => {
  const { socks, events } = hbConn(t, { pongTimeoutMs: 15 });
  socks[0].onopen!(new Event('open'));
  // Wait past the pong deadline without answering.
  await new Promise((r) => setTimeout(r, 330));
  assert.ok(events.some((e) => e.kind === 'zombie'), 'zombie event fired');
  // forceRedial dials a fresh socket without waiting for the dead one's close.
  assert.ok(socks.length >= 2, 'a replacement socket was dialed');
});

test('wake while open fires a fresh liveness probe', (t: TestCtx) => {
  const { conn, socks, sent, events } = hbConn(t);
  socks[0].onopen!(new Event('open'));
  const first = lastCtrl(sent).seq;
  conn.wake('test');
  const second = lastCtrl(sent);
  assert.equal(second.t, 'ping');
  assert.ok(second.seq > first, 'wake sent a new ping');
  assert.ok(events.some((e) => e.kind === 'wake'));
});

test('wake while reconnecting redials immediately (skips backoff)', async (t: TestCtx) => {
  const { conn, socks } = hbConn(t);
  socks[0].onopen!(new Event('open'));
  socks[0].onclose!(new Event('close') as CloseEvent); // → reconnecting, 250ms timer armed
  assert.equal(socks.length, 1);
  conn.wake('online');
  await Promise.resolve(); // reconnectTick is async (no auth probe on factory)
  await Promise.resolve();
  assert.equal(socks.length, 2, 'redialed without waiting out the backoff');
});

test('reconnectNow is a no-op while open', (t: TestCtx) => {
  const { conn, socks } = hbConn(t);
  socks[0].onopen!(new Event('open'));
  conn.reconnectNow();
  assert.equal(socks.length, 1);
});

test('diag reports total reconnects and last close code', (t: TestCtx) => {
  const { conn, socks } = hbConn(t);
  socks[0].onopen!(new Event('open'));
  const ev = { type: 'close', code: 1006, reason: 'abnormal' } as unknown as CloseEvent;
  socks[0].onclose!(ev);
  assert.equal(conn.diag().lastCloseCode, 1006);
  assert.equal(conn.diag().state, 'reconnecting');
});

// ---- concurrent-dial race (REVIEW-RECONNECT H2) ----

// countChannel returns how many sent frames rode a given channel.
function countChannel(sent: Uint8Array[], channel: number): number {
  let n = 0;
  for (const b of sent) {
    if (decodeFrame(b).channel === channel) n++;
  }
  return n;
}

test('wake/reconnectNow while a dial is in flight: one socket, queued frame flushes once', async (t: TestCtx) => {
  const { conn, socks, sent } = hbConn(t);
  socks[0].onopen!(new Event('open'));
  socks[0].onclose!(new Event('close') as CloseEvent); // → reconnecting, 250ms backoff
  // A keystroke typed during the outage queues.
  conn.sendRaw(7, new Uint8Array([1, 2, 3]));
  assert.equal(conn.pendingCount(), 1);

  // Backoff fires → socket #1 is dialed but has NOT opened (CONNECTING).
  await new Promise((r) => setTimeout(r, 300));
  assert.equal(socks.length, 2, 'backoff dialed socket #1');

  // wake + an explicit Reconnect-now click WHILE #1 is still in flight must
  // not spawn a second concurrent socket (the H2 flap).
  conn.wake('online');
  conn.reconnectNow();
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(socks.length, 2, 'no concurrent second dial while one is in flight');

  // #1 opens and flushes the queued frame — exactly once.
  socks[1].onopen!(new Event('open'));
  assert.equal(conn.pendingCount(), 0);
  assert.equal(conn.diag().state, 'open');
  assert.equal(countChannel(sent, 7), 1, 'queued frame flushed exactly once');
});

test("a superseded socket's late onclose does not schedule another reconnect", async (t: TestCtx) => {
  const { conn, socks } = hbConn(t);
  socks[0].onopen!(new Event('open'));
  socks[0].onclose!(new Event('close') as CloseEvent); // → reconnecting, backoff armed
  await new Promise((r) => setTimeout(r, 300));
  assert.equal(socks.length, 2, 'backoff dialed socket #1');
  socks[1].onopen!(new Event('open')); // #1 is now the current, open socket
  assert.equal(conn.diag().state, 'open');

  // A late / duplicate close from the OLD socket #0 (a zombie firing after
  // the replacement is live) must be ignored — not re-arm reconnect, not
  // knock the live socket out of 'open'.
  socks[0].onclose!({ type: 'close', code: 1006 } as unknown as CloseEvent);
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(conn.diag().state, 'open', 'stale close ignored');
  assert.equal(socks.length, 2, 'stale close did not trigger a new dial');
});
