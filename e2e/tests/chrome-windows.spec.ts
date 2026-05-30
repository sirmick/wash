// Multi-window / focus / close-handshake tests, using the test app
// reached via --show-hidden so the start menu can launch it.

import { test, expect } from '../fixtures/router';

test.describe('chrome (test app via --show-hidden)', () => {
  test.use({ routerOpts: { showHidden: true } });

  async function launchTestApp(page: import('@playwright/test').Page) {
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /wash test/ }).click();
  }

  test('start menu lists the test app when --show-hidden is set', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.locator('button[title="Apps"]').click();
    await expect(page.getByRole('button', { name: /wash test/ })).toBeVisible();
  });

  test('newly-created window auto-focuses (BE sees focus)', async ({ page, router }) => {
    await page.goto(router.url);
    await launchTestApp(page);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();
    await expect(app.locator('[data-testid="focused"] b')).toHaveText('yes');
  });

  test('clicking a partially-obscured window raises and focuses it', async ({ page, router }) => {
    await page.goto(router.url);
    await launchTestApp(page);
    const win0 = page.locator('wash-app-test').nth(0);
    await expect(win0).toBeVisible();

    // Spawn a second instance via the first's button — it auto-focuses
    // on appear and covers most of win0 (offset by 24 px).
    await win0.locator('[data-testid="action-spawn-self"]').click();
    await expect(page.locator('wash-app-test')).toHaveCount(2);
    const win1 = page.locator('wash-app-test').nth(1);
    await expect(win1.locator('[data-testid="focused"] b')).toHaveText('yes');
    await expect(win0.locator('[data-testid="focused"] b')).toHaveText('no');

    // Click win0's titlebar in the top-left sliver (above win1's top
    // edge). Position is relative to the titlebar — y=6 lands within
    // its 38 px height, x=80 well clear of the close button.
    const frames = page.locator('.wash-window');
    await frames.nth(0).locator('.wash-titlebar').click({ position: { x: 80, y: 6 } });

    await expect(win0.locator('[data-testid="focused"] b')).toHaveText('yes');
    await expect(win1.locator('[data-testid="focused"] b')).toHaveText('no');
  });

  test('right-click taskbar pill closes the window', async ({ page, router }) => {
    await page.goto(router.url);
    await launchTestApp(page);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();

    // The taskbar pill lives in wash-app-session and carries the
    // window title text. Right-click sends close_clicked.
    const pill = page.locator('wash-app-session button', { hasText: /^wash test$/ });
    await expect(pill).toBeVisible();
    await pill.click({ button: 'right' });
    await expect(app).toHaveCount(0);
  });

  test('clicking taskbar pill focuses the window', async ({ page, router }) => {
    await page.goto(router.url);
    await launchTestApp(page);
    const win0 = page.locator('wash-app-test').nth(0);
    await expect(win0).toBeVisible();

    // Spawn a second so we have two pills to disambiguate.
    await win0.locator('[data-testid="action-spawn-self"]').click();
    await expect(page.locator('wash-app-test')).toHaveCount(2);
    const win1 = page.locator('wash-app-test').nth(1);
    // win1 is focused after spawn.
    await expect(win1.locator('[data-testid="focused"] b')).toHaveText('yes');

    // Click the first pill (corresponds to win0; pills render in
    // insertion order).
    const pills = page.locator('wash-app-session button', { hasText: /^wash test$/ });
    await pills.first().click();

    await expect(win0.locator('[data-testid="focused"] b')).toHaveText('yes');
    await expect(win1.locator('[data-testid="focused"] b')).toHaveText('no');
  });

  test('veto close keeps the window; next close dismisses', async ({ page, router }) => {
    await page.goto(router.url);
    await launchTestApp(page);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();

    // Toggle veto on.
    await app.locator('[data-testid="action-toggle-veto"]').click();
    await expect(app.locator('[data-testid="veto-next-close"]')).toHaveText('on');

    // Click the titlebar close button — should be vetoed.
    await page.getByRole('button', { name: 'Close window' }).click();
    // Window stays.
    await expect(app).toBeVisible();
    // Event log records the close_requested with allow=false.
    await expect(app.locator('[data-testid="event-row-close_requested"]').first()).toContainText('allow=false');
    // Veto flag is single-shot — reset back to off.
    await expect(app.locator('[data-testid="veto-next-close"]')).toHaveText('off');

    // A second close click now goes through.
    await page.getByRole('button', { name: 'Close window' }).click();
    await expect(app).toHaveCount(0);
  });

  test('minimize hides window, taskbar pill restores it', async ({ page, router }) => {
    await page.goto(router.url);
    await launchTestApp(page);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();

    await page.getByRole('button', { name: 'Minimize window' }).click();
    // wash-app-test is still in the DOM (so its state isn't lost) but
    // its containing window has display:none, so it isn't visible.
    await expect(app).toBeHidden();
    await expect(app.locator('[data-testid="window-state"] b')).toHaveText('minimized');

    // The taskbar pill still lists the window — click it to restore.
    const pill = page.locator('wash-app-session button', { hasText: /^wash test$/ });
    await pill.click();
    await expect(app).toBeVisible();
    await expect(app.locator('[data-testid="window-state"] b')).toHaveText('normal');
    await expect(app.locator('[data-testid="focused"] b')).toHaveText('yes');
  });

  test('maximize fills viewport; restore returns to pre-max size', async ({ page, router }) => {
    await page.goto(router.url);
    await launchTestApp(page);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();

    // Maximize via the titlebar button.
    await page.getByRole('button', { name: 'Maximize window' }).click();
    await expect(app.locator('[data-testid="window-state"] b')).toHaveText('maximized');

    const frame = page.locator('.wash-window').first();
    const box = await frame.boundingBox();
    if (!box) throw new Error('no frame bbox');
    // Should fill viewport minus the 40 px taskbar.
    expect(box.x).toBeLessThanOrEqual(1);
    expect(box.y).toBeLessThanOrEqual(1);
    expect(box.width).toBeGreaterThanOrEqual(500);

    // Restore via the same button (now labeled "Restore window").
    await page.getByRole('button', { name: 'Restore window' }).click();
    await expect(app.locator('[data-testid="window-state"] b')).toHaveText('normal');
  });

  test('maximize fills the visible screen on a non-origin viewport cell', async ({ page, router }) => {
    await page.goto(router.url);
    await launchTestApp(page);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();

    const { innerW, innerH } = await page.evaluate(() => ({
      innerW: window.innerWidth,
      innerH: window.innerHeight,
    }));

    // Move the test window onto viewport cell (1,0) and pan the camera
    // there. The frame renders *inside* the panned cam container, so a
    // maximize that naively pins to left:0/top:0 lands at the (0,0)
    // cell's corner — a full screen-width off to the left — instead of
    // filling the screen the user is actually looking at. That is the
    // regression this test guards.
    const id = await page.evaluate(() => {
      const w = window.wash.windows().find((x) => x.element === 'wash-app-test');
      return w!.windowID;
    });
    await page.evaluate(
      (args) => {
        window.wash.moveWindow(args.id, args.innerW + 60, 60);
        window.wash.setViewport(1, 0);
      },
      { id, innerW },
    );

    // Confirm the cam actually panned one full screen-width left.
    await page.waitForTimeout(400);
    const cam = page.locator('[data-testid="wash-cam"]');
    const tx = await cam.evaluate(
      (el) => parseFloat(getComputedStyle(el as HTMLElement).transform.split(',')[4]),
    );
    expect(tx).toBeCloseTo(-innerW, 0);

    // Maximize and assert the frame fills the *visible* screen: flush to
    // the top-left, not shoved off toward the origin cell.
    await page.evaluate((wid) => window.wash.maximizeWindow(wid), id);
    await expect(app.locator('[data-testid="window-state"] b')).toHaveText('maximized');

    const frame = page.locator('.wash-window').first();
    const box = await frame.boundingBox();
    if (!box) throw new Error('no frame bbox');
    // Flush to the visible top-left (the pre-fix bug parked it at x ≈ -W).
    expect(box.x).toBeGreaterThanOrEqual(-1);
    expect(box.x).toBeLessThanOrEqual(1);
    expect(box.y).toBeGreaterThanOrEqual(-1);
    expect(box.y).toBeLessThanOrEqual(1);
    // Fills the screen minus the session sidebar reservation (right) and
    // the 40 px taskbar (bottom); never overflows the visible viewport.
    expect(box.width).toBeGreaterThanOrEqual(innerW - 320);
    expect(box.x + box.width).toBeLessThanOrEqual(innerW + 1);
    expect(box.height).toBeGreaterThanOrEqual(innerH - 120);
    expect(box.y + box.height).toBeLessThanOrEqual(innerH - 39);
  });

  test('maximized window tracks a browser resize', async ({ page, router }) => {
    await page.goto(router.url);
    await launchTestApp(page);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();

    // Maximize on a non-origin cell so this also guards the cam
    // anchoring: on resize the cam re-pans by -vx*newW and the maximized
    // frame must re-anchor to the new screen size to stay flush.
    const innerW = await page.evaluate(() => window.innerWidth);
    const id = await page.evaluate(() => {
      const w = window.wash.windows().find((x) => x.element === 'wash-app-test');
      return w!.windowID;
    });
    await page.evaluate(
      (args) => {
        window.wash.moveWindow(args.id, args.innerW + 60, 60);
        window.wash.setViewport(1, 0);
        window.wash.maximizeWindow(args.id);
      },
      { id, innerW },
    );
    await expect(app.locator('[data-testid="window-state"] b')).toHaveText('maximized');
    await page.waitForTimeout(400);

    // Shrink the browser. The maximized frame should follow the new
    // screen size and stay flush to the visible top-left.
    await page.setViewportSize({ width: 900, height: 600 });
    await page.waitForTimeout(400);

    const frame = page.locator('.wash-window').first();
    const box = await frame.boundingBox();
    if (!box) throw new Error('no frame bbox');
    expect(box.x).toBeGreaterThanOrEqual(-1);
    expect(box.x).toBeLessThanOrEqual(1);
    expect(box.y).toBeLessThanOrEqual(1);
    // Width tracks the new 900-px screen (minus sidebar reservation).
    expect(box.width).toBeGreaterThanOrEqual(900 - 320);
    expect(box.x + box.width).toBeLessThanOrEqual(901);
    // Height tracks the new 600-px screen (minus the 40-px taskbar).
    expect(box.height).toBeGreaterThanOrEqual(600 - 120);
    expect(box.y + box.height).toBeLessThanOrEqual(600 - 39);
  });

  test('double-clicking titlebar toggles maximize', async ({ page, router }) => {
    await page.goto(router.url);
    await launchTestApp(page);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();

    const titlebar = page.locator('.wash-window').first().locator('.wash-titlebar');
    await titlebar.dblclick({ position: { x: 100, y: 6 } });
    await expect(app.locator('[data-testid="window-state"] b')).toHaveText('maximized');
    await titlebar.dblclick({ position: { x: 100, y: 6 } });
    await expect(app.locator('[data-testid="window-state"] b')).toHaveText('normal');
  });

  test('drag resize handle commits new geometry to BE', async ({ page, router }) => {
    await page.goto(router.url);
    await launchTestApp(page);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();
    // Test app's manifest default is 560×480.

    const handle = page.locator('[data-testid="window-resize"]').first();
    const box = await handle.boundingBox();
    if (!box) throw new Error('no resize handle bbox');
    // Drag the handle 100 px right and 60 px down.
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2 + 100, box.y + box.height / 2 + 60, { steps: 4 });
    await page.mouse.up();

    // BE receives EvtWindowResize on commit and echoes geometry to FE.
    await expect(app.locator('[data-testid="geometry"] b')).toHaveText('660x540');
    await expect(app.locator('[data-testid="event-row-resize"]').first()).toContainText('660x540');
  });

  test('notify button shows a toast that auto-dismisses', async ({ page, router }) => {
    await page.goto(router.url);
    // Toast lingers ~6s before the auto-dismiss kicks in; bump the
    // test-level cap so the 8s wait below doesn't trip the 5s
    // default in playwright.config.ts.
    test.setTimeout(15_000);
    await launchTestApp(page);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();

    await app.locator('[data-testid="action-notify"]').click();
    const toast = page.locator('[data-testid="notification"]').first();
    await expect(toast).toBeVisible();
    await expect(toast.locator('[data-testid="notification-title"]')).toContainText(/wash-test #/);
    await expect(toast.locator('[data-testid="notification-body"]')).toHaveText(
      'Hello from the test app',
    );
    // Toast auto-dismisses after a few seconds.
    await expect(toast).toBeHidden({ timeout: 8_000 });
  });

  test('clicking a toast dismisses it', async ({ page, router }) => {
    await page.goto(router.url);
    await launchTestApp(page);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();

    await app.locator('[data-testid="action-notify"]').click();
    const toast = page.locator('[data-testid="notification"]').first();
    await expect(toast).toBeVisible();
    await toast.click();
    await expect(toast).toBeHidden({ timeout: 2_000 });
  });

  test('spawn nonexistent surfaces spawn_err event', async ({ page, router }) => {
    await page.goto(router.url);
    await launchTestApp(page);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();

    await app.locator('[data-testid="action-spawn-bad"]').click();
    await expect(app.locator('[data-testid="event-row-spawn_err"]')).toBeVisible();
    await expect(app.locator('[data-testid="counter-events"]')).not.toHaveText('0');
  });
});
