// Real-Qt capstone for the menu-fallback popover path (DISPLAY.md §12).
//
// Qt maps a menu opened WITHOUT an input serial (e.g. programmatically) as a
// parented, untitled xdg_toplevel rather than an xdg_popup. wash-display must
// route that through the popup-overlay path (anchored + grabbing on the
// parent's win), not mint a standalone wash window — otherwise the menu shows
// as a wrong-sized, non-grabbing window ("Qt popovers wrong size & no input").
// A titled QDialog is the control and must stay a real wash window.
//
// Drives a real Qt6 widget app (tools/display-qt-popover.cpp, compiled here).
// Needs the native compositor (out/wash-display) + Qt6 Widgets dev (g++ +
// pkg-config Qt6Widgets). Skipped otherwise — not part of the default e2e
// (no compositor/Qt in CI). The xdg_popup path (real-click menus, GTK menus)
// is covered by display-guest.spec.ts.
import { test, expect, displaySkipReason } from '../fixtures/router';
import { fileURLToPath } from 'node:url';
import { dirname, resolve, join } from 'node:path';
import { execSync } from 'node:child_process';
import { existsSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';

const __dirname = dirname(fileURLToPath(import.meta.url));
const SRC = resolve(__dirname, '..', '..', 'tools', 'display-qt-popover.cpp');

function buildGuest(): string | { skip: string } {
  try {
    execSync('pkg-config --exists Qt6Widgets', { stdio: 'ignore' });
    execSync('command -v g++', { stdio: 'ignore', shell: '/bin/bash' });
  } catch {
    return { skip: 'Qt6 Widgets dev (g++ + pkg-config Qt6Widgets) not available' };
  }
  const out = join(mkdtempSync(join(tmpdir(), 'qtpopover-')), 'qtpopover');
  try {
    execSync(
      `g++ -std=c++17 ${SRC} -o ${out} $(pkg-config --cflags --libs Qt6Widgets) -fPIC`,
      { stdio: 'pipe', shell: '/bin/bash' },
    );
  } catch (e) {
    return { skip: `Qt guest failed to compile: ${(e as Error).message}` };
  }
  return out;
}

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

// Skip BEFORE the router fixture is set up — it stages the wash-display binary
// and throws if absent (router.ts stageApps). CI builds no compositor, so this
// must short-circuit in a beforeEach: a test.skip() inside the body runs only
// after the router fixture has already thrown. Mirrors display-input-smoke.
test.beforeEach(() => {
  const dreason = displaySkipReason();
  test.skip(dreason !== null, dreason ?? '');
});

test('Qt programmatic menu → popover overlay; dialog → window', async ({ page, router }) => {
  const built = buildGuest();
  if (typeof built !== 'string') { test.skip(true, built.skip); return; }
  const guest = built;
  test.setTimeout(90_000);

  await page.goto(router.url);
  // Wait for the compositor to be fully up and to have published its display
  // env BEFORE launching the terminal — the shell reads spawnEnv at spawn time
  // (DISPLAY_ENV.md §5), so a Qt guest started too early sees no WAYLAND_DISPLAY
  // and never maps (mirrors display-guest.spec.ts / display-term-xclock).
  await router.waitForLog(/Xwayland ready on DISPLAY=/, 25_000);
  await router.waitForLog(/env\.publish from /, 25_000);
  const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.term' });
  expect(launched.t).toBe('launched');
  await page.waitForSelector('wash-app-term .xterm-rows', { timeout: 10_000 });
  await page.locator('wash-app-term').click();
  await page.keyboard.type(`QT_QPA_PLATFORM=wayland ${guest}\n`);

  const win = page.locator('wash-app-display[data-wash-window]').first();
  await win.waitFor({ state: 'visible', timeout: 25_000 });
  const winID = await win.evaluate((n) => Number(n.getAttribute('data-wash-window')));
  await page.evaluate((w) => window.wash.focusWindow(w), winID);

  const box = (await win.boundingBox())!;
  // Park the pointer so the (positioner-less) programmatic menu anchors here.
  await page.mouse.move(box.x + 120, box.y + 120);

  const dispWins = () => page.locator('wash-app-display[data-wash-window]').count();
  const overlays = () => page.locator('body > canvas[style*="fixed"]').count();

  // t=3s: programmatic menu. It must become a popover overlay, NOT a window.
  await router.waitForLog(/wash-display: menu toplevel as popover/, 12_000);
  await expect.poll(overlays, { timeout: 5_000 }).toBeGreaterThan(0);
  expect(await dispWins(), 'menu must not mint a 2nd wash window').toBe(1);

  // Clicking the overlay forwards input to the menu surface (popup_chan path).
  await page.locator('body > canvas[style*="fixed"]').first().click({ force: true });
  await router.waitForLog(/wash-display: inject win=\d+ button left/, 8_000);

  // t=6s: the titled dialog MUST be a real wash window (2nd display window).
  await expect.poll(dispWins, { timeout: 12_000 }).toBe(2);
});
