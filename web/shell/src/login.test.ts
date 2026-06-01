// Tests for the FE login handshake (login.ts) — framework-free, run under
// `node --test` with native TS type-stripping (see test.sh run_fe_unit).

import test from 'node:test';
import assert from 'node:assert/strict';

import { authenticate, isLoginRequired, peekType, LoginError, T_LOGIN, T_LOGIN_OK, T_LOGIN_ERR, T_LOGIN_REQUIRED } from './login.ts';
import { encodeFrame, encodeCtrl, decodeFrame, decodeCtrl, FLAG_END } from './wire.ts';
import type { SocketLike } from './virtio.ts';

// ctrlFrame builds a whole channel-0 wash frame carrying a JSON ctrl message.
function ctrlFrame(msg: unknown): Uint8Array {
  return encodeFrame({ flags: FLAG_END, channel: 0, payload: encodeCtrl(msg) });
}

class FakeSock implements SocketLike {
  binaryType: 'arraybuffer' = 'arraybuffer';
  onopen: ((ev: Event) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;
  onmessage: ((ev: MessageEvent) => unknown) | null = null;
  onclose: ((ev: CloseEvent) => unknown) | null = null;
  sent: Uint8Array[] = [];

  send(data: ArrayBuffer | Uint8Array): void {
    this.sent.push(data instanceof Uint8Array ? data : new Uint8Array(data));
  }
  close(): void {}

  // Test helper: simulate a whole frame arriving from the front.
  deliver(frame: Uint8Array): void {
    this.onmessage?.({ data: frame.slice().buffer } as unknown as MessageEvent);
  }
}

test('authenticate sends a login frame carrying the credentials', () => {
  const sock = new FakeSock();
  authenticate(sock, { user: 'root', pass: 'hunter2' });
  assert.equal(sock.sent.length, 1);
  const f = decodeFrame(sock.sent[0]);
  assert.equal(f.channel, 0);
  const msg = decodeCtrl(f.payload) as { t: string; user: string; pass: string };
  assert.equal(msg.t, T_LOGIN);
  assert.equal(msg.user, 'root');
  assert.equal(msg.pass, 'hunter2');
});

test('authenticate resolves on login.ok', async () => {
  const sock = new FakeSock();
  const p = authenticate(sock, { user: 'root', pass: 'pw' });
  sock.deliver(ctrlFrame({ t: T_LOGIN_OK }));
  await p; // resolves
});

test('authenticate rejects with LoginError + message on login.err', async () => {
  const sock = new FakeSock();
  const p = authenticate(sock, { user: 'root', pass: 'nope' });
  sock.deliver(ctrlFrame({ t: T_LOGIN_ERR, msg: 'invalid credentials' }));
  await assert.rejects(p, (e: unknown) => e instanceof LoginError && /invalid credentials/.test((e as Error).message));
});

test('login.required while waiting is ignored, ok still resolves', async () => {
  const sock = new FakeSock();
  const p = authenticate(sock, { user: 'root', pass: 'pw' });
  sock.deliver(ctrlFrame({ t: T_LOGIN_REQUIRED })); // re-emitted prompt — ignore
  sock.deliver(ctrlFrame({ t: T_LOGIN_OK }));
  await p;
});

test('authenticate restores the prior onmessage handler before resolving', async () => {
  const sock = new FakeSock();
  const prior = () => {};
  sock.onmessage = prior;
  const p = authenticate(sock, { user: 'root', pass: 'pw' });
  assert.notEqual(sock.onmessage, prior); // taken over while waiting
  sock.deliver(ctrlFrame({ t: T_LOGIN_OK }));
  await p;
  assert.equal(sock.onmessage, prior); // restored for the shell to use
});

test('socket close mid-handshake rejects', async () => {
  const sock = new FakeSock();
  const p = authenticate(sock, { user: 'root', pass: 'pw' });
  sock.onclose?.({} as CloseEvent);
  await assert.rejects(p, /socket closed during login/);
});

test('isLoginRequired / peekType classify control payloads', () => {
  assert.equal(isLoginRequired(encodeCtrl({ t: T_LOGIN_REQUIRED })), true);
  assert.equal(isLoginRequired(encodeCtrl({ t: 'catalog' })), false);
  assert.equal(peekType(encodeCtrl({ t: 'catalog' })), 'catalog');
  assert.equal(peekType(new Uint8Array([0x00, 0x01])), ''); // not JSON ctrl
});
