// The two wash-owned context menus that replace the browser's own:
// text fields (Cut/Copy/Paste/Select all) and the window titlebar
// (window actions + send to a viewport).
//
// Both are shell-side. The text one is a single document listener rather
// than a component apps opt into, so the case that matters is that it
// reaches a field inside an app it knows nothing about — here the file
// manager's path bar.

import { test, expect } from '../fixtures/router';

test.describe('context menus', () => {
  test.use({ routerOpts: { showHidden: true } });

  const launchTest = async (page: import('@playwright/test').Page) => {
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /wash test/ }).click();
    await expect(page.locator('wash-app-test')).toBeVisible();
  };

  test('a text field in an app gets the wash menu, not the browser one', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /^Files/ }).click();
    await expect(page.locator('wash-app-fm')).toBeVisible();

    const field = page.locator('wash-app-fm input:visible').first();
    const value = await field.inputValue();
    expect(value).not.toBe('');
    await field.click();
    // Right-clicking inside a selection keeps it, as a native menu does.
    await page.keyboard.press('ControlOrMeta+a');
    await field.click({ button: 'right' });

    await expect(page.getByTestId('text-context-menu')).toBeVisible();
    await expect(page.getByTestId('text-ctx-copy')).toBeEnabled();
    await expect(page.getByTestId('text-ctx-paste')).toBeEnabled();

    // Copy puts the field's selection on the wash clipboard, and hands the
    // field back with the selection intact — the menu reads it at open time
    // precisely because pressing an item moves focus out.
    await page.getByTestId('text-ctx-copy').click();
    await expect(page.getByTestId('text-context-menu')).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.wash.clipboardGetText())).toBe(value);
  });

  test('the titlebar menu sends a window to another viewport', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await launchTest(page);

    const cell = () => page.evaluate(() => {
      const w = window.wash.windows().find((x) => x.element === 'wash-app-test')!;
      return { vx: w.viewport.vx, vy: w.viewport.vy, x: w.x, y: w.y };
    });
    const before = await cell();
    expect(before).toMatchObject({ vx: 0, vy: 0 });

    await page.locator('.wash-titlebar').last().click({ button: 'right', position: { x: 80, y: 6 } });
    await expect(page.getByTestId('window-context-menu')).toBeVisible();
    // The cell it is already on is offered but not actionable, so the list
    // does not reshuffle as windows move.
    await expect(page.getByTestId('window-ctx-send-0-0')).toBeDisabled();

    await page.getByTestId('window-ctx-send-1-0').click();
    await expect.poll(async () => (await cell()).vx).toBe(1);
    const after = await cell();
    expect(after.vy).toBe(0);
    // Moves by whole screens on one axis only.
    expect(after.y).toBe(before.y);
    expect(after.x).toBeGreaterThan(before.x);
  });

  test('right-pressing the titlebar does not start a drag', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await launchTest(page);

    const pos = () => page.evaluate(() => {
      const w = window.wash.windows().find((x) => x.element === 'wash-app-test')!;
      return { x: w.x, y: w.y };
    });
    const before = await pos();

    // The titlebar's pointerdown used to run for any button: a right-press
    // captured the pointer and began a move, so the window faded as if being
    // dragged while its own context menu opened over it.
    const bar = page.locator('.wash-titlebar').last();
    await bar.hover({ position: { x: 80, y: 6 } });
    await page.mouse.down({ button: 'right' });
    await page.mouse.move(400, 400);
    await page.mouse.up({ button: 'right' });

    expect(await pos()).toEqual(before);
  });
});
