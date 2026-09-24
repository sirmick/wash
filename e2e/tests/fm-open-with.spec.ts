// fm "Open with…" (docs/Review-findings.md P2 → fm). Double-click routes a
// file to whichever app declared its extension; nothing let the user pick a
// DIFFERENT app. The context menu now offers a chooser: the apps registered
// for the extension first, then the other known openers, and picking one
// spawns it with the file as its `--open` argv (Conn.SpawnRequestOpen, which
// is why fm's manifest declares CapSpawn).
//
// Both halves: the chooser's rows + the target window, and the fm BE's
// `fm: open_with app=… path=…` audit line.

import { test, expect } from '../fixtures/router';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAKklEQVR4nGK+Y2NzTm7fPrlzaCSDjc2dOzY2mCQDVlEbmzsDqgMQAAD//+8yV15/i6x3AAAAAElFTkSuQmCC',
  'base64',
);

function seed(root: string): void {
  writeFileSync(join(root, 'notes.md'), '# notes\n\nopen-with body\n');
  writeFileSync(join(root, 'photo.png'), PNG);
}

test.use({
  routerOpts: { apps: ['session', 'fm', 'edit', 'imageview', 'term'], fmRoot: true, fmSeed: seed },
});

async function openFm(
  page: import('@playwright/test').Page,
  router: import('../fixtures/router').RouterHandle,
) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-notes.md"]')).toBeVisible();
}

const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

test.describe('fm open with', () => {
  test.setTimeout(45_000);

  test('the chooser ranks the extension-registered app first and the pick spawns it', async ({ page, router }) => {
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-photo.png"]').click({ button: 'right' });
    await expect(page.locator('[data-testid="fm-ctx-open-with"]')).toBeVisible();
    await page.locator('[data-testid="fm-ctx-open-with"]').click();

    const menu = page.locator('[data-testid="fm-open-with-menu"]');
    await expect(menu).toBeVisible();
    // imageview declared .png, so it leads; the editor and terminal follow
    // as generic openers.
    const items = menu.locator('[data-testid^="fm-open-with-com."]');
    await expect(items).toHaveCount(3);
    await expect(items.first()).toHaveAttribute('data-testid', 'fm-open-with-com.wash.imageview');
    await expect(menu.locator('[data-testid="fm-open-with-com.wash.edit"]')).toBeVisible();
    await expect(menu.locator('[data-testid="fm-open-with-com.wash.term"]')).toBeVisible();

    // Pick the editor for a .png — the whole point of the chooser.
    const from = router.logCursor();
    await menu.locator('[data-testid="fm-open-with-com.wash.edit"]').click();
    await expect(menu).toHaveCount(0);
    const target = escapeRe(join(router.fmRoot, 'photo.png'));
    await router.waitForLog(
      new RegExp(`fm: open_with app=com\\.wash\\.edit path="${target}"`),
      10_000,
      from,
    );
    await expect(page.locator('wash-app-edit')).toBeVisible({ timeout: 15_000 });
  });

  test('a file no app registered still offers every opener', async ({ page, router }) => {
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-notes.md"]').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-open-with"]').click();
    const menu = page.locator('[data-testid="fm-open-with-menu"]');
    // .md is the editor's; imageview + term remain as the other openers.
    await expect(menu.locator('[data-testid="fm-open-with-com.wash.edit"]')).toBeVisible();
    await expect(menu.locator('[data-testid="fm-open-with-com.wash.imageview"]')).toBeVisible();

    const from = router.logCursor();
    await menu.locator('[data-testid="fm-open-with-com.wash.edit"]').click();
    const target = escapeRe(join(router.fmRoot, 'notes.md'));
    await router.waitForLog(new RegExp(`fm: open_with app=com\\.wash\\.edit path="${target}"`), 10_000, from);
    await expect(page.locator('wash-app-edit .cm-content')).toContainText('open-with body', { timeout: 15_000 });
  });
});
