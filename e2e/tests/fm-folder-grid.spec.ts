// fm folder-grid preview: selecting a folder renders its contents as a
// tile grid in the preview pane — lucide icons by type, and real
// thumbnails for images streamed from the BE over a raw channel
// (internal/thumbs + @wash/ui createFileClient). Asserting the thumbnail's
// <img> gets a blob: URL exercises that whole transport end to end.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { existsSync, mkdirSync, writeFileSync } from 'node:fs';
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

    // Tiles race the listing under parallel-worker load — wait for all
    // three before reading their order (docs/TEST_FLAKES.md remedy).
    await expect(page.locator('[data-testid^="fm-tile-"]')).toHaveCount(3);

    // sortedFiltered orders the grid the same as the tree: dirs first, then
    // case-insensitive name. cat.png < dog.png < notes.txt.
    const names = await page.locator('[data-testid^="fm-tile-"]').evaluateAll((els) =>
      els.map((e) => e.getAttribute('data-testid')!.replace('fm-tile-', '')),
    );
    expect(names).toEqual(['cat.png', 'dog.png', 'notes.txt']);
  });
});

// The grid lives in the preview dock, outside the tree's list-pane drop zone,
// so it carries its own container-level drop handlers that land a drop INTO
// the shown folder (gridDir). Folder TILES still claim their own drops; a
// drop anywhere a folder tile didn't claim — a file tile, empty space, an
// empty folder — falls through to the shown folder. Source rows come from the
// tree on the left (still visible while a folder is previewed as a grid).
test.describe('fm folder-grid preview: drop targets', () => {
  test('drop onto a file/image tile lands in the shown folder', async ({ page, router }) => {
    await openFm(page, router);
    await page.locator('[data-testid="fm-entry-pics"]').click();
    await expect(page.locator('[data-testid="fm-folder-grid"]')).toBeVisible();

    // hello.txt (root, from seedSimpleTree) → a NON-folder tile, a dead spot
    // before the pane drop zone. It must move into the shown folder (pics).
    await page.locator('[data-testid="fm-entry-hello.txt"]')
      .dragTo(page.locator('[data-testid="fm-tile-notes.txt"]'));

    await expect.poll(() => existsSync(join(router.fmRoot, 'pics', 'hello.txt')), { timeout: 5_000 }).toBe(true);
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(false);
  });

  test('drop onto an empty folder grid moves into that folder', async ({ page, router }) => {
    mkdirSync(join(router.fmRoot, 'empty'));
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-empty"]').click();
    // An empty folder renders the "(empty folder)" placeholder grid — which
    // is still a drop target (the case the tree can't express at all).
    await expect(page.locator('[data-testid="fm-folder-grid"]')).toContainText('empty folder');

    await page.locator('[data-testid="fm-entry-hello.txt"]')
      .dragTo(page.locator('[data-testid="fm-folder-grid"]'));

    await expect.poll(() => existsSync(join(router.fmRoot, 'empty', 'hello.txt')), { timeout: 5_000 }).toBe(true);
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(false);
  });

  test('drop onto a sub-folder tile moves into that sub-folder, not the shown folder', async ({ page, router }) => {
    mkdirSync(join(router.fmRoot, 'pics', 'sub'));
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-pics"]').click();
    await expect(page.locator('[data-testid="fm-tile-sub"]')).toBeVisible();

    await page.locator('[data-testid="fm-entry-hello.txt"]')
      .dragTo(page.locator('[data-testid="fm-tile-sub"]'));

    // The folder tile claims the drop (stopPropagation) → into pics/sub, and
    // NOT into pics/ (which the pane-level handler would have used).
    await expect.poll(() => existsSync(join(router.fmRoot, 'pics', 'sub', 'hello.txt')), { timeout: 5_000 }).toBe(true);
    expect(existsSync(join(router.fmRoot, 'pics', 'hello.txt'))).toBe(false);
  });
});
