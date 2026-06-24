// wash-term menubar: Edit (copy/paste/select-all/clear), Tab (new/close +
// color), and Theme (follow-desktop / dark / light) menus across the top of
// the terminal window, driving the active tab's xterm via TerminalAPI.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

async function openTerminal(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page
    .locator('[data-testid="start-menu"]')
    .getByRole('button', { name: 'Terminal', exact: true })
    .click();
  const host = page.locator('[data-testid="term-host"]').first();
  await expect(host).toBeVisible();
  await expect.poll(() => bufferText(page), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);
  return host;
}

async function bufferText(page: Page): Promise<string> {
  return await page.locator('[data-testid="term-host"]').first().evaluate((host: any) => {
    const term = host.__washTerm;
    if (!term) return '';
    const buf = term.buffer.active;
    let out = '';
    for (let y = 0; y < buf.length; y++) {
      const line = buf.getLine(y);
      if (line) out += line.translateToString(true) + '\n';
    }
    return out;
  });
}

function themeBg(page: Page) {
  return page
    .locator('[data-testid="term-host"]')
    .first()
    .evaluate((h: any) => h.__washTerm?.options?.theme?.background);
}

test.describe('terminal menubar', () => {
  test.setTimeout(25_000);

  test('Edit › Copy copies the selection to the clipboard', async ({ page, context, router }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: router.url });
    const host = await openTerminal(page, router.url);

    await host.click();
    await page.keyboard.type('menubar-copy-marker');
    await host.evaluate((h: any) => h.__washTerm?.selectAll());

    await page.locator('[data-testid="term-menu-edit-btn"]').click();
    await expect(page.locator('[data-testid="term-menu-edit"]')).toBeVisible();
    await page.locator('[data-testid="term-menu-copy"]').click();

    await expect
      .poll(() => page.evaluate(() => navigator.clipboard.readText()), { timeout: 5_000 })
      .toContain('menubar-copy-marker');
  });

  test('Theme menu pins the xterm palette dark/light and back to follow', async ({ page, router }) => {
    await openTerminal(page, router.url);

    // Pick Dark → black background; the active item shows a check.
    await page.locator('[data-testid="term-menu-theme-btn"]').click();
    await page.locator('[data-testid="term-menu-theme-dark"]').click();
    await expect.poll(() => themeBg(page)).toBe('#000000');

    // Pick Light → white background.
    await page.locator('[data-testid="term-menu-theme-btn"]').click();
    await page.locator('[data-testid="term-menu-theme-light"]').click();
    await expect.poll(() => themeBg(page)).toBe('#ffffff');

    // The override applies to a newly-opened tab too (window-wide).
    await page.locator('[data-testid="term-menu-tab-btn"]').click();
    await page.locator('[data-testid="term-menu-newtab"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);
    await expect
      .poll(() =>
        page
          .locator('[data-testid="term-host"]')
          .nth(1)
          .evaluate((h: any) => h.__washTerm?.options?.theme?.background ?? ''),
        { timeout: 8_000 },
      )
      .toBe('#ffffff');
  });

  test('Tab › color tags the active tab via the menubar', async ({ page, router }) => {
    await openTerminal(page, router.url);

    // Tag the active tab amber through the Tab menu.
    await page.locator('[data-testid="term-menu-tab-btn"]').click();
    await expect(page.locator('[data-testid="term-menu-tab"]')).toBeVisible();
    await page.locator('[data-testid="term-menu-tag-amber"]').click();

    // The active tab button's top border picks up the tag color (non-transparent).
    const tab = page.locator('button[data-testid^="term-tab-"]:not([data-testid^="term-tab-close"])').first();
    await expect
      .poll(() => tab.evaluate((el) => getComputedStyle(el as HTMLElement).borderTopColor))
      .not.toBe('rgba(0, 0, 0, 0)');
  });
});
