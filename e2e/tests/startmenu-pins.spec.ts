// Start-menu polish: pinned apps, the search filter, and keyboard driving
// (docs/Review-findings.md P2 cross-app, "start menu has no recent files/
// pinned").
//
// Both halves: the FE (sections, filter, highlight, what launches) and the
// BE (the pin lands in $XDG_STATE_HOME/wash/recent.json alongside recent,
// and survives a reload because the BE — not the tab — owns it).

import { test, expect } from '../fixtures/router';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import type { RouterHandle } from '../fixtures/router';

test.use({ routerOpts: { apps: ['session', 'about', 'fm'] } });

function pinsOnDisk(router: RouterHandle): string[] {
  const raw = readFileSync(join(router.xdgStateHome, 'wash', 'recent.json'), 'utf8');
  return (JSON.parse(raw) as { pinned?: string[] }).pinned ?? [];
}

async function openMenu(page: Page) {
  await page.locator('button[title="Apps"]').click();
  await expect(page.locator('[data-testid="start-menu"]')).toBeVisible();
}

test.describe('start menu: pins, search, keyboard', () => {
  test.setTimeout(30_000);

  test('right-click pins an app; the pin persists across a reload and unpins again', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await openMenu(page);

    const menu = page.locator('[data-testid="start-menu"]');
    await expect(menu.locator('[data-testid="start-menu-pinned"]')).toHaveCount(0);
    await menu.locator('[data-testid="start-menu-com.wash.about"]').click({ button: 'right' });
    const ctx = page.locator('[data-testid="start-menu-app-menu"]');
    await expect(ctx).toBeVisible();
    await expect(ctx.locator('[data-testid="start-menu-pin"]')).toHaveText('Pin to start');
    await ctx.locator('[data-testid="start-menu-pin"]').click();

    // The start menu stays up (the context menu is a portal, not an
    // "outside click") and re-renders from the BE's launcher.state push.
    await expect(menu.locator('[data-testid="start-menu-pinned"]')).toHaveText(/pinned/i);
    const pins = menu.locator('[data-testid="start-menu-pinned-item"]');
    await expect(pins).toHaveCount(1);
    await expect(pins.nth(0)).toContainText(/about/i);
    // BE half: it is in the state file, not just in this tab.
    await expect.poll(() => pinsOnDisk(router)).toEqual(['com.wash.about']);

    // Reload: the BE owns it, so the pin comes back.
    await page.reload();
    await expect(page.locator('wash-app-session')).toBeVisible();
    await openMenu(page);
    await expect(page.locator('[data-testid="start-menu-pinned-item"]')).toHaveCount(1);

    // Unpin from the pinned row itself.
    await page.locator('[data-testid="start-menu-pinned-item"]').click({ button: 'right' });
    await expect(ctx.locator('[data-testid="start-menu-pin"]')).toHaveText('Unpin from start');
    await ctx.locator('[data-testid="start-menu-pin"]').click();
    await expect(page.locator('[data-testid="start-menu-pinned"]')).toHaveCount(0);
    await expect.poll(() => pinsOnDisk(router)).toEqual([]);
  });

  test('the filter narrows the list, and Arrows/Enter launch without the mouse', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await openMenu(page);

    // The filter is focused on open: typing goes straight into it.
    const search = page.locator('[data-testid="start-menu-search"]');
    await expect(search).toBeFocused();
    await search.fill('abou');
    await expect(page.locator('[data-testid="start-menu-com.wash.about"]')).toBeVisible();
    await expect(page.locator('[data-testid="start-menu-com.wash.fm"]')).toHaveCount(0);

    // The first row is highlighted; Enter launches it.
    const row = page.locator('[data-testid="start-menu"] [data-selected="true"]');
    await expect(row).toHaveCount(1);
    await expect(row).toContainText(/about/i);
    await page.keyboard.press('Enter');
    await expect(page.locator('wash-app-about')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('[data-testid="start-menu"]')).toHaveCount(0);
  });

  test('Arrow keys move the highlight and Esc closes the menu', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await openMenu(page);

    const rows = page.locator('[data-testid="start-menu"] [data-selected]');
    const first = await rows.first().textContent();
    await page.keyboard.press('ArrowDown');
    const nowSelected = page.locator('[data-testid="start-menu"] [data-selected="true"]');
    await expect(nowSelected).toHaveCount(1);
    expect(await nowSelected.textContent()).not.toBe(first);
    // Up returns to the top.
    await page.keyboard.press('ArrowUp');
    expect(await nowSelected.textContent()).toBe(first);

    await page.keyboard.press('Escape');
    await expect(page.locator('[data-testid="start-menu"]')).toHaveCount(0);
  });
});
