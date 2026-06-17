// fm folder-grid preview: selecting a folder renders its contents as a
// tile grid in the preview pane — lucide icons by type, and real
// thumbnails for images streamed from the BE over a raw channel
// (internal/thumbs + @wash/ui createFileClient). Asserting the thumbnail's
// <img> gets a blob: URL exercises that whole transport end to end.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

// A valid 8×8 PNG (decodable by internal/thumbs' stdlib image/png).
const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAKklEQVR4nGK+Y2NzTm7fPrlzaCSDjc2dOzY2mCQDVlEbmzsDqgMQAAD//+8yV15/i6x3AAAAAElFTkSuQmCC',
  'base64',
);

function seedWithPics(root: string): void {
  seedSimpleTree(root);
  mkdirSync(join(root, 'pics'));
  writeFileSync(join(root, 'pics', 'cat.png'), PNG);
  writeFileSync(join(root, 'pics', 'dog.png'), PNG);
  writeFileSync(join(root, 'pics', 'notes.txt'), 'not an image\n');
}

test.use({ routerOpts: { apps: ['session', 'about', 'fm'], fmRoot: true, fmSeed: seedWithPics } });

async function openFm(
  page: import('@playwright/test').Page,
  router: import('../fixtures/router').RouterHandle,
) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-pics"]')).toBeVisible();
}

test.describe('fm folder-grid preview', () => {
  test('selecting a folder shows a tile grid with icons and image thumbnails', async ({ page, router }) => {
    await openFm(page, router);

    // Single-click the folder → its contents render as a grid in the dock.
    await page.locator('[data-testid="fm-entry-pics"]').click();

    await expect(page.locator('[data-testid="fm-folder-grid"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-tile-cat.png"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-tile-notes.txt"]')).toBeVisible();

    // The image tile's thumbnail streams over the raw channel and lands as
    // a blob URL — proving thumbs BE + channel + createFileClient work.
    const thumb = page.locator('[data-testid="fm-tile-cat.png"] img');
    await expect(thumb).toHaveAttribute('src', /^blob:/, { timeout: 10_000 });
  });

  test('grid tiles support Ctrl multi-select (shared selection kernel)', async ({ page, router }) => {
    await openFm(page, router);
    await page.locator('[data-testid="fm-entry-pics"]').click();
    await expect(page.locator('[data-testid="fm-folder-grid"]')).toBeVisible();

    await page.locator('[data-testid="fm-tile-cat.png"]').click();
    await page.locator('[data-testid="fm-tile-dog.png"]').click({ modifiers: ['Control'] });

    // Both tiles are selected (data-selected mirrors the shared selection set).
    await expect(page.locator('[data-testid="fm-tile-cat.png"][data-selected="true"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-tile-dog.png"][data-selected="true"]')).toBeVisible();
  });

  test('right-clicking a grid tile opens the shared context menu', async ({ page, router }) => {
    await openFm(page, router);
    await page.locator('[data-testid="fm-entry-pics"]').click();
    await expect(page.locator('[data-testid="fm-folder-grid"]')).toBeVisible();

    await page.locator('[data-testid="fm-tile-cat.png"]').click({ button: 'right' });
    await expect(page.locator('[data-testid="fm-context-menu"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-ctx-open"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-ctx-rename"]')).toBeVisible();
  });

  test('grid follows the global sort (name order by default)', async ({ page, router }) => {
    await openFm(page, router);
    await page.locator('[data-testid="fm-entry-pics"]').click();
    await expect(page.locator('[data-testid="fm-folder-grid"]')).toBeVisible();

    // sortedFiltered orders the grid the same as the tree: dirs first, then
    // case-insensitive name. cat.png < dog.png < notes.txt.
    const names = await page.locator('[data-testid^="fm-tile-"]').evaluateAll((els) =>
      els.map((e) => e.getAttribute('data-testid')!.replace('fm-tile-', '')),
    );
    expect(names).toEqual(['cat.png', 'dog.png', 'notes.txt']);
  });
});
