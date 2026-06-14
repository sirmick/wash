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
import { mkdirSync, writeFileSync, existsSync } from 'node:fs';
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
