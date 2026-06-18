// Origin-scoped window-store merge (M1c, docs/REMOTE.md R2). wm.ts is
// DOM/Solid-bound so it runs here (vitest + jsdom), not under node:test.
// These cover the load-bearing multi-router invariants: two origins
// coexist in one store, a snapshot is authoritative only for its own
// origin, and focus is tracked per (origin,windowID).

import { describe, it, expect, beforeEach } from 'vitest';
import { applySessionSnapshot, raiseLocal, windows, focused } from './wm.ts';
import type { SessionWindow } from './main.ts';

// Elements aren't registered in the test, so mountWhenReady takes the
// waitForBundle path; an immediately-resolving stub lets the upsert run on
// the next microtask. flush() drains those microtasks before asserting.
const immediate = () => Promise.resolve();
async function flush() {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

function sw(id: number, instance: string, opts: Partial<SessionWindow> = {}): SessionWindow {
  return {
    window_id: id,
    instance_id: instance,
    element: `wash-app-${instance}`,
    title: instance,
    x: 0,
    y: 0,
    w: 100,
    h: 100,
    z: id,
    state: 'normal',
    focused: false,
    ...opts,
  };
}

describe('wm origin-scoped merge', () => {
  beforeEach(async () => {
    // Empty snapshots clear each origin (the drop-filter runs synchronously).
    applySessionSnapshot('local', [], immediate);
    applySessionSnapshot('hostB', [], immediate);
    await flush();
  });

  it('merges windows from two origins, even with colliding window ids', async () => {
    applySessionSnapshot('local', [sw(1, 'a')], immediate);
    applySessionSnapshot('hostB', [sw(1, 'b')], immediate);
    await flush();
    expect(windows.length).toBe(2);
    expect(windows.some((w) => w.origin === 'local' && w.windowID === 1)).toBe(true);
    expect(windows.some((w) => w.origin === 'hostB' && w.windowID === 1)).toBe(true);
  });

  it("a B snapshot omitting a window drops only B's — never local's", async () => {
    applySessionSnapshot('local', [sw(1, 'a')], immediate);
    applySessionSnapshot('hostB', [sw(1, 'b'), sw(2, 'b2')], immediate);
    await flush();
    expect(windows.length).toBe(3);

    // B re-snapshots without window 2 (e.g. it closed on B).
    applySessionSnapshot('hostB', [sw(1, 'b')], immediate);
    await flush();
    expect(windows.length).toBe(2);
    expect(windows.some((w) => w.origin === 'local' && w.windowID === 1)).toBe(true);
    expect(windows.some((w) => w.origin === 'hostB' && w.windowID === 2)).toBe(false);
  });

  it('cross-origin raise: gz arbitrates stacking across colliding window ids', async () => {
    applySessionSnapshot('local', [sw(1, 'a')], immediate);
    applySessionSnapshot('hostB', [sw(1, 'b')], immediate);
    await flush();
    const gzOf = (origin: string, id: number) =>
      windows.find((w) => w.origin === origin && w.windowID === id)!.gz;

    // Both have window id 1 (per-router z collides). Raise local, then remote:
    // the remote window must end up on top of the GLOBAL stack — the bug was
    // that B's small B-local z overwrote the raise and it sank behind local.
    raiseLocal('local', 1);
    raiseLocal('hostB', 1);
    expect(gzOf('hostB', 1)).toBeGreaterThan(gzOf('local', 1));

    // Re-raising local flips the global order back.
    raiseLocal('local', 1);
    expect(gzOf('local', 1)).toBeGreaterThan(gzOf('hostB', 1));
  });

  it('focus is per-origin: a local no-claim snapshot leaves B focus intact', async () => {
    applySessionSnapshot('hostB', [sw(5, 'b', { focused: true })], immediate);
    await flush();
    expect(focused()).toEqual({ origin: 'hostB', windowID: 5 });

    applySessionSnapshot('local', [sw(1, 'a')], immediate); // no focus claim
    await flush();
    expect(focused()).toEqual({ origin: 'hostB', windowID: 5 });
  });
});
