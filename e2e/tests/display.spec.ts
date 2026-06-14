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

  test('BE decodes a batched input app_msg (the §6 input contract)', async ({ router }) => {
    // The wash-display input contract (docs/DISPLAY.md §6): the FE batches
    // pointer/keyboard/scroll events into one app_msg {kind:"input", win,
    // events:[...]} per rAF, and the BE injects them into the focused
    // surface. Here we drive that app_msg straight through the control
    // socket (no compositor, no DOM) to lock the BE decode + per-event
    // logging — the FE→BE DOM-driven path is exercised once the
    // <wash-app-display> element captures input (M1). The router delivers
    // a cross-instance app_msg on the instance's primary window, so the BE
    // must route by the payload's "win", not the app_msg win.
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    expect(launched.t).toBe('launched');
    const inst = launched.instance_id as string;

    const opened = await router.sendAppMsg(inst, { kind: 'display_open', id: 'i1', n: 1 });
    const wins = (opened.windows ?? []) as Array<{ win: number }>;
    expect(wins).toHaveLength(1);
    const win = wins[0].win;

    await router.sendAppMsg(inst, {
      kind: 'input',
      win,
      events: [
        { ev: 'motion', x: 312, y: 88 },
        { ev: 'button', btn: 'left', state: 'down' },
        { ev: 'button', btn: 'left', state: 'up' },
        { ev: 'axis', axis: 'v', delta: -120 },
        { ev: 'key', code: 'KeyA', state: 'down' },
        { ev: 'key', code: 'KeyA', state: 'up' },
      ],
    });

    // Each event decodes to its own router-log line, keyed by the payload win.
    await router.waitForLog(new RegExp(`wash-test input win=${win} popup_chan=0 events=6`), 5_000);
    await router.waitForLog(new RegExp(`wash-test input win=${win} ev=motion x=312 y=88`), 5_000);
    await router.waitForLog(new RegExp(`wash-test input win=${win} ev=button btn=left state=down`), 5_000);
    await router.waitForLog(new RegExp(`wash-test input win=${win} ev=axis axis=v delta=-120`), 5_000);
    await router.waitForLog(new RegExp(`wash-test input win=${win} ev=key code=KeyA state=down`), 5_000);
  });

  test('browser: <wash-app-display> forwards real pointer/key/wheel input', async ({ page, router }) => {
    // The FE half of the §6 input contract: with a shell loaded, the
    // built-in element captures DOM pointer/keyboard/wheel events and
    // batches them to the owning instance as app_msg{kind:"input"}. We use
    // the test app's fake-display window (element wash-app-display) and
    // assert the test BE logged the decoded events — proving capture,
    // coalescing, coordinate mapping, and the FE→instance route end to end.
    await page.goto(router.url);

    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    expect(launched.t).toBe('launched');
    const inst = launched.instance_id as string;

    const resp = await router.sendAppMsg(inst, { kind: 'display_open', id: 'fi', n: 1 });
    const wins = (resp.windows ?? []) as Array<{ win: number }>;
    expect(wins).toHaveLength(1);
    const win = wins[0].win;

    const el = page.locator(`wash-app-display[data-wash-window="${win}"]`);
    await el.waitFor({ state: 'visible', timeout: 10_000 });

    // The test app also has its own primary window (element wash-app-test);
    // raise the display window so it's the topmost surface at the click
    // point (otherwise the primary window intercepts the pointer).
    await page.evaluate((w) => window.wash.focusWindow(w), win);

    // Pointer: hover then click. Playwright drives real pointer+mouse
    // events, so the element's pointermove/down/up listeners fire.
    await el.hover();
    await el.click();
    await router.waitForLog(new RegExp(`wash-test input win=${win} ev=motion`), 5_000);
    await router.waitForLog(new RegExp(`wash-test input win=${win} ev=button btn=left state=down`), 5_000);

    // Keyboard: the click focused the element; press a key → code KeyA.
    await page.keyboard.press('a');
    await router.waitForLog(new RegExp(`wash-test input win=${win} ev=key code=KeyA state=down`), 5_000);

    // Wheel: a vertical scroll over the canvas → an axis event.
    await el.hover();
    await page.mouse.wheel(0, 120);
    // (the element is already raised + focused from the click above)
    await router.waitForLog(new RegExp(`wash-test input win=${win} ev=axis axis=v`), 5_000);
  });

  test('browser: a popup renders as a positioned overlay + forwards input (M3)', async ({ page, router }) => {
    // §12 M3: a child surface (menu/dropdown) of a display window streams
    // over a "video-popup" channel and the parent's <wash-app-display>
    // draws it as a fixed-position overlay canvas that can overflow the
    // window. Clicking the overlay forwards input keyed by the popup
    // channel (popups have no wash win). Driven via the test app's
    // fake-display popup mode — no compositor.
    await page.goto(router.url);
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    const inst = launched.instance_id as string;

    const resp = await router.sendAppMsg(inst, { kind: 'display_open', id: 'p1', n: 1 });
    const win = ((resp.windows ?? []) as Array<{ win: number }>)[0].win;
    await page.locator(`wash-app-display[data-wash-window="${win}"]`).waitFor({ state: 'visible' });

    // Open a popup at offset (40,50) on that window.
    const opened = await router.sendAppMsg(inst, { kind: 'popup_open', id: 'pop', win, x: 40, y: 50 });
    const chan = opened.channel as number;
    expect(chan).toBeGreaterThan(0);

    // A fixed-position overlay canvas appears on <body> (not inside the
    // window — so it can overflow), positioned near parent-origin + offset.
    const overlay = page.locator('body > canvas[style*="fixed"]');
    await overlay.first().waitFor({ state: 'attached', timeout: 10_000 });
    expect(await overlay.count()).toBeGreaterThanOrEqual(1);

    // Click the overlay → input is forwarded keyed by the popup channel,
    // not a win (the BE logs popup_chan=<chan>).
    // force: the canned popup frame is 1×1, too small for the actionability
    // hit-test; we only care that the click reaches the overlay's listeners.
    await overlay.first().click({ force: true });
    await router.waitForLog(new RegExp(`wash-test input win=0 popup_chan=${chan} `), 5_000);
    await router.waitForLog(new RegExp(`popup_chan=${chan} events=`), 5_000);
  });

  test('clipboard bridges between instances (the §7 clipboard contract)', async ({ router }) => {
    // wash's clipboard is eager + router-held: clipboard.set broadcasts
    // clipboard.changed to every OTHER app, and clipboard.get returns the
    // bytes. wash-display will bridge this to the Wayland/X11 selection
    // (M2); here we prove the wire vocabulary with two test instances so a
    // later display copy/paste rides a known-good path. The setter is
    // excluded from its own broadcast, hence two instances.
    const a = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    const b = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    const instA = a.instance_id as string;
    const instB = b.instance_id as string;
    expect(instA).not.toBe(instB);

    // A sets the clipboard; B (a different instance) sees clipboard.changed.
    await router.sendAppMsg(instA, { kind: 'clipboard_set', mime: 'text/plain', text: 'cl-12345' });
    await router.waitForLog(/wash-test clipboard\.changed mime=text\/plain/, 5_000);

    // B reads it back; the reply (clipboard_get_ok) echoes the request id so
    // the control socket correlates it, and carries the round-tripped bytes.
    const got = await router.sendAppMsg(instB, { kind: 'clipboard_get', id: 'cg1' });
    expect(got.type).toBe('clipboard_get_ok');
    expect(got.mime).toBe('text/plain');
    expect(got.text).toBe('cl-12345');
  });
});
