// Origin-scoped window-store merge (M1c, docs/REMOTE.md R2). wm.ts is
// DOM/Solid-bound so it runs here (vitest + jsdom), not under node:test.
// These cover the load-bearing multi-router invariants: two origins
// coexist in one store, a snapshot is authoritative only for its own
// origin, and focus is tracked per (origin,windowID).

import { describe, it, expect, beforeEach } from 'vitest';
import {
  applySessionSnapshot,
  applySessionPatch,
  raiseLocal,
  windows,
  focused,
  moveLocal,
  markGeomPending,
  pendingGeomTok,
  nextGeomTok,
} from './wm.ts';
import type { SessionWindow, SessionPatch } from './main.ts';

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

  it("a remote reconnect snapshot doesn't steal focus from the active origin", async () => {
    // User is working in a local window.
    applySessionSnapshot('local', [sw(1, 'a', { focused: true })], immediate);
    applySessionSnapshot('hostB', [sw(5, 'b', { focused: true })], immediate);
    await flush();
    // hostB connected first-time here; but focus must stay where the user is.
    raiseLocal('local', 1);
    expect(focused()).toEqual({ origin: 'local', windowID: 1 });
    const gzLocalBefore = windows.find((w) => w.origin === 'local' && w.windowID === 1)!.gz;

    // hostB drops and reconnects: it re-attests its own window as focused.
    applySessionSnapshot('hostB', [sw(5, 'b', { focused: true })], immediate);
    await flush();

    // Focus and the global stack must not have been yanked to hostB.
    expect(focused()).toEqual({ origin: 'local', windowID: 1 });
    const gzLocalAfter = windows.find((w) => w.origin === 'local' && w.windowID === 1)!.gz;
    const gzRemote = windows.find((w) => w.origin === 'hostB' && w.windowID === 5)!.gz;
    expect(gzRemote).toBeLessThan(gzLocalAfter);
    expect(gzLocalAfter).toBe(gzLocalBefore);
  });
});

// REVIEW-RECONNECT M5: a window whose bundle is still in flight isn't in the
// store yet, so a superseding snapshot or delete can't filter it — the late
// upsert would land an unclosable ghost. A controllable waitForBundle lets us
// hold the mount pending, supersede it, then resolve and assert no ghost.
describe('wm deferred-mount ghost guard (M5)', () => {
  function deferred(): { promise: Promise<void>; resolve: () => void } {
    let resolve!: () => void;
    const promise = new Promise<void>((r) => { resolve = r; });
    return { promise, resolve };
  }
  // waitForBundle that blocks only the named instance; everything else is ready.
  const gateFor = (instance: string, p: Promise<void>) =>
    (id: string) => (id === instance ? p : Promise.resolve());

  beforeEach(async () => {
    // Clear BOTH origins — module state persists across describe blocks, so a
    // lingering hostB window with a colliding id would confuse origin-agnostic
    // checks.
    applySessionSnapshot('local', [], immediate);
    applySessionSnapshot('hostB', [], immediate);
    await flush();
  });

  it('drops a deferred mount superseded by a reconnect snapshot that omits it', async () => {
    const gate = deferred();
    applySessionSnapshot('local', [sw(5, 'ghost')], gateFor('ghost', gate.promise));
    await flush();
    expect(windows.some((w) => w.origin === 'local' && w.windowID === 5)).toBe(false); // still pending

    applySessionSnapshot('local', [], immediate); // reconnect omits window 5
    await flush();
    gate.resolve(); // bundle finally arrives
    await flush();

    expect(windows.some((w) => w.origin === 'local' && w.windowID === 5)).toBe(false);
  });

  it('drops a deferred mount cancelled by a window.delete', async () => {
    const gate = deferred();
    applySessionSnapshot('local', [sw(6, 'ghost')], gateFor('ghost', gate.promise));
    await flush();

    const del: SessionPatch = { op: 'window.delete', window_id: 6 };
    applySessionPatch('local', [del], immediate);
    await flush();
    gate.resolve();
    await flush();

    expect(windows.some((w) => w.origin === 'local' && w.windowID === 6)).toBe(false);
  });

  it('still mounts a deferred window when nothing supersedes it', async () => {
    const gate = deferred();
    applySessionSnapshot('local', [sw(7, 'late')], gateFor('late', gate.promise));
    await flush();
    gate.resolve();
    await flush();

    expect(windows.some((w) => w.origin === 'local' && w.windowID === 7)).toBe(true);
  });

  it('re-mounts a window a reconnect snapshot still wants (new schedule wins)', async () => {
    const gate1 = deferred();
    applySessionSnapshot('local', [sw(8, 'keep')], gateFor('keep', gate1.promise));
    await flush();
    // Reconnect snapshot still includes window 8, with its bundle now ready.
    applySessionSnapshot('local', [sw(8, 'keep')], immediate);
    await flush();
    // The stale first schedule resolves late — must not double-insert or drop.
    gate1.resolve();
    await flush();

    expect(windows.filter((w) => w.origin === 'local' && w.windowID === 8).length).toBe(1);
  });
});

