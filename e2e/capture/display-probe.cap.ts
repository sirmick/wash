// Display compositor probe — fires up real X11/Wayland apps over the native
// wash-display compositor, screenshots each as a wash window, and dumps the
// relevant router/compositor log lines next to each shot as evidence.
//
// NOT marketing capture — this is the manual test surface for the
// wash-display-io branch. Output lands in tmp/display-shots/ (gitignored):
//   <name>.win.png   the wash-app-display window only
//   <name>.full.png  the whole desktop
//   <name>.log       compositor/router log slice (window.create, frames, input…)
//
// Run with the screenshots config (single worker, roomy viewport):
//   cd e2e && npx playwright test -c playwright.screenshots.config.ts \
//             capture/display-probe.cap.ts
//
// Output lands in <repo-root>/screenshots/ for inspection.
//
// Each test boots a throwaway router; the compositor (surface:background)
// autostarts on shell connect. We wait for Xwayland + env.publish BEFORE
// launching the terminal so the shell inherits DISPLAY/WAYLAND_DISPLAY
// (DISPLAY_ENV.md §5).

import { test, expect, displaySkipReason } from '../fixtures/router';
import type { Page, Locator } from '@playwright/test';
import { mkdirSync, writeFileSync, existsSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const SHOTS = resolve(__dirname, '..', '..', 'screenshots');
mkdirSync(SHOTS, { recursive: true });
const GUEST = resolve(__dirname, '..', '..', 'tools', 'display-testguest.py');

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

// ---- helpers ---------------------------------------------------------------
async function bootDesktop(page: Page, url: string): Promise<void> {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
}
function win(page: Page, el: string): Locator {
  return page.locator('.wash-window', { has: page.locator(el) });
}
const settle = (page: Page, ms = 1000) => page.waitForTimeout(ms);

// Bring the compositor up and open a focused terminal we can type into.
async function bootWithTerminal(page: Page, router: any): Promise<void> {
  await bootDesktop(page, router.url);
  await router.waitForLog(/Xwayland ready on DISPLAY=/, 25_000);
  await router.waitForLog(/env\.publish from /, 25_000);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu-com.wash.term"]').click();
  await expect(win(page, 'wash-app-term')).toBeVisible();
  await page.waitForSelector('wash-app-term .xterm-rows', { timeout: 10_000 });
  await page.locator('wash-app-term').click();
  await page.keyboard.type('clear\n', { delay: 8 });
  await settle(page, 300);
}

// Derive delivered fps from the throttled compositor log: "frame seq=N" lines
// are emitted every 60 frames with a HH:MM:SS.mmm timestamp, so the seq delta
// over the time delta between the first and last such line is the frame rate.
function fpsFromLog(log: string): number {
  const re = /(\d\d):(\d\d):(\d\d)\.(\d\d\d).*frame seq=(\d+)/g;
  const pts: { t: number; seq: number }[] = [];
  let m: RegExpExecArray | null;
  while ((m = re.exec(log))) {
    pts.push({ t: +m[1] * 3600 + +m[2] * 60 + +m[3] + +m[4] / 1000, seq: +m[5] });
  }
  if (pts.length < 2) return 0;
  const a = pts[0], b = pts[pts.length - 1];
  return b.t > a.t ? Math.round((b.seq - a.seq) / (b.t - a.t)) : 0;
}

// Dump the log lines that matter for the display feature to <name>.log.
function dumpLog(router: any, name: string): void {
  const lines = router.log().split('\n').filter((l: string) =>
    /window\.create|window\.created|wash-display:|popup|inject|clipboard|frame|Xwayland|env\.publish|cursor/.test(l),
  );
  writeFileSync(join(SHOTS, `${name}.log`), lines.join('\n') + '\n');
}

// In-page: does the display window's <canvas> actually carry varied pixels,
// or is it blank (the wlroots-0.17.1 readback risk)? Returns a small summary.
async function canvasStats(el: Locator): Promise<{ found: boolean; w: number; h: number; uniqueColors: number; nonBlankPct: number }> {
  return el.evaluate((node) => {
    const root = (node as HTMLElement).shadowRoot ?? node;
    const c = (root.querySelector('canvas') ?? (node as HTMLElement).querySelector('canvas')) as HTMLCanvasElement | null;
    if (!c) return { found: false, w: 0, h: 0, uniqueColors: 0, nonBlankPct: 0 };
    const ctx = c.getContext('2d');
    if (!ctx) return { found: true, w: c.width, h: c.height, uniqueColors: 0, nonBlankPct: 0 };
    const { width: w, height: h } = c;
    if (!w || !h) return { found: true, w, h, uniqueColors: 0, nonBlankPct: 0 };
    const data = ctx.getImageData(0, 0, w, h).data;
    const seen = new Set<number>();
    let nonBlank = 0;
    const total = w * h;
    const step = Math.max(1, Math.floor(total / 4000)); // sample ~4k px
    let sampled = 0;
    for (let i = 0; i < total; i += step) {
      const p = i * 4;
      const r = data[p], g = data[p + 1], b = data[p + 2], a = data[p + 3];
      seen.add((r << 24) | (g << 16) | (b << 8) | a);
      if (!(r === 0 && g === 0 && b === 0) && a !== 0) nonBlank++;
      sampled++;
    }
    return { found: true, w, h, uniqueColors: seen.size, nonBlankPct: Math.round((nonBlank / sampled) * 100) };
  });
}

// Launch a GUI command in the terminal, wait for its window, screenshot it.
async function captureApp(page: Page, router: any, name: string, cmd: string, opts: { timeout?: number } = {}): Promise<void> {
  await page.keyboard.type(cmd + '\n', { delay: 8 });
  await router.waitForLog(/window\.create .*element="wash-app-display"/, opts.timeout ?? 20_000);
  const display = win(page, 'wash-app-display').first();
  await expect(display).toBeVisible({ timeout: 20_000 });
  await settle(page, opts.timeout ? 2500 : 1500);
  const stats = await canvasStats(display).catch(() => null);
  await display.screenshot({ path: join(SHOTS, `${name}.win.png`) });
  await page.screenshot({ path: join(SHOTS, `${name}.full.png`) });
  dumpLog(router, name);
  // Surface the blank-frame verdict in the test log + a sidecar file.
  const verdict = stats ? `canvas ${stats.w}x${stats.h} uniqueColors=${stats.uniqueColors} nonBlank=${stats.nonBlankPct}%` : 'canvas: (probe failed)';
  writeFileSync(join(SHOTS, `${name}.verdict.txt`), verdict + '\n');
  console.log(`[${name}] ${verdict}`);
}

// ---- the probes ------------------------------------------------------------
const skip = () => {
  const reason = displaySkipReason();
  test.skip(reason !== null, reason ?? '');
};

test.describe('display-probe', () => {
  test.beforeEach(skip);

  test('xclock (X11)', async ({ page, router }) => {
    test.setTimeout(60_000);
    if (!existsSync('/usr/bin/xclock')) test.skip(true, 'xclock missing');
    await bootWithTerminal(page, router);
    await captureApp(page, router, 'xclock', 'xclock');
  });

  test('xeyes (X11)', async ({ page, router }) => {
    test.setTimeout(60_000);
    if (!existsSync('/usr/bin/xeyes')) test.skip(true, 'xeyes missing');
    await bootWithTerminal(page, router);
    await captureApp(page, router, 'xeyes', 'xeyes');
  });

  test('xlogo (X11)', async ({ page, router }) => {
    test.setTimeout(60_000);
    if (!existsSync('/usr/bin/xlogo')) test.skip(true, 'xlogo missing');
    await bootWithTerminal(page, router);
    await captureApp(page, router, 'xlogo', 'xlogo');
  });

  test('gnome-calculator (GTK/wayland)', async ({ page, router }) => {
    test.setTimeout(60_000);
    if (!existsSync('/usr/bin/gnome-calculator')) test.skip(true, 'gnome-calculator missing');
    await bootWithTerminal(page, router);
    await captureApp(page, router, 'gnome-calculator', 'gnome-calculator', { timeout: 30_000 });
  });

  // Dynamic check: damage tracking only matters for UPDATES (the first frame
  // is always full). Type into the calculator and confirm the typed digits +
  // result land via partial frames on the persistent FE canvas.
  test('calc-dynamic (incremental damage)', async ({ page, router }) => {
    test.setTimeout(60_000);
    if (!existsSync('/usr/bin/gnome-calculator')) test.skip(true, 'gnome-calculator missing');
    await bootWithTerminal(page, router);
    await page.keyboard.type('gnome-calculator\n', { delay: 8 });
    await router.waitForLog(/window\.create .*element="wash-app-display"/, 30_000);
    const display = win(page, 'wash-app-display').first();
    await expect(display).toBeVisible({ timeout: 20_000 });
    await settle(page, 2000);
    const w = await display.evaluate((n) => Number(n.getAttribute('data-wash-window')));
    await page.evaluate((id) => (window as any).wash.focusWindow(id), w);
    await display.click({ position: { x: 180, y: 250 } }); // focus the calc surface
    await settle(page, 500);
    await page.keyboard.type('1337*2', { delay: 120 });
    await settle(page, 800);
    await page.keyboard.press('Enter'); // 1337*2 = 2674
    await settle(page, 1500);
    await display.screenshot({ path: join(SHOTS, 'calc-dynamic.win.png') });
    dumpLog(router, 'calc-dynamic');
  });

  test('testguest — wayland (xdg_popup)', async ({ page, router }) => {
    test.setTimeout(60_000);
    await bootWithTerminal(page, router);
    await captureApp(page, router, 'guest-wayland', `GDK_BACKEND=wayland python3 ${GUEST}`);
    // right-click → xdg_popup overlay; screenshot it.
    const display = win(page, 'wash-app-display').first();
    const w = await display.evaluate((n) => Number(n.getAttribute('data-wash-window')));
    await page.evaluate((id) => (window as any).wash.focusWindow(id), w);
    // Right-click in the LOWER event-box area ("right-click here for a menu")
    // — a pointer-triggered popup carries the grab serial Wayland needs.
    await display.click({ button: 'right', position: { x: 120, y: 230 } });
    await router.waitForLog(/wash-display: popup mapped/, 15_000);
    await page.locator('body > canvas[style*="fixed"]').first().waitFor({ state: 'attached', timeout: 10_000 });
    await settle(page, 800);
    await page.screenshot({ path: join(SHOTS, 'guest-wayland.popup.png') });
    // copy → clipboard guest->wash (Wayland leg is ours)
    await display.click();
    await page.keyboard.press('c');
    await router.waitForLog(/clipboard guest->wash/, 8_000);
    dumpLog(router, 'guest-wayland');
  });

  test('testguest — x11 (override-redirect)', async ({ page, router }) => {
    test.setTimeout(60_000);
    await bootWithTerminal(page, router);
    await captureApp(page, router, 'guest-x11', `GDK_BACKEND=x11 python3 ${GUEST}`);
    const display = win(page, 'wash-app-display').first();
    const w = await display.evaluate((n) => Number(n.getAttribute('data-wash-window')));
    await page.evaluate((id) => (window as any).wash.focusWindow(id), w);
    await display.click({ button: 'right', position: { x: 120, y: 230 } });
    await router.waitForLog(/wash-display: X11 popup mapped/, 15_000);
    await page.locator('body > canvas[style*="fixed"]').first().waitFor({ state: 'attached', timeout: 10_000 });
    await settle(page, 800);
    await page.screenshot({ path: join(SHOTS, 'guest-x11.popup.png') });
    // NB: the X11 guest->wash copy leg rides wlroots' lazy xwm selection sync,
    // so (per display-guest.spec.ts) we don't gate the x11 variant on it.
    dumpLog(router, 'guest-x11');
  });

  // Firefox is the subsurface stress test: it renders web content into
  // wl_subsurfaces, so the wayland path exposes whether our single-root-
  // surface capture sees subsurface content. The x11 path (Xwayland presents
  // ONE composited buffer per window) is the control / workaround.
  const FF = ['/opt/firefox/firefox', '/usr/bin/firefox-bin'].find((p) => existsSync(p));
  for (const [name, prefix] of [
    ['firefox-wayland', 'MOZ_ENABLE_WAYLAND=1'],
    ['firefox-x11', 'MOZ_ENABLE_WAYLAND=0 GDK_BACKEND=x11'],
  ] as const) {
    test(name, async ({ page, router }) => {
      test.setTimeout(120_000);
      if (!FF) test.skip(true, 'native firefox not found (/opt/firefox/firefox)');
      await bootWithTerminal(page, router);
      // Fresh profile + -no-remote so FF maps its own toplevel. about:robots
      // renders offline.
      const prof = mkdtempSync(join(tmpdir(), 'ffprof-'));
      await page.keyboard.type(
        `${prefix} ${FF} -no-remote -profile ${prof} about:robots\n`, { delay: 8 });
      await router.waitForLog(/window\.create .*element="wash-app-display"/, 45_000);
      const display = win(page, 'wash-app-display').first();
      await expect(display).toBeVisible({ timeout: 20_000 });
      // Poll for real content (FF cold-starts slowly): distinguishes EVER
      // paints (timing) from never (subsurface-capture gap).
      let stats: any = null;
      const t0 = Date.now();
      for (let i = 0; i < 40; i++) {
        await settle(page, 1000);
        stats = await canvasStats(display).catch(() => null);
        if (stats && stats.nonBlankPct > 5) break;
      }
      const secs = Math.round((Date.now() - t0) / 1000);
      await display.screenshot({ path: join(SHOTS, `${name}.win.png`) });
      await page.screenshot({ path: join(SHOTS, `${name}.full.png`) });
      dumpLog(router, name);
      const verdict = stats
        ? `canvas ${stats.w}x${stats.h} uniqueColors=${stats.uniqueColors} nonBlank=${stats.nonBlankPct}% (after ${secs}s)`
        : 'canvas: (probe failed)';
      writeFileSync(join(SHOTS, `${name}.verdict.txt`), verdict + '\n');
      console.log(`[${name}] ${verdict}`);
    });
  }

  // Video throughput: a 30fps clip stresses the damage→readback→WebP path
  // (each video frame damages the whole video region). gst-play uses
  // glimagesink (GL into the window). We let it play, screenshot a frame, and
  // derive the delivered frame rate from the throttled per-60-frame log.
  // Generate the clip (gitignored) before running these:
  //   ffmpeg -f lavfi -i testsrc2=size=640x480:rate=30:duration=12 \
  //          -pix_fmt yuv420p tmp/washtest.mp4
  const VIDEO = resolve(__dirname, '..', '..', 'tmp', 'washtest.mp4');
  test('gst-video (wayland throughput)', async ({ page, router }) => {
    test.setTimeout(60_000);
    if (!existsSync(VIDEO)) test.skip(true, 'tmp/washtest.mp4 not generated');
    await bootWithTerminal(page, router);
    await page.keyboard.type(`gst-play-1.0 ${VIDEO}\n`, { delay: 8 });
    await router.waitForLog(/window\.create .*element="wash-app-display"/, 25_000);
    const display = win(page, 'wash-app-display').first();
    await expect(display).toBeVisible({ timeout: 20_000 });
    await settle(page, 4000); // let it play
    const stats = await canvasStats(display).catch(() => null);
    await display.screenshot({ path: join(SHOTS, 'gst-video.win.png') });
    dumpLog(router, 'gst-video');
    // Derive fps: consecutive "frame seq=N" log lines are 60 frames apart.
    const fps = fpsFromLog(router.log());
    const verdict = `${stats ? `canvas ${stats.w}x${stats.h} nonBlank=${stats.nonBlankPct}%` : 'no canvas'} delivered≈${fps}fps`;
    writeFileSync(join(SHOTS, 'gst-video.verdict.txt'), verdict + '\n');
    console.log(`[gst-video] ${verdict}`);
  });

  test('firefox-video (subsurface video)', async ({ page, router }) => {
    test.setTimeout(90_000);
    const FFbin = ['/opt/firefox/firefox', '/usr/bin/firefox-bin'].find((p) => existsSync(p));
    if (!FFbin || !existsSync(VIDEO)) test.skip(true, 'firefox or test video missing');
    await bootWithTerminal(page, router);
    const prof = mkdtempSync(join(tmpdir(), 'ffprof-'));
    await page.keyboard.type(
      `MOZ_ENABLE_WAYLAND=1 ${FFbin} -no-remote -profile ${prof} file://${VIDEO}\n`, { delay: 8 });
    await router.waitForLog(/window\.create .*element="wash-app-display"/, 45_000);
    const display = win(page, 'wash-app-display').first();
    await expect(display).toBeVisible({ timeout: 20_000 });
    await settle(page, 6000); // FF cold start + autoplay
    const stats = await canvasStats(display).catch(() => null);
    await display.screenshot({ path: join(SHOTS, 'firefox-video.win.png') });
    dumpLog(router, 'firefox-video');
    const fps = fpsFromLog(router.log());
    const verdict = `${stats ? `canvas ${stats.w}x${stats.h} nonBlank=${stats.nonBlankPct}%` : 'no canvas'} delivered≈${fps}fps`;
    writeFileSync(join(SHOTS, 'firefox-video.verdict.txt'), verdict + '\n');
    console.log(`[firefox-video] ${verdict}`);
  });

  // Chromium (snap on this box) — a second browser engine to confirm the
  // subsurface path isn't Firefox-specific. Snap confinement may block the
  // compositor socket; if no window maps we learn that.
  test('chromium (wayland)', async ({ page, router }) => {
    test.setTimeout(90_000);
    if (existsSync('/usr/bin/snap') === false) test.skip(true, 'no chromium');
    await bootWithTerminal(page, router);
    const prof = mkdtempSync(join(tmpdir(), 'chr-'));
    await page.keyboard.type(
      `chromium --ozone-platform=wayland --no-first-run --user-data-dir=${prof} about:blank\n`,
      { delay: 8 });
    let mapped = true;
    await router.waitForLog(/window\.create .*element="wash-app-display"/, 40_000).catch(() => { mapped = false; });
    if (!mapped) {
      dumpLog(router, 'chromium');
      writeFileSync(join(SHOTS, 'chromium.verdict.txt'), 'no window mapped (snap sandbox?)\n');
      console.log('[chromium] no window mapped — likely snap confinement');
      return;
    }
    const display = win(page, 'wash-app-display').first();
    await expect(display).toBeVisible({ timeout: 20_000 });
    await settle(page, 5000);
    const stats = await canvasStats(display).catch(() => null);
    await display.screenshot({ path: join(SHOTS, 'chromium.win.png') });
    await page.screenshot({ path: join(SHOTS, 'chromium.full.png') });
    dumpLog(router, 'chromium');
    const verdict = stats ? `canvas ${stats.w}x${stats.h} nonBlank=${stats.nonBlankPct}%` : 'no canvas';
    writeFileSync(join(SHOTS, 'chromium.verdict.txt'), verdict + '\n');
    console.log(`[chromium] ${verdict}`);
  });

  // M8 move bridge: dragging a chromeless guest's OWN titlebar should move
  // the wash window. The drag is injected → guest requests xdg_toplevel.move
  // → compositor relays {move:true} → wash-app-display drives the wash window.
  test('csd-move (drag titlebar moves window)', async ({ page, router }) => {
    test.setTimeout(60_000);
    if (!existsSync('/usr/bin/gnome-calculator')) test.skip(true, 'no calc');
    await bootWithTerminal(page, router);
    await page.keyboard.type('gnome-calculator\n', { delay: 8 });
    await router.waitForLog(/window\.create .*element="wash-app-display"/, 30_000);
    const winEl = win(page, 'wash-app-display').first();
    await expect(winEl).toBeVisible({ timeout: 20_000 });
    await settle(page, 2500);
    const id = await winEl.locator('wash-app-display').evaluate((n) => Number(n.getAttribute('data-wash-window')));
    await page.evaluate((w) => (window as any).wash.focusWindow(w), id);
    const before = (await winEl.boundingBox())!;
    // Grab the libadwaita headerbar in clearly-empty space (between the Undo
    // button on the left and the Basic dropdown in the centre).
    const sx = before.x + 120, sy = before.y + 22;
    await page.mouse.move(sx, sy);
    await page.mouse.down();
    await page.mouse.move(sx + 160, sy + 100, { steps: 14 });
    await page.mouse.up();
    await settle(page, 700);
    const after = (await winEl.boundingBox())!;
    dumpLog(router, 'csd-move');
    await page.screenshot({ path: join(SHOTS, 'csd-move.full.png') });
    const moved = Math.abs(after.x - before.x) + Math.abs(after.y - before.y);
    console.log(`[csd-move] before=(${Math.round(before.x)},${Math.round(before.y)}) after=(${Math.round(after.x)},${Math.round(after.y)}) moved=${Math.round(moved)}px`);
    expect(moved).toBeGreaterThan(40);
  });

  test('montage — three X11 apps at once', async ({ page, router }) => {
    test.setTimeout(60_000);
    await bootWithTerminal(page, router);
    for (const cmd of ['xclock -update 1 &', 'xeyes &', 'xlogo &']) {
      await page.keyboard.type(cmd + '\n', { delay: 8 });
      await settle(page, 400);
    }
    // Wait until three display windows have mapped.
    await expect.poll(async () => win(page, 'wash-app-display').count(), { timeout: 30_000 }).toBeGreaterThanOrEqual(3);
    await settle(page, 2000);
    await page.screenshot({ path: join(SHOTS, 'montage.full.png') });
    dumpLog(router, 'montage');
    console.log(`[montage] display windows = ${await win(page, 'wash-app-display').count()}`);
  });
});
