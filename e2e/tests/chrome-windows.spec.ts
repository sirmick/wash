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
