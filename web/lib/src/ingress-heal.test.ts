// Unit tests for the ingress self-heal decision logic (ingress-heal.ts):
// status → verdict, backoff, base extraction, and the createIngressHeal
// state machine driven through fake deps — a scripted probe, a toggleable
// link, a manual reconnect cue and a manual clock. Run via `make fe-unit`.

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  MAX_HEAL_ATTEMPTS,
  createIngressHeal,
  healDelay,
  ingressBase,
  verdictForStatus,
  type IngressHealDeps,
  type IngressHealOptions,
} from './ingress-heal.ts';

test('verdictForStatus: 401/410 dead, failures unknown, everything else alive', () => {
  assert.equal(verdictForStatus(401), 'dead');
  assert.equal(verdictForStatus(410), 'dead');
  assert.equal(verdictForStatus(null), 'unknown');
  assert.equal(verdictForStatus(0), 'unknown');
  for (const s of [200, 204, 302, 404, 405, 502, 503]) assert.equal(verdictForStatus(s), 'alive', String(s));
});

test('healDelay doubles from 1s and caps at 15s', () => {
  assert.deepEqual([0, 1, 2, 3, 4, 10].map(healDelay), [1000, 2000, 4000, 8000, 15000, 15000]);
});

test('ingressBase pulls /app/<token>/ out of paths and URLs', () => {
  assert.equal(ingressBase('/app/abc/'), '/app/abc/');
  assert.equal(ingressBase('/app/abc/?folder=%2Fhome'), '/app/abc/');
  assert.equal(ingressBase('/wash/app/abc/lib/a%20b.mp3'), '/app/abc/');
  assert.equal(ingressBase('https://h:8080/app/abc/stream?i=1&n=2'), '/app/abc/');
  assert.equal(ingressBase('/app/abc'), ''); // bare, no slash: not a base
  assert.equal(ingressBase('https://radio.example/stream.mp3'), '');
  assert.equal(ingressBase(''), '');
  assert.equal(ingressBase(undefined), '');
});

// harness builds a controller over fake deps. The probe answers from a
// per-base script (a status, or a queue of them); timers run only when the
// test advances the clock.
function harness(init: { base?: string; status?: Record<string, number | null | (number | null)[]> } = {}) {
  let base = init.base ?? '/app/t1/';
  const script = init.status ?? {};
  let up = true;
  let reconnect: (() => void) | null = null;
  const probes: string[] = [];
  const reensures: string[] = [];
  const recovered: boolean[] = [];
  const timers = new Map<number, { at: number; fn: () => void }>();
  let now = 0;
  let nextId = 1;
  let unsubscribed = false;

  const deps: IngressHealDeps = {
    probe: async (b) => {
      probes.push(b);
      const s = script[b];
      if (Array.isArray(s)) return s.length > 1 ? (s.shift() as number | null) : (s[0] ?? null);
      return s === undefined ? 200 : s;
    },
    linkUp: () => up,
    onReconnect: (cb) => {
      reconnect = cb;
      return () => {
        unsubscribed = true;
      };
    },
    setTimeout: (fn, ms) => {
      const id = nextId++;
      timers.set(id, { at: now + ms, fn });
      return id;
    },
    clearTimeout: (h) => {
      timers.delete(h as number);
    },
  };
  const opts: IngressHealOptions = {
    base: () => base,
    reensure: () => reensures.push(base),
    onRecovered: (same) => recovered.push(same),
  };
  const heal = createIngressHeal(opts, deps);
  // settle lets the probe promise chain resolve.
  const settle = async () => {
    for (let i = 0; i < 5; i++) await Promise.resolve();
  };
  return {
    heal,
    probes,
    reensures,
    recovered,
    timers,
    get unsubscribed() {
      return unsubscribed;
    },
    setBase: (b: string) => {
      base = b;
    },
    setStatus: (b: string, s: number | null | (number | null)[]) => {
      script[b] = s;
    },
    setUp: (v: boolean) => {
      up = v;
    },
    reconnect: async () => {
      reconnect?.();
      await settle();
    },
    check: async () => {
      heal.check();
      await settle();
    },
    // advance runs every timer due within ms, in order.
    advance: async (ms: number) => {
      const until = now + ms;
      for (;;) {
        const due = [...timers.entries()].filter(([, t]) => t.at <= until).sort((a, b) => a[1].at - b[1].at)[0];
        if (!due) break;
        timers.delete(due[0]);
        now = due[1].at;
        due[1].fn();
        await settle();
      }
      now = until;
    },
    settle,
  };
}

test('alive probe does nothing', async () => {
  const h = harness();
  await h.check();
  assert.deepEqual(h.probes, ['/app/t1/']);
  assert.deepEqual(h.reensures, []);
  assert.deepEqual(h.recovered, []);
  assert.equal(h.timers.size, 0);
});

