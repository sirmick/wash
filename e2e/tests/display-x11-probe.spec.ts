// X11 semantics the toolkit guests can't reach (REVIEW-DISPLAY-2026-09,
// docs/Review-findings-display.md): driven by tools/display-x11-probe.c, a
// dependency-free xcb client compiled here (needs cc + libxcb-dev, both
// already required to build the compositor's Xwayland bridge).
//
//   1. An UNTYPED override-redirect window (Steam toast / Wine helper / Java
//      popup shape) is overlaid WITHOUT grabbing the parent's pointer
//      (finding #3: the old "untyped ⇒ grab" default made the parent dead).
//   2. A _NET_WM_WINDOW_TYPE_MENU override-redirect with WM_TRANSIENT_FOR is a
//      grabbing menu parented through the transient-for chain (#7).
//   3. _NET_WM_STATE_FULLSCREEN is answered with a ConfigureNotify to the
//      screen size (#6: no request_fullscreen handler meant mpv/SDL believed
//      they were fullscreen at the old size).
// Also pins the Xwayland `ready` atom interning (#2).
//
// Needs the native compositor (out/wash-display). Not part of the default
// `make e2e` (no compositor in CI yet — docs/DISPLAY_E2E.md P0).
import { test, expect, displaySkipReason, type RouterHandle } from '../fixtures/router';
import { fileURLToPath } from 'node:url';
import { dirname, resolve, join } from 'node:path';
import { execSync } from 'node:child_process';
import { mkdtempSync, existsSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';

const __dirname = dirname(fileURLToPath(import.meta.url));
const SRC = resolve(__dirname, '..', '..', 'tools', 'display-x11-probe.c');

function buildProbe(): { bin: string; dir: string } | { skip: string } {
  try {
    execSync('pkg-config --exists xcb', { stdio: 'ignore' });
    execSync('command -v cc', { stdio: 'ignore', shell: '/bin/bash' });
  } catch {
    return { skip: 'cc + libxcb-dev not available' };
  }
  const dir = mkdtempSync(join(tmpdir(), 'x11probe-'));
  const bin = join(dir, 'x11probe');
  try {
    execSync(`cc -O1 ${SRC} -o ${bin} $(pkg-config --cflags --libs xcb)`, { stdio: 'pipe', shell: '/bin/bash' });
  } catch (e) {
    return { skip: `probe failed to compile: ${(e as Error).message}` };
  }
  return { bin, dir };
}

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

// Skip in a beforeEach (before the router fixture stages the compositor) —
// the display-skip-placement rule.
test.beforeEach(() => {
  const reason = displaySkipReason();
  test.skip(reason !== null, reason ?? '');
});

// Launch the probe from a wash terminal (it inherits DISPLAY via env.publish,
// DISPLAY_ENV.md) and wait for its toplevel to become a display window.
async function launchProbe(page: import('@playwright/test').Page, router: RouterHandle, cmd: string) {
  await page.goto(router.url);
  await router.waitForLog(/Xwayland ready on DISPLAY=/, 25_000);
  await router.waitForLog(/env\.publish from .*keys=.*WASH_X_DISPLAY/, 25_000);
  const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.term' });
  expect(launched.t).toBe('launched');
  await page.waitForSelector('wash-app-term .xterm-rows', { timeout: 10_000 });
  await expect(page.locator('wash-app-term')).toContainText(/\$|#|>/, { timeout: 10_000 });
  await page.locator('wash-app-term').click();
  await page.keyboard.type(`${cmd}\n`);
  const win = page.locator('wash-app-display[data-wash-window]').first();
  await win.waitFor({ state: 'visible', timeout: 25_000 });
  return win;
}

for (const mode of ['or-untyped', 'or-menu'] as const) {
  test(`override-redirect ${mode}: overlay ${mode === 'or-menu' ? 'with' : 'without'} a parent pointer grab`, async ({ page, router }) => {
    const built = buildProbe();
    if ('skip' in built) { test.skip(true, built.skip); return; }
    test.setTimeout(60_000);
    const log = join(built.dir, `${mode}.log`);
    await launchProbe(page, router, `${built.bin} ${mode} ${log}`);
    const line = await router.waitForLog(/wash-display: X11 popup mapped parent_win=\d+ chan=\d+ off=-?\d+,-?\d+ grab=[01]/, 10_000);
    expect(line).toContain(mode === 'or-menu' ? 'grab=1' : 'grab=0');
    // Both are drawn as an overlay canvas on the parent window.
    await page.locator('body > canvas[style*="fixed"]').first().waitFor({ state: 'attached', timeout: 10_000 });
    // The probe's own toplevel is the parent (transient-for for the menu; same
    // pid for the untyped one) — the window id in the log must be the probe's.
    const win = page.locator('wash-app-display[data-wash-window]').first();
    const winID = await win.evaluate((n) => Number(n.getAttribute('data-wash-window')));
    expect(line).toContain(`parent_win=${winID} `);
    // An untyped overlay must NOT redirect the parent's input: a click on the
    // parent canvas is injected into the parent window itself.
    if (mode === 'or-untyped') {
      await page.evaluate((w) => window.wash.focusWindow(w), winID);
      await win.click({ position: { x: 200, y: 140 } });
      await router.waitForLog(new RegExp(`wash-display: inject win=${winID} button left down @ 20\\d,1[34]\\d`), 10_000);
    }
    // The atom cache is (re)built on every Xwayland ready, not lazily on a
    // static connection that dies with a lazy-restarted X server (finding #2).
    await router.waitForLog(/wash-display: Xwayland ready, atoms interned \(4\)/, 5_000);
  });
}

test('_NET_WM_STATE_FULLSCREEN is answered with a screen-sized configure', async ({ page, router }) => {
  const built = buildProbe();
  if ('skip' in built) { test.skip(true, built.skip); return; }
  test.setTimeout(60_000);
  const log = join(built.dir, 'fullscreen.log');
  await launchProbe(page, router, `${built.bin} fullscreen ${log}`);
  // Behavioral proof first: the X client itself must receive a
  // ConfigureNotify LARGER than its own 240×160 request (an unhandled
  // request leaves it at 240×160 with _NET_WM_STATE claiming fullscreen).
  const bigConfigure = () => {
    const txt = existsSync(log) ? readFileSync(log, 'utf8') : '';
    for (const m of txt.matchAll(/XPROBE: configure (\d+)x(\d+)/g)) {
      if (Number(m[1]) > 240 && Number(m[2]) > 160) return `${m[1]}x${m[2]}`;
    }
    return '';
  };
  await expect.poll(bigConfigure, { timeout: 15_000 }).not.toBe('');
  // ...and it is the compositor's screen-size answer, mirrored to the wash
  // window state.
  const line = await router.waitForLog(/wash-display: X11 fullscreen win=\d+ on -> (\d+)x(\d+)/, 5_000);
  expect(line).toContain(`-> ${bigConfigure()}`);
});
