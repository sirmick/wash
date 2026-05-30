// Unit tests for the fs.watch bookkeeping + refresh debounce.
// Run with: node --test apps/fm/fe/src/watch.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { createWatch, type Clock } from './watch.ts';

// Deterministic fake clock: timers fire only when advance() crosses
// their due time, so the debounce is testable without real waits.
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

interface Opts {
  shouldRefresh?: (dir: string) => boolean;
  debounceMs?: number;
  watchKind?: string;
  unwatchKind?: string;
}

function setup(opts: Opts = {}) {
  const sent: unknown[] = [];
  const refreshed: string[] = [];
  const clock = new FakeClock();
  const watch = createWatch({
    send: (m) => sent.push(m),
    refresh: (dir) => refreshed.push(dir),
    shouldRefresh: opts.shouldRefresh ?? (() => true),
    clock,
    debounceMs: opts.debounceMs ?? 100,
    watchKind: opts.watchKind,
    unwatchKind: opts.unwatchKind,
  });
  return { sent, refreshed, clock, watch };
}

// ---- watch / unwatch dedup ----

test('watch records the dir and sends one watch request', () => {
  const { sent, watch } = setup();
  watch.watch('/a');
  assert.equal(watch.isWatching('/a'), true);
  assert.deepEqual(sent, [{ kind: 'watch', path: '/a' }]);
});

test('watching the same dir twice sends only one request', () => {
  const { sent, watch } = setup();
  watch.watch('/a');
  watch.watch('/a');
  assert.equal(sent.length, 1);
});

test('unwatch drops the dir and sends one unwatch request', () => {
  const { sent, watch } = setup();
  watch.watch('/a');
  watch.unwatch('/a');
  assert.equal(watch.isWatching('/a'), false);
  assert.deepEqual(sent, [
    { kind: 'watch', path: '/a' },
    { kind: 'unwatch', path: '/a' },
  ]);
});

test('watch ignores an empty path (no request, not tracked)', () => {
  const { sent, watch } = setup();
  watch.watch('');
  assert.equal(watch.isWatching(''), false);
  assert.equal(sent.length, 0);
});

test('custom watchKind/unwatchKind are used in the messages (edit: fs.watch)', () => {
  const { sent, watch } = setup({ watchKind: 'fs.watch', unwatchKind: 'fs.unwatch' });
  watch.watch('/a');
  watch.unwatch('/a');
  assert.deepEqual(sent, [
    { kind: 'fs.watch', path: '/a' },
    { kind: 'fs.unwatch', path: '/a' },
  ]);
});

test('unwatchWhere unwatches the matching subtree only (one request each)', () => {
  const { sent, watch } = setup();
  for (const d of ['/a', '/a/b', '/a/b/c', '/other']) watch.watch(d);
  sent.length = 0; // drop the watch requests; focus on the teardown
  // collapse /a → unwatch /a and everything under it, but not /other.
  watch.unwatchWhere((w) => w === '/a' || w.startsWith('/a/'));
  assert.deepEqual(sent, [
    { kind: 'unwatch', path: '/a' },
    { kind: 'unwatch', path: '/a/b' },
    { kind: 'unwatch', path: '/a/b/c' },
  ]);
  assert.equal(watch.isWatching('/other'), true);
  assert.equal(watch.isWatching('/a/b'), false);
});

test('unwatchWhere(() => true) releases everything (unmount teardown)', () => {
  const { sent, watch } = setup();
  watch.watch('/a');
  watch.watch('/b');
  sent.length = 0;
  watch.unwatchWhere(() => true);
  assert.deepEqual(sent.map((m) => (m as { path: string }).path).sort(), ['/a', '/b']);
  assert.equal(watch.isWatching('/a'), false);
  assert.equal(watch.isWatching('/b'), false);
});

test('unwatch on an unwatched dir is a no-op (no request)', () => {
  const { sent, watch } = setup();
  watch.unwatch('/never');
  assert.equal(sent.length, 0);
});

// ---- scheduleRefresh debounce ----

test('scheduleRefresh re-lists once after the debounce window', () => {
  const { refreshed, clock, watch } = setup();
  watch.scheduleRefresh('/a');
  assert.deepEqual(refreshed, []); // not yet
  clock.advance(100);
  assert.deepEqual(refreshed, ['/a']);
});

test('bursts of scheduleRefresh for one dir coalesce into a single re-list', () => {
  const { refreshed, clock, watch } = setup();
  watch.scheduleRefresh('/a');
  clock.advance(40);
  watch.scheduleRefresh('/a'); // resets the timer
  clock.advance(40);
  watch.scheduleRefresh('/a'); // resets again
  assert.deepEqual(refreshed, []); // still within a window each time
  clock.advance(100);
  assert.deepEqual(refreshed, ['/a']); // exactly one re-list
});

test('refreshes for distinct dirs are tracked independently', () => {
  const { refreshed, clock, watch } = setup();
  watch.scheduleRefresh('/a');
  watch.scheduleRefresh('/b');
  clock.advance(100);
  assert.deepEqual(refreshed.sort(), ['/a', '/b']);
});

test('scheduleRefresh is skipped at schedule time when shouldRefresh is false', () => {
  const { refreshed, clock, watch } = setup({ shouldRefresh: () => false });
  watch.scheduleRefresh('/a');
  assert.equal(clock.active(), 0); // no timer armed
  clock.advance(100);
  assert.deepEqual(refreshed, []);
});

test('a dir collapsed during the debounce window is re-checked and skipped at fire time', () => {
  // shouldRefresh starts true (timer arms) then flips false before it fires —
  // models the user collapsing the row mid-debounce.
  let allow = true;
  const { refreshed, clock, watch } = setup({ shouldRefresh: () => allow });
  watch.scheduleRefresh('/a');
  assert.equal(clock.active(), 1);
  allow = false;
  clock.advance(100);
  assert.deepEqual(refreshed, []); // re-check at fire time suppressed it
});

test('dispose clears pending timers so nothing fires afterwards', () => {
  const { refreshed, clock, watch } = setup();
  watch.scheduleRefresh('/a');
  watch.scheduleRefresh('/b');
  assert.equal(clock.active(), 2);
  watch.dispose();
  assert.equal(clock.active(), 0);
  clock.advance(1000);
  assert.deepEqual(refreshed, []);
});
