import { test } from 'node:test';
import assert from 'node:assert/strict';
import { RelayChannelSocket } from './relay-socket.ts';

// Build a wash frame: [flags:8][channel:24][len:32][payload].
function frame(payload: Uint8Array): Uint8Array {
  const f = new Uint8Array(8 + payload.length);
  f[0] = 0x01; // FlagEnd
  // channel 0 (bytes 1..3 stay 0)
  new DataView(f.buffer).setUint32(4, payload.length, false);
  f.set(payload, 8);
  return f;
}

function fakeConn() {
  const sent: { ch: number; bytes: Uint8Array }[] = [];
  return { sent, sendRaw: (ch: number, bytes: Uint8Array) => sent.push({ ch, bytes }) };
}

test('feed reassembles a frame split across chunks and fires onmessage once', () => {
  const conn = fakeConn();
  const sock = new RelayChannelSocket(conn as never, 7);
  const got: Uint8Array[] = [];
  sock.onmessage = (ev) => got.push(new Uint8Array((ev as MessageEvent).data as ArrayBuffer));

  const f = frame(new TextEncoder().encode('hi'));
  // Split the 10-byte frame across two relay chunks (straddling the header).
  sock.feed(f.slice(0, 5));
  assert.equal(got.length, 0, 'incomplete frame must not dispatch');
  sock.feed(f.slice(5));
  assert.equal(got.length, 1, 'one whole frame dispatched');
  assert.deepEqual(got[0], f);
});

test('two frames in one chunk dispatch as two messages', () => {
  const conn = fakeConn();
  const sock = new RelayChannelSocket(conn as never, 3);
  const got: Uint8Array[] = [];
  sock.onmessage = (ev) => got.push(new Uint8Array((ev as MessageEvent).data as ArrayBuffer));

  const a = frame(new TextEncoder().encode('aa'));
  const b = frame(new TextEncoder().encode('bbb'));
  const both = new Uint8Array(a.length + b.length);
  both.set(a, 0);
  both.set(b, a.length);
  sock.feed(both);
  assert.equal(got.length, 2);
  assert.deepEqual(got[0], a);
  assert.deepEqual(got[1], b);
});

test('send forwards bytes as a raw frame on the relay channel', () => {
  const conn = fakeConn();
  const sock = new RelayChannelSocket(conn as never, 42);
  const payload = new TextEncoder().encode('outbound');
  sock.send(payload);
  assert.equal(conn.sent.length, 1);
  assert.equal(conn.sent[0].ch, 42);
  assert.deepEqual(conn.sent[0].bytes, payload);
});

test('send splits an oversized frame across raw frames within the payload cap', () => {
  const MAX_PAYLOAD = 16 * 1024 * 1024; // mirrors wire.MaxPayload
  const conn = fakeConn();
  const sock = new RelayChannelSocket(conn as never, 9);
  // A max B-frame is MAX_PAYLOAD + 8 bytes on the wire — too big for one raw
  // frame (encodeFrame rejects >MAX_PAYLOAD). It must be split (REVIEW-DATAPATH
  // F10). Pattern the bytes so a reordering/loss bug can't pass by accident.
  const whole = new Uint8Array(MAX_PAYLOAD + 8);
  for (let i = 0; i < whole.length; i++) whole[i] = (i * 7) & 0xff;
  sock.send(whole);

  assert.ok(conn.sent.length >= 2, 'oversized frame must be split into 2+ raw frames');
  const reassembled = new Uint8Array(whole.length);
  let off = 0;
  for (const s of conn.sent) {
    assert.equal(s.ch, 9, 'every piece on the relay channel');
    assert.ok(s.bytes.length <= MAX_PAYLOAD, 'no piece exceeds the payload cap');
    reassembled.set(s.bytes, off);
    off += s.bytes.length;
  }
  assert.equal(off, whole.length, 'pieces reassemble to the whole frame length');
  assert.deepEqual(reassembled, whole, 'pieces reassemble to the original bytes in order');
});

test('a fatal desync invokes onFatalClose and does NOT fire onclose', async () => {
  const conn = fakeConn();
  const sock = new RelayChannelSocket(conn as never, 5);
  let fatal = 0;
  let closed = 0;
  sock.onFatalClose = () => { fatal++; };
  sock.onclose = () => { closed++; };

  // An 8-byte header declaring a payload length past the 16 MiB cap — an
  // unrecoverable stream desync. It must detach the origin (onFatalClose),
  // NOT fire onclose (which would send the RouterClient reconnecting into
  // this same dead socket forever — REVIEW-RECONNECT M6).
  const bad = new Uint8Array(8);
  bad[0] = 0x01;
  new DataView(bad.buffer).setUint32(4, 16 * 1024 * 1024 + 1, false);
  sock.feed(bad);

  await Promise.resolve(); // let any (unwanted) queued onclose microtask run
  assert.equal(fatal, 1, 'fatal desync detaches the origin');
  assert.equal(closed, 0, 'onclose must not fire on a fatal desync');
});

test('frames fed before onmessage attaches are held, then flushed on attach', () => {
  const conn = fakeConn();
  const sock = new RelayChannelSocket(conn as never, 1);
  const f = frame(new TextEncoder().encode('zz'));
  sock.feed(f); // no onmessage yet — must be buffered, not dropped
  const got: Uint8Array[] = [];
  sock.onmessage = (ev) => got.push(new Uint8Array((ev as MessageEvent).data as ArrayBuffer));
  assert.equal(got.length, 1, 'buffered frame flushed when onmessage attaches');
  assert.deepEqual(got[0], f);
});
