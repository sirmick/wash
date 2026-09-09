// wash-term's Alt tab bindings (apps/term/fe/src/main.tsx onTermKey).
//
// Ctrl+Shift+T / Ctrl+Shift+W / Ctrl+Tab are Chromium's own restore-tab,
// close-window and next-tab on Linux and Windows; a page cannot intercept
// them in a normal browser tab (the existing e2e only passes because CDP-
// injected keys bypass the reservation). Alt+T, Alt+W, Alt+PageUp/PageDown
// and Alt+1…9 are the alternates the browser leaves alone. Both halves:
// the BE's tab-opened line for Alt+T and the pty's exit line for Alt+W,
// the strip's active tab and host count for the rest.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

async function openTerminal(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
  const term = page.locator('wash-app-term');
  await expect(term).toBeVisible();
  await expect(term).toContainText(/\$|#|>/, { timeout: 10_000 });
  await page.locator('[data-testid="term-host"]').first().click();
  return term;
}

// visibleChannel is the channel id of the pane's visible terminal — i.e.
// which tab the strip has active.
async function visibleChannel(page: Page): Promise<string> {
  return (await page.locator('[data-testid="term-host"]:visible').first().getAttribute('data-channel')) ?? '';
}

async function tabIds(page: Page): Promise<string[]> {
  const ids = await page.locator('button[data-testid^="term-tab-"]').evaluateAll(
    (els) => els.map((e) => (e.getAttribute('data-testid') ?? '').replace('term-tab-', '')),
  );
  return ids;
}

test.describe('term Alt tab keys', () => {
  test.setTimeout(45_000);

  test('Alt+T opens a tab, Alt+1/2 and Alt+PgUp/PgDn switch, Alt+W asks then closes', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await router.waitForLog(/wash-term tab opened ch=\d+/, 10_000);
    const hosts = page.locator('[data-testid="term-host"]');
    await expect(hosts).toHaveCount(1);

    // Alt+T: a second tab, and the BE opened a second pty for it.
    const from = router.logCursor();
    await page.keyboard.press('Alt+t');
    await expect(hosts).toHaveCount(2);
    await router.waitForLog(/wash-term tab opened ch=\d+/, 10_000, from);
    await expect(page.locator('[data-testid="term-tabbar"]')).toHaveCount(1);
    const [first, second] = await tabIds(page);
    expect(first).not.toBe(second);
    // The new tab is the active one.
    expect(await visibleChannel(page)).toBe(second);

    // Alt+1 / Alt+2 jump by position.
    await page.keyboard.press('Alt+1');
    await expect.poll(() => visibleChannel(page)).toBe(first);
    await page.keyboard.press('Alt+2');
    await expect.poll(() => visibleChannel(page)).toBe(second);
    // Alt+9 with two tabs: nothing happens.
    await page.keyboard.press('Alt+9');
    await page.waitForTimeout(200);
    expect(await visibleChannel(page)).toBe(second);

    // Alt+PageUp / Alt+PageDown cycle.
    await page.keyboard.press('Alt+PageUp');
    await expect.poll(() => visibleChannel(page)).toBe(first);
    await page.keyboard.press('Alt+PageDown');
    await expect.poll(() => visibleChannel(page)).toBe(second);

    // Alt+W asks (every close asks), then closes the active tab.
    const dialog = page.locator('[data-testid="term-close-confirm"]');
    await page.keyboard.press('Alt+w');
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('at a prompt');
    await page.locator('[data-testid="term-close-confirm-cancel"]').click();
    await expect(dialog).toHaveCount(0);
    await expect(hosts).toHaveCount(2);

    const beforeClose = router.logCursor();
    await page.locator('[data-testid="term-host"]:visible').first().click();
    await page.keyboard.press('Alt+w');
    await expect(dialog).toBeVisible();
    await page.locator('[data-testid="term-close-confirm-ok"]').click();
    await expect(hosts).toHaveCount(1);
    expect(await visibleChannel(page)).toBe(first);
    // BE half: the pty behind the closed tab is gone.
    await router.waitForLog(/pty: win=\d+ shell=\S+ exited/, 10_000, beforeClose);
  });

  test('the Tab and Split menus show both bindings', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.locator('[data-testid="term-menu-tab-btn"]').click();
    const tabMenu = page.locator('[data-testid="term-menu-tab"]');
    await expect(tabMenu.locator('[data-testid="term-menu-newtab"]')).toContainText('Alt+T');
    await expect(tabMenu.locator('[data-testid="term-menu-newtab"]')).toContainText('Ctrl+Shift+T');
    await expect(tabMenu.locator('[data-testid="term-menu-closetab"]')).toContainText('Alt+W');
    await expect(tabMenu.locator('[data-testid="term-menu-next-tab"]')).toContainText('Alt+PgDn');
    await expect(tabMenu.locator('[data-testid="term-menu-prev-tab"]')).toContainText('Alt+PgUp');
    await page.keyboard.press('Escape');

    await page.locator('[data-testid="term-menu-split-btn"]').click();
    const close = page.locator('[data-testid="term-menu-close-pane"]');
    // The key closes a TAB; the label no longer claims otherwise.
    await expect(close).toContainText('Close Tab');
    await expect(close).toContainText('Alt+W');
  });
});
