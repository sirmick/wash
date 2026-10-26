// Unit tests for the screensaver kernels: the idle timer (fake clock), config
// normalization, saver resolution and option defaults. The overlay itself is
// covered by screensaver.ctest.ts. Run via `make fe-unit`.

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  createIdleTimer,
  normalizeScreensaver,
  optionValues,
  resolveSaver,
  RANDOM_SAVER,
  SCREENSAVERS,
  type IdleClock,
  type SaverOption,
} from './screensaver.ts';

// fakeClock: time only moves when the test says so; timers fire in order.
function fakeClock() {
  let t = 0;
  let seq = 0;
  const timers = new Map<number, { at: number; fn: () => void }>();
  const clock: IdleClock & { advance(ms: number): void; pending(): number } = {
    now: () => t,
    setTimeout(fn, ms) {
      const id = ++seq;
      timers.set(id, { at: t + ms, fn });
      return id;
    },
    clearTimeout(id) {
      timers.delete(id as number);
    },
    advance(ms) {
      const end = t + ms;
      for (;;) {
        let next: [number, { at: number; fn: () => void }] | null = null;
        for (const e of timers) if (e[1].at <= end && (!next || e[1].at < next[1].at)) next = e;
        if (!next) break;
        timers.delete(next[0]);
        t = next[1].at;
        next[1].fn();
      }
      t = end;
    },
    pending: () => timers.size,
  };
  return clock;
}

test('idle fires once after the timeout with no activity', () => {
  const clock = fakeClock();
  let fired = 0;
  const it = createIdleTimer({ timeoutMs: 1000, onIdle: () => fired++, clock });
  clock.advance(999);
  assert.equal(fired, 0);
  clock.advance(1);
  assert.equal(fired, 1);
  assert.equal(it.idle, true);
  clock.advance(10_000);
  assert.equal(fired, 1, 'not again until there has been activity');
});

test('activity pushes the deadline back without arming a timer per event', () => {
  const clock = fakeClock();
  let fired = 0;
  const it = createIdleTimer({ timeoutMs: 1000, onIdle: () => fired++, clock });
  for (let i = 0; i < 50; i++) {
    clock.advance(100);
    it.activity();
  }
  assert.ok(clock.pending() <= 2, `timers pending: ${clock.pending()}`);
  assert.equal(fired, 0);
  clock.advance(999);
  assert.equal(fired, 0);
  clock.advance(1);
  assert.equal(fired, 1);
});

test('activity after idle re-arms', () => {
  const clock = fakeClock();
  let fired = 0;
  const it = createIdleTimer({ timeoutMs: 500, onIdle: () => fired++, clock });
  clock.advance(500);
  assert.equal(fired, 1);
  it.activity();
  assert.equal(it.idle, false);
  clock.advance(500);
  assert.equal(fired, 2);
});

test('canIdle veto waits a full timeout before asking again', () => {
  const clock = fakeClock();
  let fired = 0;
  let hidden = true;
  createIdleTimer({ timeoutMs: 1000, onIdle: () => fired++, canIdle: () => !hidden, clock });
  clock.advance(1000);
  assert.equal(fired, 0);
  hidden = false;
  clock.advance(999);
  assert.equal(fired, 0);
  clock.advance(1);
  assert.equal(fired, 1);
});

test('timeout 0 disables; setTimeoutMs re-enables from now', () => {
  const clock = fakeClock();
  let fired = 0;
  const it = createIdleTimer({ timeoutMs: 0, onIdle: () => fired++, clock });
  clock.advance(100_000);
  assert.equal(fired, 0);
  assert.equal(clock.pending(), 0);
  it.setTimeoutMs(2000);
  clock.advance(1999);
  assert.equal(fired, 0);
  clock.advance(1);
  assert.equal(fired, 1);
  it.stop();
  it.activity();
  clock.advance(10_000);
  assert.equal(fired, 1, 'stopped');
});

test('normalizeScreensaver applies defaults and clamps', () => {
  assert.deepEqual(normalizeScreensaver(undefined), { enabled: false, timeout_s: 600, saver: RANDOM_SAVER, options: {} });
  const n = normalizeScreensaver({ enabled: true, timeout_s: 0.2, saver: 'nope', options: { harbor: { fog: 1 }, bad: 3 } });
  assert.equal(n.enabled, true);
  assert.equal(n.timeout_s, 1);
  assert.equal(n.saver, RANDOM_SAVER);
  assert.deepEqual(n.options, { harbor: { fog: 1 } });
  assert.equal(normalizeScreensaver({ enabled: 'yes' }).enabled, false);
  assert.equal(normalizeScreensaver({ saver: 'harbor' }).saver, 'harbor');
});

test('resolveSaver: random draws only animated savers; unknown ids blank', () => {
  const animated = SCREENSAVERS.filter((s) => s.module).map((s) => s.id);
  const seen = new Set<string>();
  for (let i = 0; i < animated.length; i++) seen.add(resolveSaver(RANDOM_SAVER, () => (i + 0.5) / animated.length));
  assert.deepEqual([...seen].sort(), [...animated].sort());
  assert.equal(resolveSaver(RANDOM_SAVER, () => 0.9999999), animated[animated.length - 1]);
  assert.equal(resolveSaver('harbor'), 'harbor');
  assert.equal(resolveSaver('gone'), 'blank');
});

test('optionValues fills defaults, clamps ranges, rejects bad choices', () => {
  const specs: SaverOption[] = [
    { key: 'fog', label: 'Fog', kind: 'range', min: 0, max: 1, default: 0.2 },
    { key: 'look', label: 'Look', kind: 'select', choices: [['a', 'A'], ['b', 'B']], default: 'a' },
    { key: 'hud', label: 'HUD', kind: 'toggle', default: false },
  ];
  assert.deepEqual(optionValues(specs, undefined), { fog: 0.2, look: 'a', hud: false });
  assert.deepEqual(optionValues(specs, { fog: 7, look: 'b', hud: true, extra: 1 }), { fog: 1, look: 'b', hud: true });
  assert.deepEqual(optionValues(specs, { fog: 'x', look: 'zzz', hud: 1 }), { fog: 0.2, look: 'a', hud: false });
});
