// Tests for Conn's /auth/check reconnect preflight — the logic that
// tells "auth gone, stop looping" apart from "transient drop, keep
// reconnecting". We exercise authGone() directly with an injected
// fetch, so no real WebSocket / network is involved.
//
// Run with: cd web/shell && npx tsx --test src/ws.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { Conn } from './ws.ts';
import type { SocketLike } from './virtio.ts';

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
