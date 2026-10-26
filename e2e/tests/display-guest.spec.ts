// Real-app capstone for the interactive features (DISPLAY.md §12), driven
// against a genuine GTK3 client (tools/display-testguest.py — PyGObject,
// preinstalled, no build). One script covers BOTH popup paths via
// GDK_BACKEND: "wayland" → xdg_popup menus (M3a); "x11" → Xwayland
// override-redirect menus (M3b). Unlike the faked contract tests
// (display.spec.ts), this exercises the compositor end to end with a real
// toolkit's menus and clipboard.
//
// Assertions ride the compositor's own log lines (which share the router
// log), so they don't depend on reading streamed pixels:
//   - right-click → "popup mapped" / "X11 popup mapped" (the menu surface)
//   - click the overlay → "inject … popup_chan=" (popup input)
//   - press 'c' → "clipboard guest->wash" (a real GTK copy bridged to wash)
//
// Needs the native compositor (out/wash-display) + python3-gi. Not in the
// default `make e2e`.
import { test, expect, displaySkipReason } from '../fixtures/router';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { execSync } from 'node:child_process';

const __dirname = dirname(fileURLToPath(import.meta.url));
const GUEST = resolve(__dirname, '..', '..', 'tools', 'display-testguest.py');

function pyGtkMissing(): string | null {
  try {
    execSync('python3 -c "import gi; gi.require_version(\'Gtk\',\'3.0\')"', { stdio: 'ignore' });
    return null;
  } catch {
    return 'python3 GTK3 (PyGObject) not available';
  }
}

test.use({ routerOpts: { apps: ['session', 'term', 'display', 'test'], showHidden: true } });

for (const backend of ['wayland', 'x11'] as const) {
  test.describe(`GTK guest (${backend})`, () => {
    test.beforeEach(() => {
      const reason = displaySkipReason() ?? pyGtkMissing();
      test.skip(reason !== null, reason ?? '');
    });

    test(`real menu + clipboard via ${backend}`, async ({ page, router }) => {
      test.setTimeout(60_000);
      // "X11 popup mapped" for x11; "popup mapped" (xdg) for wayland.
      const popupLog = backend === 'x11' ? /wash-display: X11 popup mapped/ : /wash-display: popup mapped/;

      await page.goto(router.url);
      await router.waitForLog(/Xwayland ready on DISPLAY=/, 25_000);
      // Wait for the publish that actually CARRIES the display env, not for
      // "an env.publish happened". The compositor publishes more than once
      // (geometry early, socket names once Xwayland is up) and waitForLog
      // scans from t=0, so the bare event was satisfied by the earlier publish
      // and the terminal was spawned before spawnEnv had the display —
      // docs/FLAKE_LOG.md 2026-07-29, TEST_FLAKES.md A10. WASH_X_DISPLAY sorts
      // last in the line, so matching it means the whole publish landed.
      await router.waitForLog(/env\.publish from .*keys=.*WASH_X_DISPLAY/, 25_000);

      const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.term' });
      expect(launched.t).toBe('launched');
      await page.waitForSelector('wash-app-term .xterm-rows', { timeout: 10_000 });
      // xterm mounted != shell ready — without this the keystrokes below can be
      // typed into a pty that has not drawn its prompt yet, and the first ones
      // are swallowed (docs/TEST_FLAKES.md C5).
      await expect(page.locator('wash-app-term')).toContainText(/\$|#|>/, { timeout: 10_000 });
      await page.locator('wash-app-term').click();

      // Launch the guest connected to the compositor (the terminal's shell
      // already carries DISPLAY/WAYLAND_DISPLAY via env.publish).
      await page.keyboard.type(`GDK_BACKEND=${backend} python3 ${GUEST}\n`);

      // It maps as a display window.
      const win = page.locator('wash-app-display[data-wash-window]').first();
      await win.waitFor({ state: 'visible', timeout: 25_000 });
      const winID = await win.evaluate((n) => Number(n.getAttribute('data-wash-window')));
      await page.evaluate((w) => window.wash.focusWindow(w), winID);

      // Right-click in the lower (event-box) area → a real context menu.
      // A pointer-triggered popup carries the grab serial Wayland needs.
      await win.click({ button: 'right', position: { x: 120, y: 230 } });
      await router.waitForLog(popupLog, 15_000);

      // The menu shows as an overlay canvas on <body>; clicking it forwards
      // input keyed by the popup channel (proves popup input to a real menu).
      const overlay = page.locator('body > canvas[style*="fixed"]').first();
      await overlay.waitFor({ state: 'attached', timeout: 10_000 });
      await overlay.click({ force: true });
      await router.waitForLog(/wash-display: inject win=\d+ button left/, 10_000);

      // Press 'c' → the guest copies its sentinel to the toolkit clipboard,
      // which the compositor bridges into wash's clipboard. Assert the
      // "stored N bytes" line, not the "mime=" one: the latter is logged
      // BEFORE the bytes are read, so it passed while the X11 leg was
      // silently broken by a double-closed pipe fd (the compositor closed
      // the write end wlroots' xwm was about to write to —
      // docs/Review-findings-display.md #1). Both backends now prove the
      // full transfer: the 21-byte sentinel must arrive intact.
      await win.click(); // refocus the window (the menu took the pointer)
      await page.keyboard.press('c');
      await router.waitForLog(/wash-display: clipboard guest->wash mime=/, 10_000);
      // Behavioral proof: read wash's clipboard back through a hidden test-app
      // instance (the same clipboard_get the §7 contract test uses) and
      // compare the bytes — the guest's SENTINEL must have arrived intact.
      const reader = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
      const readerInst = reader.instance_id as string;
      await expect
        .poll(async () => {
          const got = await router.sendAppMsg(readerInst, { kind: 'clipboard_get', id: `cg-${backend}` });
          return got.type === 'clipboard_get_ok' ? String(got.text) : '';
        }, { timeout: 10_000 })
        .toBe('wash-clip-sentinel-42');
      await router.waitForLog(/wash-display: clipboard guest->wash stored 21 bytes mime=text\/plain/, 10_000);
    });
  });
}
