// BE crash → shell tombstone end-to-end.
//
// Flow under test:
//   1. wash-test mounts as a regular floating window.
//   2. Click the "Crash BE (panic)" action — wash-test panics in a
//      goroutine.
//   3. Router's spawnAndRun-cleanup goroutine sees cmd.Wait() return
//      with a non-zero exit, broadcasts ShellAppCrashed with the
//      captured stdout+stderr tail.
//   4. Shell marks the window crashed in place; the FloatingWindow
//      paints a red CrashPane over the (dead) custom element. The
//      log textbox contains the Go panic trace.
//   5. The router-driven window-delete patch arrives shortly after
//      but is suppressed for crashed windows — the tombstone stays.
//   6. Clicking the window's close button removes the tombstone
//      (FE-only, since the router-side window is already gone).
//
// We run this in chrome mode (not kiosk) because the crash tombstone
// only makes sense for windowed apps and we need a frame around the
// dead element.

import { test, expect } from '../fixtures/router';
import { spawn } from 'node:child_process';
import { join } from 'node:path';

test.describe('BE crash → shell tombstone', () => {
  test.setTimeout(20_000);
  test.use({ routerOpts: { showHidden: true } });

  test('panicked BE renders a crash pane with the goroutine trace', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    // Launch wash-test via palette. The test app is hidden in
    // production catalogs; showHidden=true above un-hides it.
    await page.keyboard.press('Control+Space');
    const input = page.locator('[data-testid="palette-input"]');
    await input.fill('test');
    await page.locator('[data-testid="palette-item-com.wash.test"]').click();

    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();
    await expect(app.locator('[data-testid="instance"]')).not.toHaveText('');

    // Find the parent floating-window frame. We assert the tombstone
    // appears on the SAME frame, not as a separate new window.
    const window_ = page.locator('.wash-window').filter({ has: app });
    await expect(window_).toBeVisible();

    // Click crash → BE panics in a goroutine → cmd.Wait returns →
    // router ships app.crashed → shell paints the tombstone.
    await app.locator('[data-testid="action-crash-be"]').click();

    const crashPane = window_.locator('[data-testid="window-crashed"]');
    await expect(crashPane).toBeVisible({ timeout: 5_000 });

    // The captured log includes Go's panic prefix and the trace.
    const logBox = crashPane.locator('[data-testid="window-crashed-log"]');
    await expect(logBox).toHaveValue(/panic: wash-test: deliberate crash from FE button/);
    await expect(logBox).toHaveValue(/goroutine \d+/);

    // Router-side log confirms the crash was detected and broadcast.
    await router.waitForLog(
      /app com\.wash\.test crashed:.*code=2.*instance=/,
      5_000,
    );

    // The dead custom element has lost its content (remap doesn't
    // re-mount, and the BE is gone) — but the frame and tombstone
    // are still there.
    await expect(window_).toBeVisible();

    // Closing the tombstone removes the window locally.
    await window_.locator('[data-testid="window-close"]').click();
    await expect(window_).toHaveCount(0);
  });

  test('terminal-attached BE crash tombstones with a "see terminal" hint', async ({ page, router }) => {
    // When wash-test is launched from outside the router (e.g. from a
    // shell inside wash-term), the router doesn't own the process and
    // can't read its stdout/stderr — both go to the launching
    // terminal. But the window IS owned by the router, so an
    // unexpected conn-drop should still tombstone the window. The
    // tombstone log gets a hint pointing the user at the terminal.
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    const testBin = join(router.appsDir, 'wash-test');
    const child = spawn(testBin, [], {
      env: { ...process.env, WASH_DISPLAY: router.controlSocket },
      stdio: 'ignore',
      detached: false,
    });
    try {
      const app = page.locator('wash-app-test');
      await expect(app).toBeVisible({ timeout: 5_000 });

      const window_ = page.locator('.wash-window').filter({ has: app });
      await expect(window_).toBeVisible();

      // Crash button → BE panics → conn drops → router emits the
      // terminal-attach variant of the crash event.
      await app.locator('[data-testid="action-crash-be"]').click();

      const crashPane = window_.locator('[data-testid="window-crashed"]');
      await expect(crashPane).toBeVisible({ timeout: 5_000 });

      // No stdout/stderr was captured (the router didn't own them),
      // but the dialog points the user at the launching terminal.
      const logBox = crashPane.locator('[data-testid="window-crashed-log"]');
      await expect(logBox).toHaveValue(/launched from a terminal/);

      await router.waitForLog(
        /app com\.wash\.test exited unexpectedly \(terminal-attach\)/,
        5_000,
      );
    } finally {
      try { child.kill('SIGKILL'); } catch { /* already gone */ }
    }
  });

  test('graceful BE exit does NOT produce a tombstone', async ({ page, router }) => {
    // Sanity-counterpart: closing via the titlebar X (which sends
    // window.close_clicked, exits the app via its own close handler)
    // is a clean exit. No crash event, no tombstone, window just
    // disappears.
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.keyboard.press('Control+Space');
    await page.locator('[data-testid="palette-input"]').fill('test');
    await page.locator('[data-testid="palette-item-com.wash.test"]').click();

    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();
    const window_ = page.locator('.wash-window').filter({ has: app });
    await window_.locator('[data-testid="window-close"]').click();

    await expect(window_).toHaveCount(0);
    // No crashed-window tombstones anywhere on the page.
    await expect(page.locator('[data-testid="window-crashed"]')).toHaveCount(0);
    // And the router never logged a crash.
    expect(router.log()).not.toMatch(/crashed:/);
  });
});
