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
