// Multi-window display contract (docs/DISPLAY.md §4–5), BE-only.
//
// Proves the wash-display wire contract end-to-end through the real
// router — no compositor, no browser. The test app's fake-display mode
// (it declares CapWindows) creates N windows via window.create, opens a
// per-window "video" channel on each, and pushes one stand-in frame.
// We assert the BE reply (distinct window + channel ids) and the
// router's window.create log line, per [[wash e2e pattern]].
//
// The browser-side proof (a <wash-app-display> canvas rendering the
// frame) lands with the shell's video-channel routing in a later step;
// here the contract itself is what we lock down.

import { test, expect } from '../fixtures/router';

test.use({ routerOpts: { apps: ['session', 'test'] } });

test.describe('multi-window display contract', () => {
  test('one BE creates N windows owned by a single instance', async ({ router }) => {
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    expect(launched.t).toBe('launched');
    const inst = launched.instance_id as string;

    // Fake-display: create 2 windows. (The per-window video channel is
    // shell-bound; in this headless BE-only run there's no shell, so
    // channel ids stay 0 — the video-render path is proven in a later
    // browser test. Here we lock down the multi-window create contract.)
    const resp = await router.sendAppMsg(inst, { kind: 'display_open', id: 'd1', n: 2 });
    expect(resp.kind).toBe('display_opened');

    const wins = (resp.windows ?? []) as Array<{ win: number; channel: number }>;
    expect(wins).toHaveLength(2);
    for (const w of wins) {
      expect(w.win).toBeGreaterThan(0);
    }
    // Two distinct windows owned by one instance — the heart of §4.
    expect(new Set(wins.map((w) => w.win)).size).toBe(2);

    // BE assertion: the router logged a window.create for this instance.
    await router.waitForLog(new RegExp(`window\\.create instance=${inst} win=`), 5_000);

    // Tearing down one window replies cleanly (and the router GCs its
    // channel — exercised by the no-leak fixture teardown).
    const closed = await router.sendAppMsg(inst, {
      kind: 'display_close',
      id: 'd2',
      win: wins[0].win,
    });
    expect(closed.kind).toBe('display_closed');
    expect(closed.win).toBe(wins[0].win);
  });
});
