// Tests for VirtioConsoleSocket — the WebSocket-shaped shim over a
// v86 virtio-console port.
//
// Run with: cd web/shell && npx tsx --test src/virtio.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  VirtioConsoleSocket,
  virtioConsoleFactory,
  type V86Bus,
} from './virtio.ts';
import { encodeFrame, FLAG_END } from './wire.ts';

// mockBus is a deterministic V86Bus stand-in: every send is captured;
// the test drives onChunk by invoking the handler registered for a
// given event name.
class MockBus implements V86Bus {
  sent: Array<{ event: string; bytes: Uint8Array }> = [];
  private handlers = new Map<string, (data: Uint8Array | ArrayBuffer | number[]) => void>();

  send(event: string, payload: Uint8Array): void {
    this.sent.push({ event, bytes: new Uint8Array(payload) });
  }

  register(event: string, handler: (data: Uint8Array | ArrayBuffer | number[]) => void): void {
    this.handlers.set(event, handler);
  }

  // Drive a chunk into the socket as if v86 emitted it.
  fire(event: string, data: Uint8Array | ArrayBuffer | number[]): void {
    const h = this.handlers.get(event);
    if (!h) throw new Error(`no handler for ${event}`);
    h(data);
  }
}

test('opens on next microtask and fires onopen once', async () => {
  const bus = new MockBus();
  const sock = new VirtioConsoleSocket({ bus, portN: 2 });
  let opens = 0;
  sock.onopen = () => { opens++; };
  await Promise.resolve(); // let the queued microtask run
  await Promise.resolve();
  assert.equal(opens, 1);
});

test('send routes to virtio-console{N}-input-bytes', () => {
  const bus = new MockBus();
  const sock = new VirtioConsoleSocket({ bus, portN: 2 });
  sock.send(new Uint8Array([1, 2, 3]));
  assert.equal(bus.sent.length, 1);
  assert.equal(bus.sent[0].event, 'virtio-console2-input-bytes');
  assert.deepEqual(Array.from(bus.sent[0].bytes), [1, 2, 3]);
});

test('reassembles a single frame split across two chunks', async () => {
  const bus = new MockBus();
  const sock = new VirtioConsoleSocket({ bus, portN: 2 });
  const messages: Uint8Array[] = [];
  sock.onmessage = (ev) => {
    messages.push(new Uint8Array(ev.data as ArrayBuffer));
  };
  await Promise.resolve(); // let onopen fire

  const frame = encodeFrame({ flags: FLAG_END, channel: 0, payload: new TextEncoder().encode('hello') });
  // Split mid-payload.
  bus.fire('virtio-console2-output-bytes', frame.slice(0, 6));
  assert.equal(messages.length, 0, 'no complete frame yet');
  bus.fire('virtio-console2-output-bytes', frame.slice(6));
  assert.equal(messages.length, 1);
  assert.equal(messages[0].byteLength, frame.byteLength);
  // Compare as plain byte sequences (the .buffer in MessageEvent
  // round-trips through Uint8Array construction).
  assert.deepEqual(Array.from(messages[0]), Array.from(frame));
});

test('extracts multiple frames from a single chunk', async () => {
  const bus = new MockBus();
  const sock = new VirtioConsoleSocket({ bus, portN: 2 });
  const messages: Uint8Array[] = [];
  sock.onmessage = (ev) => { messages.push(new Uint8Array(ev.data as ArrayBuffer)); };
  await Promise.resolve();

  const f1 = encodeFrame({ flags: FLAG_END, channel: 0, payload: new Uint8Array([0xAA]) });
  const f2 = encodeFrame({ flags: FLAG_END, channel: 1, payload: new Uint8Array([0xBB, 0xCC]) });
  const merged = new Uint8Array(f1.byteLength + f2.byteLength);
  merged.set(f1, 0);
  merged.set(f2, f1.byteLength);
  bus.fire('virtio-console2-output-bytes', merged);
  assert.equal(messages.length, 2);
  assert.equal(messages[0].byteLength, f1.byteLength);
  assert.equal(messages[1].byteLength, f2.byteLength);
});

test('handles trailing partial frame across chunks', async () => {
  const bus = new MockBus();
  const sock = new VirtioConsoleSocket({ bus, portN: 2 });
  const messages: Uint8Array[] = [];
  sock.onmessage = (ev) => { messages.push(new Uint8Array(ev.data as ArrayBuffer)); };
  await Promise.resolve();

  const f1 = encodeFrame({ flags: FLAG_END, channel: 0, payload: new Uint8Array([1]) });
  const f2 = encodeFrame({ flags: FLAG_END, channel: 0, payload: new Uint8Array([2, 3, 4]) });
  // Send f1 + first byte of f2. Then the rest of f2.
  const partial1 = new Uint8Array(f1.byteLength + 1);
  partial1.set(f1, 0);
  partial1[f1.byteLength] = f2[0];
  bus.fire('virtio-console2-output-bytes', partial1);
  assert.equal(messages.length, 1, 'only f1 is complete');

  bus.fire('virtio-console2-output-bytes', f2.slice(1));
  assert.equal(messages.length, 2);
});

test('ignores chunks after close', async () => {
  const bus = new MockBus();
  const sock = new VirtioConsoleSocket({ bus, portN: 2 });
  const messages: Uint8Array[] = [];
  sock.onmessage = (ev) => { messages.push(new Uint8Array(ev.data as ArrayBuffer)); };
  await Promise.resolve();

  sock.close();
  await Promise.resolve(); // let close microtask run

  const f1 = encodeFrame({ flags: FLAG_END, channel: 0, payload: new Uint8Array([1]) });
  bus.fire('virtio-console2-output-bytes', f1);
  assert.equal(messages.length, 0, 'closed socket must not deliver');
});

test('close fires onclose exactly once', async () => {
  const bus = new MockBus();
  const sock = new VirtioConsoleSocket({ bus, portN: 2 });
  let closes = 0;
  sock.onclose = () => { closes++; };
  sock.close();
  sock.close(); // double-close must be a no-op
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(closes, 1);
});

test('virtioConsoleFactory targets the requested port', () => {
  const bus = new MockBus();
  const factory = virtioConsoleFactory(bus, 3);
  const sock = factory();
  sock.send(new Uint8Array([9]));
  assert.equal(bus.sent[0].event, 'virtio-console3-input-bytes');
});

test('accepts ArrayBuffer chunks from v86', async () => {
  // v86 may deliver via ArrayBuffer rather than Uint8Array depending
  // on browser/build. The shim should handle both.
  const bus = new MockBus();
  const sock = new VirtioConsoleSocket({ bus, portN: 2 });
  const messages: Uint8Array[] = [];
  sock.onmessage = (ev) => { messages.push(new Uint8Array(ev.data as ArrayBuffer)); };
  await Promise.resolve();

  const f1 = encodeFrame({ flags: FLAG_END, channel: 0, payload: new Uint8Array([0xEE]) });
  // Re-wrap as plain ArrayBuffer (a copy, not the same byte-offset slice).
  const ab = f1.slice().buffer;
  bus.fire('virtio-console2-output-bytes', ab);
  assert.equal(messages.length, 1);
});
