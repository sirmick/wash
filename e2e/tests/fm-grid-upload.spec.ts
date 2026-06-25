// fm folder-grid preview: EXTERNAL (OS) file uploads landing per-folder.
//
// The existing fm-folder-grid spec covers INTERNAL move drops onto tiles.
// This one covers the OS-file-drop branch: dropping desktop files onto a
// sub-folder TILE must upload INTO that sub-folder, and a drop on empty grid
// space (or a file tile) lands in the shown folder — the grid analogue of the
// tree's per-folder upload granularity. Per [[wash e2e pattern]] each test
// asserts both the FS state under fmRoot and the BE transition via the log.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { existsSync, readFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
}

// dragFilesOnto synthesizes an external (OS) file drop — a DataTransfer
// carrying real File items, no wash MIME — mirroring fm-dnd-torture's helper.
async function dragFilesOnto(
  page: import('@playwright/test').Page,
  selector: string,
  files: { name: string; content: string }[],
) {
  const dataTransfer = await page.evaluateHandle((files) => {
    const dt = new DataTransfer();
    for (const f of files) dt.items.add(new File([f.content], f.name, { type: 'text/plain' }));
    return dt;
  }, files);
  const target = page.locator(selector);
  await target.dispatchEvent('dragenter', { dataTransfer });
  await target.dispatchEvent('dragover', { dataTransfer });
  await target.dispatchEvent('drop', { dataTransfer });
}

test('external drop onto a sub-folder tile uploads INTO that sub-folder, not the shown folder', async ({ page, router }) => {
  mkdirSync(join(router.fmRoot, 'docs', 'sub'));
  await openFm(page, router);

  // Show docs/ as a grid; its sub/ folder renders as a tile.
  await page.locator('[data-testid="fm-entry-docs"]').click();
  await expect(page.locator('[data-testid="fm-tile-sub"]')).toBeVisible();

  await dragFilesOnto(page, '[data-testid="fm-tile-sub"]', [{ name: 'dropped.txt', content: 'into-sub\n' }]);

  await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 10_000);
  await expect.poll(() => existsSync(join(router.fmRoot, 'docs', 'sub', 'dropped.txt')), { timeout: 5_000 }).toBe(true);
  expect(readFileSync(join(router.fmRoot, 'docs', 'sub', 'dropped.txt'), 'utf8')).toBe('into-sub\n');
  // NOT the shown folder (docs/) and NOT the cwd (root).
  expect(existsSync(join(router.fmRoot, 'docs', 'dropped.txt'))).toBe(false);
  expect(existsSync(join(router.fmRoot, 'dropped.txt'))).toBe(false);
});

test('external drop on empty grid space uploads into the shown folder', async ({ page, router }) => {
  await openFm(page, router);

  await page.locator('[data-testid="fm-entry-docs"]').click();
  await expect(page.locator('[data-testid="fm-folder-grid"]')).toBeVisible();

  // The grid container itself (not a folder tile) → the shown folder, docs/.
  await dragFilesOnto(page, '[data-testid="fm-folder-grid"]', [{ name: 'pane.txt', content: 'into-docs\n' }]);

  await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 10_000);
  await expect.poll(() => existsSync(join(router.fmRoot, 'docs', 'pane.txt')), { timeout: 5_000 }).toBe(true);
  expect(existsSync(join(router.fmRoot, 'pane.txt'))).toBe(false);
});