test('probes the base, not the full URL it was handed', async () => {
  const h = harness({ base: '/app/t1/?folder=%2Fsrc' });
  await h.check();
  assert.deepEqual(h.probes, ['/app/t1/']);
});

test('no base → no probe', async () => {
  const h = harness({ base: '' });
  await h.check();
  assert.deepEqual(h.probes, []);
});

test('410 → re-ensure; the fresh base answering alive recovers (not same-base)', async () => {
  const h = harness({ status: { '/app/t1/': 410 } });
  await h.check();
  assert.deepEqual(h.reensures, ['/app/t1/']);
  assert.equal(h.timers.size, 1, 'follow-up probe scheduled');
  // The BE answers with a fresh path; the app re-points and the scheduled
  // follow-up confirms it.
  h.setBase('/app/t2/');
  await h.advance(healDelay(0));
  assert.deepEqual(h.probes, ['/app/t1/', '/app/t2/']);
  assert.deepEqual(h.recovered, [false]);
  assert.deepEqual(h.reensures, ['/app/t1/'], 'no second re-ensure');
  assert.equal(h.timers.size, 0);
});

test('401 that clears in place recovers with sameBase=true', async () => {
  const h = harness({ status: { '/app/t1/': [401, 200] } });
  await h.check();
  assert.deepEqual(h.reensures, ['/app/t1/']);
  await h.advance(healDelay(0));
  assert.deepEqual(h.recovered, [true]);
});

test('a check from the app after the fresh base lands confirms early and cancels the timer', async () => {
  const h = harness({ status: { '/app/t1/': 410 } });
  await h.check();
  h.setBase('/app/t2/');
  await h.check();
  assert.deepEqual(h.recovered, [false]);
  assert.equal(h.timers.size, 0);
});

test('a token that stays dead re-ensures with backoff, then gives up', async () => {
  const h = harness({ status: { '/app/t1/': 410 } });
  await h.check();
  for (let i = 0; i < MAX_HEAL_ATTEMPTS + 3; i++) await h.advance(15_000);
  assert.equal(h.reensures.length, MAX_HEAL_ATTEMPTS);
  assert.equal(h.timers.size, 0, 'nothing left scheduled after giving up');
  // Further cues from the app (iframe loads, media errors) don't restart it…
  await h.check();
  assert.equal(h.reensures.length, MAX_HEAL_ATTEMPTS);
  // …but a reconnect is a fresh episode.
  await h.reconnect();
  assert.equal(h.reensures.length, MAX_HEAL_ATTEMPTS + 1);
});

test('follow-up probes are spaced by healDelay', async () => {
  const h = harness({ status: { '/app/t1/': 410 } });
  await h.check();
  await h.advance(healDelay(0) - 1);
  assert.equal(h.reensures.length, 1, 'not before the first delay');
  await h.advance(1);
  assert.equal(h.reensures.length, 2);
  await h.advance(healDelay(1) - 1);
  assert.equal(h.reensures.length, 2);
  await h.advance(1);
  assert.equal(h.reensures.length, 3);
});

test('never probes or re-ensures while the link is down; reconnect resumes', async () => {
  const h = harness({ status: { '/app/t1/': 410 } });
  h.setUp(false);
  await h.check();
  assert.deepEqual(h.probes, [], 'no probe while down');
  h.setUp(true);
  await h.reconnect();
  assert.deepEqual(h.probes, ['/app/t1/']);
  assert.deepEqual(h.reensures, ['/app/t1/']);
});

test('link drops while the probe is in flight → dead verdict is not acted on', async () => {
  const h = harness({ status: { '/app/t1/': 410 } });
  h.heal.check();
  h.setUp(false);
  await h.settle();
  assert.deepEqual(h.reensures, []);
  assert.equal(h.timers.size, 0);
});

test('unknown (probe failed) never re-ensures', async () => {
  const h = harness({ status: { '/app/t1/': null } });
  await h.check();
  await h.reconnect();
  assert.equal(h.probes.length, 2);
  assert.deepEqual(h.reensures, []);
});

test('concurrent checks coalesce into one probe', async () => {
  const h = harness();
  h.heal.check();
  h.heal.check();
  h.heal.check();
  await h.settle();
  assert.equal(h.probes.length, 1);
});

test('a base swapped mid-probe is re-probed, the stale answer dropped', async () => {
  const h = harness({ status: { '/app/t1/': 410 } });
  h.heal.check();
  h.setBase('/app/t2/');
  await h.settle();
  assert.deepEqual(h.probes, ['/app/t1/', '/app/t2/']);
  assert.deepEqual(h.reensures, []);
});

test('dispose cancels the follow-up and unsubscribes from reconnects', async () => {
  const h = harness({ status: { '/app/t1/': 410 } });
  await h.check();
  assert.equal(h.timers.size, 1);
  h.heal.dispose();
  assert.equal(h.timers.size, 0);
  assert.equal(h.unsubscribed, true);
  await h.check();
  assert.equal(h.probes.length, 1);
});
