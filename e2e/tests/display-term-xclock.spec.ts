// Real end-to-end: type `xclock` in a wash terminal → an X11 window
// appears as a wash window. This proves the display-env propagation
// chain (docs/DISPLAY_ENV.md): the autostarted compositor publishes its
// DISPLAY via env.publish → the router merges it into spawnEnv → the
// terminal's shell (pty.WithWashEnv) gets DISPLAY → an X client launched
// at the prompt finds the compositor and maps as a <wash-app-display>
// window.
//
// Needs the native compositor: out/wash-display (make WASH_DISPLAY=1) and
// /usr/bin/xclock. Not part of the default `make e2e` (no compositor in
// CI). Kept here as the reusable real-stack check.
import { test, expect, displaySkipReason } from '../fixtures/router';
import { existsSync } from 'node:fs';

test.use({ routerOpts: { apps: ['session', 'term', 'display'], showHidden: true } });

// Opt-in capstone: needs the native compositor AND an X client.
test.beforeEach(() => {
  const reason = displaySkipReason() ?? (existsSync('/usr/bin/xclock') ? null : '/usr/bin/xclock not installed');
  test.skip(reason !== null, reason ?? '');
});

test('type xclock in a terminal → X11 window appears', async ({ page, router }) => {
  test.setTimeout(60_000);
  await page.goto(router.url);

  // Shell connect triggers the background-app sweep → the compositor
  // starts, brings up Xwayland, and publishes its display env. Wait for
  // BOTH the Xwayland socket and the env.publish before launching term,
  // so the terminal's shell inherits DISPLAY (spawnEnv is read at spawn
  // time — DISPLAY_ENV.md §5 timing note).
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

  // Launch the terminal and wait for the shell prompt to be live.
  const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.term' });
  expect(launched.t).toBe('launched');
  await page.waitForSelector('wash-app-term .xterm-rows', { timeout: 10_000 });
  // xterm mounted != shell ready — without this the keystrokes below can be
  // typed into a pty that has not drawn its prompt yet, and the first ones
  // are swallowed (docs/TEST_FLAKES.md C5).
  await expect(page.locator('wash-app-term')).toContainText(/\$|#|>/, { timeout: 10_000 });
  await page.locator('wash-app-term').click();

  // Type the command. xclock runs in the foreground and opens a window;
  // that's all we need.
  await page.keyboard.type('xclock\n');

  // The X window maps through wlr_xwayland → window.create with the
  // built-in wash-app-display element. Assert both the router-side
  // window.create (BE proof) and the FE element mounting (browser proof).
  await router.waitForLog(/window\.create .*element="wash-app-display"/, 20_000);
  await page.waitForSelector('wash-app-display', { timeout: 20_000 });

  // And confirm the terminal did NOT report a missing display — i.e. env
  // propagation actually worked rather than xclock failing to connect.
  await expect(page.locator('wash-app-term .xterm-rows')).not.toContainText(
    'Can\'t open display',
    { timeout: 2_000 },
  );
});
