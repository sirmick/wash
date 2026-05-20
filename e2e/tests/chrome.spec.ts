// Chrome-mode tests: session app + taskbar + start menu + floating
// windows. Hidden apps stay out of the launcher; About is launchable.

import { test, expect } from '../fixtures/router';

test.describe('chrome', () => {
  test('start menu lists About but hides the test app', async ({ page, router }) => {
    await page.goto(router.url);
    const session = page.locator('wash-app-session');
    await expect(session).toBeVisible();
    await session.locator('[data-testid="action-set-title"]').waitFor({ state: 'detached', timeout: 200 }).catch(() => {});
    // Open the start menu and assert the catalog rendering.
    await page.locator('button[title="Apps"]').click();
    // Use accessible role to find the buttons (they're button elements
    // inside the menu div).
    await expect(page.getByRole('button', { name: /About wash/ })).toBeVisible();
    // Hidden app must not appear.
    await expect(page.getByRole('button', { name: /wash test/i })).toHaveCount(0);
  });

  test('launching About via start menu opens a floating window', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    // Open start menu, click About.
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /About wash/ }).click();

    // A wash-app-about element appears inside a floating window.
    const aboutEl = page.locator('wash-app-about');
    await expect(aboutEl).toBeVisible();
    await expect(aboutEl).toContainText('Web Application SHell');
  });

  test('palette: Ctrl+Space opens, type filters, Enter launches', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.keyboard.press('Control+Space');
    const palette = page.locator('[data-testid="palette"]');
    await expect(palette).toBeVisible();
    const input = page.locator('[data-testid="palette-input"]');
    await expect(input).toBeFocused();

    await input.fill('about');
    await expect(page.locator('[data-testid="palette-item-com.wash.about"]')).toBeVisible();
    // Test app is hidden by default → no entry, even with a matching filter.
    await input.fill('test');
    await expect(page.locator('[data-testid="palette-item-com.wash.test"]')).toHaveCount(0);

    await input.fill('about');
    await page.keyboard.press('Enter');
    await expect(palette).toBeHidden();
    await expect(page.locator('wash-app-about')).toBeVisible();
  });

  test('palette: Escape closes it', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.keyboard.press('Control+Space');
    await expect(page.locator('[data-testid="palette"]')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.locator('[data-testid="palette"]')).toBeHidden();
  });

  test('palette: clicking the search button opens it; backdrop click closes', async ({
    page,
    router,
  }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.locator('[data-testid="palette-open"]').click();
    const palette = page.locator('[data-testid="palette"]');
    await expect(palette).toBeVisible();

    // Click the backdrop (a corner well outside the inner box).
    await palette.click({ position: { x: 5, y: 5 } });
    await expect(palette).toBeHidden();
  });

  test('clicking the titlebar X dismisses the window', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /About wash/ }).click();
    const aboutEl = page.locator('wash-app-about');
    await expect(aboutEl).toBeVisible();

    // The close × is an unlabeled button with aria-label="Close window".
    await page.getByRole('button', { name: 'Close window' }).click();
    await expect(aboutEl).toHaveCount(0);
  });
});
