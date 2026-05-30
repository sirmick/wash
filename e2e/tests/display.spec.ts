// Multi-window display contract (docs/DISPLAY.md §4–5).
//
// Two layers:
//   1. BE/router contract (headless, no browser): the test app's
//      fake-display mode (it declares CapWindows) creates N windows via
//      window.create, opens a per-window "video" channel on each, and
//      pushes one stand-in frame. We assert the BE reply (distinct
//      window + channel ids) and the router's window.create log line,
//      per [[wash e2e pattern]].
//   2. FE render (browser): with a shell attached, each window's video
//      channel binds (kind="video") and the built-in <wash-app-display>
//      element decodes the canned frame onto its <canvas> — the pixel
//      readback proves the decode/draw path.

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

  test('browser: <wash-app-display> decodes the canned video frame', async ({ page, router }) => {
    // The shell page is served by the same router. Load it, then drive
    // the test app's fake-display mode through the control socket. The
    // test app writes the canned frame (45-byte LE header + a 1x1
    // opaque PNG) the moment it opens each window's video channel; the
    // router binds the channel to the shell with kind="video", and
    // main.tsx routes the bytes to the matching <wash-app-display>
    // element keyed by window id.
    await page.goto(router.url);

    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    expect(launched.t).toBe('launched');
    const inst = launched.instance_id as string;

    const resp = await router.sendAppMsg(inst, { kind: 'display_open', id: 'b1', n: 2 });
    expect(resp.kind).toBe('display_opened');
    const wins = (resp.windows ?? []) as Array<{ win: number; channel: number }>;
    expect(wins).toHaveLength(2);
    // With a shell attached the per-window video channels open and carry
    // the frame, so channel ids must be nonzero (unlike the headless run
    // above) — the shell-bound branch of the contract.
    for (const w of wins) {
      expect(w.channel).toBeGreaterThan(0);
    }

    // The built-in element mounts per window (window.tsx createElement).
    await page.waitForSelector('wash-app-display[data-wash-window]', { timeout: 10_000 });
    expect(await page.locator('wash-app-display').count()).toBeGreaterThanOrEqual(2);

    // Poll the canvas: the canned frame is a 1x1 opaque PNG behind the
    // 45-byte header, so after decode + drawImage the canvas is sized to
    // the frame (1x1) and its single pixel is fully opaque (a != 0).
    // createImageBitmap is async, hence the poll.
    await expect
      .poll(
        async () =>
          page.evaluate(() => {
            const el = document.querySelector('wash-app-display');
            if (!el) return null;
            const canvas = el.querySelector('canvas') as HTMLCanvasElement | null;
            if (!canvas || canvas.width === 0 || canvas.height === 0) return null;
            const ctx = canvas.getContext('2d');
            if (!ctx) return null;
            const { data } = ctx.getImageData(0, 0, 1, 1);
            return { w: canvas.width, h: canvas.height, a: data[3] };
          }),
        { timeout: 10_000 },
      )
      .toMatchObject({ w: 1, h: 1, a: 255 });
  });
});
