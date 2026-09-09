// fm keyboard row navigation (docs/Review-findings.md P2 → fm). Arrow keys
// move the cursor row (Shift extends, Space toggles), Home/End and
// PageUp/PageDown jump, ArrowRight/Left expand/step-in and collapse/parent,
// and Enter on a FILE opens it through the same open routing a double-click
// uses — asserted on both halves: the row state in the tree, and the fm
// BE's `fm: open path=` audit line plus the spawned editor window.

import { test, expect } from '../fixtures/router';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'a.txt'), 'A\n');
  writeFileSync(join(root, 'b.txt'), 'B\n');
  mkdirSync(join(root, 'docs'));
  writeFileSync(join(root, 'docs', 'inner.txt'), 'inner file body\n');
  // Enough rows that a page is smaller than the list.
  for (let i = 0; i < 60; i++) {
    writeFileSync(join(root, `row-${String(i).padStart(2, '0')}.txt`), `${i}\n`);
  }
}

test.use({
  routerOpts: { apps: ['session', 'fm', 'bulk', 'edit'], fmRoot: true, fmSeed: seed },
});

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-a.txt"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);
}

const row = (page: import('@playwright/test').Page, name: string) =>
  page.locator(`[data-testid="fm-entry-${name}"]`);

test.describe('fm keyboard navigation', () => {
  test.setTimeout(30_000);

  test('ArrowDown/ArrowUp move the cursor row; Shift extends; Space toggles', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');
    // Sorted dirs-first: docs, a.txt, b.txt, row-00 … Start from a file so
    // the folder's select-lists-and-expands behaviour doesn't shift rows.
    await row(page, 'a.txt').click();
    await fm.press('ArrowDown');
    await expect(row(page, 'b.txt')).toHaveAttribute('data-selected', 'true');
    await expect(row(page, 'a.txt')).not.toHaveAttribute('data-selected', 'true');
    // A file under the cursor previews and becomes the path-bar cursor.
    await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(join(router.fmRoot, 'b.txt'));
    await expect(page.locator('[data-testid="fm-preview"]')).toContainText('B');

    await fm.press('Shift+ArrowDown');
    await expect(row(page, 'b.txt')).toHaveAttribute('data-selected', 'true');
    await expect(row(page, 'row-00.txt')).toHaveAttribute('data-selected', 'true');
    await expect(page.locator('[data-testid="fm-status"]')).toContainText(/2 of \d+ selected/);

    await fm.press('ArrowUp');
    await expect(row(page, 'b.txt')).toHaveAttribute('data-selected', 'true');
    await expect(row(page, 'row-00.txt')).not.toHaveAttribute('data-selected', 'true');
    await expect(page.locator('[data-testid="fm-status"]')).not.toContainText(/selected/);

    await fm.press('ArrowUp');
    await expect(row(page, 'a.txt')).toHaveAttribute('data-selected', 'true');

    // Space toggles the cursor row out of the selection, then back in.
    await fm.press('Space');
    await expect(row(page, 'a.txt')).not.toHaveAttribute('data-selected', 'true');
    await fm.press('Space');
    await expect(row(page, 'a.txt')).toHaveAttribute('data-selected', 'true');
  });

  test('Home/End and PageDown/PageUp jump and keep the cursor row in view', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');
    await fm.press('End');
    await expect(row(page, 'row-59.txt')).toHaveAttribute('data-selected', 'true');
    await expect(row(page, 'row-59.txt')).toBeInViewport();
    await fm.press('Home');
    await expect(row(page, 'docs')).toHaveAttribute('data-selected', 'true');
    await expect(row(page, 'docs')).toBeInViewport();

    // A page from the top lands somewhere strictly inside the list.
    await fm.press('PageDown');
    const selected = page.locator('[data-testid="fm-list"] [data-selected="true"]');
    await expect(selected).toHaveCount(1);
    const name = await selected.getAttribute('data-testid');
    expect(name).not.toBe('fm-entry-docs');
    expect(name).not.toBe('fm-entry-row-59.txt');
    await expect(selected).toBeInViewport();
    await fm.press('PageUp');
    await expect(row(page, 'docs')).toHaveAttribute('data-selected', 'true');
  });

  test('ArrowRight expands then steps into a folder; ArrowLeft goes to the parent then collapses', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');
    // Selecting an unlisted folder lists it, which expands it; start from
    // the expanded state and collapse first so each arrow is unambiguous.
    await row(page, 'docs').click();
    await expect(row(page, 'inner.txt')).toBeVisible();
    await fm.press('ArrowLeft');
    await expect(row(page, 'inner.txt')).toHaveCount(0);
    await expect(row(page, 'docs')).toHaveAttribute('data-selected', 'true');
    await fm.press('ArrowRight');
    await expect(row(page, 'inner.txt')).toBeVisible();
    await expect(row(page, 'docs')).toHaveAttribute('data-selected', 'true');
    await fm.press('ArrowRight');
    await expect(row(page, 'inner.txt')).toHaveAttribute('data-selected', 'true');
    await fm.press('ArrowLeft');
    await expect(row(page, 'docs')).toHaveAttribute('data-selected', 'true');
    await expect(row(page, 'inner.txt')).toBeVisible();
    await fm.press('ArrowLeft');
    await expect(row(page, 'inner.txt')).toHaveCount(0);
    // At the top level a collapsed folder has no parent row to go to.
    await fm.press('ArrowLeft');
    await expect(row(page, 'docs')).toHaveAttribute('data-selected', 'true');
  });

  test('Enter on a file opens it via open routing (BE log + editor window); Enter on a folder toggles it', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');
    await row(page, 'docs').click();
    await expect(row(page, 'inner.txt')).toBeVisible(); // select-lists-and-expands
    await fm.press('Enter');
    await expect(row(page, 'inner.txt')).toHaveCount(0);
    await fm.press('Enter');
    await expect(row(page, 'inner.txt')).toBeVisible();

    await row(page, 'a.txt').click();
    const from = router.logCursor();
    await fm.press('Enter');
    const target = join(router.fmRoot, 'a.txt');
    await router.waitForLog(new RegExp(`fm: open path="${target.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}"`), 10_000, from);
    const editor = page.locator('wash-app-edit');
    await expect(editor).toBeVisible({ timeout: 15_000 });
    await expect(editor.locator('.cm-content')).toContainText('A', { timeout: 15_000 });
  });
});