describe('wm pending geometry commit (window.move tok)', () => {
  beforeEach(async () => {
    applySessionSnapshot('local', [], immediate);
    await flush();
  });

  it('a stale upsert keeps the committed geometry until the token echoes', async () => {
    applySessionSnapshot('local', [sw(1, 'a', { x: 10, y: 10 })], immediate);
    await flush();
    // Drop: optimistic store write + tagged commit, as window.tsx does.
    moveLocal('local', 1, 300, 200);
    markGeomPending('local', 1, 42);
    // The focus upsert from pointer-down lands after the drop, carrying
    // the pre-drag position: geometry stays, focus is taken.
    applySessionPatch('local', [{ op: 'window.upsert', window: sw(1, 'a', { x: 10, y: 10, focused: true }) }], immediate);
    await flush();
    const w = windows.find((x) => x.windowID === 1)!;
    expect([w.x, w.y]).toEqual([300, 200]);
    expect(focused()).toEqual({ origin: 'local', windowID: 1 });
    expect(pendingGeomTok('local', 1)).toBe(42);
    // The router's echo (its clamp of our move) is accepted and clears.
    applySessionPatch('local', [{ op: 'window.upsert', window: sw(1, 'a', { x: 290, y: 200, geom_tok: 42 }) }], immediate);
    await flush();
    expect([windows[0].x, windows[0].y]).toEqual([290, 200]);
    expect(pendingGeomTok('local', 1)).toBeUndefined();
    // Nothing pending: a later router move is taken as before.
    applySessionPatch('local', [{ op: 'window.upsert', window: sw(1, 'a', { x: 5, y: 5, geom_tok: 42 }) }], immediate);
    await flush();
    expect([windows[0].x, windows[0].y]).toEqual([5, 5]);
  });

  it('a newer commit supersedes the older token; a snapshot clears it', async () => {
    applySessionSnapshot('local', [sw(1, 'a')], immediate);
    await flush();
    markGeomPending('local', 1, 1);
    markGeomPending('local', 1, 2);
    moveLocal('local', 1, 50, 50);
    // Echo of the FIRST commit is itself stale now.
    applySessionPatch('local', [{ op: 'window.upsert', window: sw(1, 'a', { x: 20, y: 20, geom_tok: 1 }) }], immediate);
    await flush();
    expect([windows[0].x, windows[0].y]).toEqual([50, 50]);
    expect(pendingGeomTok('local', 1)).toBe(2);
    applySessionSnapshot('local', [sw(1, 'a', { x: 7, y: 7 })], immediate);
    await flush();
    expect(pendingGeomTok('local', 1)).toBeUndefined();
    expect([windows[0].x, windows[0].y]).toEqual([7, 7]);
  });

  it('a commit whose echo never comes stops holding geometry after the TTL', async () => {
    applySessionSnapshot('local', [sw(1, 'a')], immediate);
    await flush();
    moveLocal('local', 1, 50, 50);
    markGeomPending('local', 1, 3, Date.now() - 10_000);
    applySessionPatch('local', [{ op: 'window.upsert', window: sw(1, 'a', { x: 20, y: 20, geom_tok: 99 }) }], immediate);
    await flush();
    expect([windows[0].x, windows[0].y]).toEqual([20, 20]);
    expect(pendingGeomTok('local', 1)).toBeUndefined();
  });

  it('tokens are non-zero and distinct', () => {
    const a = nextGeomTok();
    const b = nextGeomTok();
    expect(a).not.toBe(0);
    expect(b).not.toBe(a);
  });
});
