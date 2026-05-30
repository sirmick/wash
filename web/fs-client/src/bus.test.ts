// Unit tests for the request/reply bus.
// Run with: node --test apps/fm/fe/src/bus.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { createBus, type BEMessage, type Clock } from './bus.ts';

// Deterministic fake clock: timers fire only when advance() crosses
// their due time, so timeout behaviour is testable without real waits.
class FakeClock implements Clock {
  private seq = 0;
  private now = 0;
  private timers = new Map<number, { fn: () => void; due: number }>();
  setTimeout(fn: () => void, ms: number): unknown {
    const h = ++this.seq;
    this.timers.set(h, { fn, due: this.now + ms });
    return h;
  }
  clearTimeout(handle: unknown): void {
    this.timers.delete(handle as number);
  }
  advance(ms: number): void {
    this.now += ms;
    for (const [h, t] of [...this.timers]) {
      if (t.due <= this.now) {
        this.timers.delete(h);
        t.fn();
      }
    }
  }
  active(): number {
    return this.timers.size;
  }
}

function setup() {
  const sent: unknown[] = [];
  const clock = new FakeClock();
  const bus = createBus((m) => sent.push(m), clock);
  return { sent, clock, bus };
}

test('request sends the payload tagged with a fresh f-<n> id', () => {
  const { sent, bus } = setup();
  void bus.request({ kind: 'list', path: '/' });
  assert.equal(sent.length, 1);
  assert.deepEqual(sent[0], { kind: 'list', path: '/', id: 'f-1' });
});

test('request ids increment per call', () => {
  const { sent, bus } = setup();
  void bus.request({ kind: 'a' });
  void bus.request({ kind: 'b' });
  assert.equal((sent[0] as { id: string }).id, 'f-1');
  assert.equal((sent[1] as { id: string }).id, 'f-2');
});

test('tryResolve matches the echoed id and resolves the promise', async () => {
  const { bus } = setup();
  const p = bus.request({ kind: 'read', path: '/a' });
  const reply: BEMessage = { kind: 'read_ok', id: 'f-1', content: 'hi' };
  assert.equal(bus.tryResolve(reply), true);
  assert.deepEqual(await p, reply);
  assert.equal(bus.pending(), 0);
});

test('tryResolve returns false for messages with no id (pushes)', () => {
  const { bus } = setup();
  void bus.request({ kind: 'list' });
  assert.equal(bus.tryResolve({ kind: 'fs_event', path: '/x' }), false);
  assert.equal(bus.pending(), 1); // the outstanding request is untouched
});

test('tryResolve returns false for an unknown id', () => {
  const { bus } = setup();
  void bus.request({ kind: 'list' });
  assert.equal(bus.tryResolve({ kind: 'read_ok', id: 'f-999' }), false);
  assert.equal(bus.pending(), 1);
});

test('a second reply for the same id is not double-delivered', async () => {
  const { bus } = setup();
  const p = bus.request({ kind: 'read' });
  assert.equal(bus.tryResolve({ kind: 'read_ok', id: 'f-1', n: 1 }), true);
  await p;
  // The BE (mis)fires the same id again — must be ignored, no leak.
  assert.equal(bus.tryResolve({ kind: 'read_ok', id: 'f-1', n: 2 }), false);
  assert.equal(bus.pending(), 0);
});

test('timeout resolves with a synthetic timeout_err and clears the resolver', async () => {
  const { bus, clock } = setup();
  const p = bus.request({ kind: 'read' }, 5000);
  assert.equal(bus.pending(), 1);
  clock.advance(5000);
  const m = await p;
  assert.equal(m.kind, 'timeout_err');
  assert.equal(m.id, 'f-1');
  assert.equal(m.code, 'timeout');
  assert.equal(bus.pending(), 0);
});

test('a reply before the timeout cancels the timer (no double resolve)', async () => {
  const { bus, clock } = setup();
  const p = bus.request({ kind: 'read' }, 5000);
  assert.equal(bus.tryResolve({ kind: 'read_ok', id: 'f-1' }), true);
  assert.equal((await p).kind, 'read_ok');
  // Timer must have been cleared on resolve; advancing fires nothing.
  assert.equal(clock.active(), 0);
  clock.advance(10000);
  assert.equal(bus.pending(), 0);
});
