// Real end-to-end input injection (DISPLAY.md §6): launch an X client
// (xclock) so it maps as a <wash-app-display> window, then click + type ON
// THAT window's canvas and assert the native compositor injected the event
// into the real wlroots surface. This is the M1 capstone — it exercises the
// full FE-capture → app_msg → seat-injection path against a real X client,
// which the CI-faked contract test (display.spec.ts) cannot.
//
// Needs the native compositor (out/wash-display, make WASH_DISPLAY=1) and
// /usr/bin/xclock. Not part of the default `make e2e` (no compositor in CI).
import { test, expect, displaySkipReason } from '../fixtures/router';
import { existsSync } from 'node:fs';

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

test.beforeEach(() => {
  const reason = displaySkipReason() ?? (existsSync('/usr/bin/xclock') ? null : '/usr/bin/xclock not installed');
  test.skip(reason !== null, reason ?? '');
});

test('click + type on a real X window are injected by the compositor', async ({ page, router }) => {
  test.setTimeout(60_000);
  await page.goto(router.url);

  // Compositor autostarts on shell connect; wait for Xwayland + env.publish
  // so the terminal inherits DISPLAY (DISPLAY_ENV.md §5).
  await router.waitForLog(/Xwayland ready on DISPLAY=/, 25_000);
  // Wait for the publish that actually CARRIES the display env, not for "an
  // env.publish happened". The compositor publishes more than once (geometry
  // early, socket names once Xwayland is up) and waitForLog scans from t=0, so
  // the bare event was satisfied by the earlier publish and the terminal was
  // spawned before spawnEnv had WASH_X_DISPLAY — the `xclock → Can't open
  // display:` flake (docs/FLAKE_LOG.md 2026-07-29, TEST_FLAKES.md A10).
  // WASH_X_DISPLAY sorts last in the line, so matching it means the whole
  // publish landed.
  await router.waitForLog(/env\.publish from .*keys=.*WASH_X_DISPLAY/, 25_000);

  const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.term' });
  expect(launched.t).toBe('launched');
  await page.waitForSelector('wash-app-term .xterm-rows', { timeout: 10_000 });
  // xterm mounted != shell ready — without this the keystrokes below can be
  // typed into a pty that has not drawn its prompt yet, and the first ones
  // are swallowed (docs/TEST_FLAKES.md C5).
  await expect(page.locator('wash-app-term')).toContainText(/\$|#|>/, { timeout: 10_000 });
  await page.locator('wash-app-term').click();
  await page.keyboard.type('xclock\n');

  // The X window maps as a display window owned by the compositor instance.
  const el = page.locator('wash-app-display[data-wash-window]').first();
  await el.waitFor({ state: 'visible', timeout: 20_000 });

  // Raise + focus it (router-authoritative focus → seat keyboard focus), so
  // it's the topmost surface at the click point and keys route to it.
  const win = await el.evaluate((node) => Number(node.getAttribute('data-wash-window')));
  await page.evaluate((w) => window.wash.focusWindow(w), win);

  // Click the canvas → the FE forwards a left button → the compositor
  // injects it into the xclock surface.
  await el.hover();
  await el.click();
  await router.waitForLog(new RegExp(`wash-display: inject win=${win} button left down`), 10_000);

  // Type a key → injected through the virtual keyboard into the surface.
  await page.keyboard.press('a');
  await router.waitForLog(new RegExp(`wash-display: inject win=${win} key keycode=`), 10_000);
});
