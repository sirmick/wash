// Column header + per-row cells. The header is sticky above the
// tree, sortable, and shares the same SortKey state the existing
// sort menu drives.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { mkdirSync, writeFileSync, utimesSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
}

test.describe('fm column header', () => {
  test('renders Name / Modified / Created / Size headers', async ({ page, router }) => {
    await openFm(page, router);
    const hdr = page.locator('[data-testid="fm-column-header"]');
    await expect(hdr).toBeVisible();
    await expect(page.locator('[data-testid="fm-header-name"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-header-mtime"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-header-ctime"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-header-size"]')).toBeVisible();
  });

  test('clicking Modified header sorts by mtime; clicking again toggles direction', async ({ page, router }) => {
    // Stamp three files with very different mtimes so the sort
    // ordering is deterministic regardless of fs jitter.
    const root = router.fmRoot;
    writeFileSync(join(root, 'old.txt'), 'x');
    utimesSync(join(root, 'old.txt'), new Date('2020-01-01'), new Date('2020-01-01'));
    writeFileSync(join(root, 'mid.txt'), 'x');
    utimesSync(join(root, 'mid.txt'), new Date('2022-01-01'), new Date('2022-01-01'));
    writeFileSync(join(root, 'new.txt'), 'x');
    utimesSync(join(root, 'new.txt'), new Date('2024-01-01'), new Date('2024-01-01'));

    await openFm(page, router);
    // Default sort is name; click Modified header once to sort
    // by mtime ascending (oldest first).
    await page.locator('[data-testid="fm-header-mtime"]').click();
    const rowsAsc = await page.locator('[data-testid^="fm-entry-"]').all();
    const namesAsc = await Promise.all(rowsAsc.map((r) => r.getAttribute('data-path')));
    // Among the three new files, "old.txt" should appear before
    // "mid.txt" before "new.txt" in ascending mtime order.
    const idx = (name: string) => namesAsc.findIndex((p) => p?.endsWith('/' + name));
    expect(idx('old.txt')).toBeLessThan(idx('mid.txt'));
    expect(idx('mid.txt')).toBeLessThan(idx('new.txt'));

    // Click again → descending.
    await page.locator('[data-testid="fm-header-mtime"]').click();
    const rowsDesc = await page.locator('[data-testid^="fm-entry-"]').all();
    const namesDesc = await Promise.all(rowsDesc.map((r) => r.getAttribute('data-path')));
    const idxD = (name: string) => namesDesc.findIndex((p) => p?.endsWith('/' + name));
    expect(idxD('new.txt')).toBeLessThan(idxD('mid.txt'));
    expect(idxD('mid.txt')).toBeLessThan(idxD('old.txt'));
  });

  test('directory row shows item count in the size cell', async ({ page, router }) => {
    mkdirSync(join(router.fmRoot, 'mybox'));
    writeFileSync(join(router.fmRoot, 'mybox', 'a'), '1');
    writeFileSync(join(router.fmRoot, 'mybox', 'b'), '2');
    writeFileSync(join(router.fmRoot, 'mybox', 'c'), '3');

    await openFm(page, router);
    // Expand the dir so its listings are fetched; childCount is
    // populated only after the listing arrives.
    await page.locator('[data-testid="fm-entry-mybox"]').dblclick();
    // Navigate back to root via Home button to see mybox in its
    // parent row context with the count cell rendered.
    await page.locator('[data-testid="fm-home"]').click();

    const row = page.locator('[data-testid="fm-entry-mybox"]');
    await expect(row).toContainText(/3 items/);
  });
});
