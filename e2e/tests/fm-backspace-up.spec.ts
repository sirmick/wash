// Backspace navigates up one level — the convention every desktop file
// manager follows. fm used to treat Backspace like Delete (the confirm
// dialog being the only backstop) (docs/Review-findings.md, P1 → fm).
// Delete keeps deleting.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { existsSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);
}

test('Backspace in a subfolder returns to the parent and deletes nothing', async ({ page, router }) => {
  await openFm(page, router);
  const fm = page.locator('wash-app-fm');
  const bar = page.locator('[data-testid="fm-path"]');

  await page.locator('[data-testid="fm-entry-docs"]').dblclick();
  await expect(bar).toHaveValue(join(router.fmRoot, 'docs'));
  await expect(page.locator('[data-testid="fm-entry-readme.md"]')).toBeVisible();

  await fm.press('Backspace');
  await expect(bar).toHaveValue(router.fmRoot);
  await expect(page.locator('[data-testid="fm-confirm-delete"]')).toHaveCount(0);
  expect(existsSync(join(router.fmRoot, 'docs', 'readme.md'))).toBe(true);
});

test('Backspace with a selected file goes to its folder; Delete still asks to delete', async ({ page, router }) => {
  await openFm(page, router);
  const fm = page.locator('wash-app-fm');
  const bar = page.locator('[data-testid="fm-path"]');

  await page.locator('[data-testid="fm-entry-docs"]').dblclick();
  await page.locator('[data-testid="fm-entry-readme.md"]').click();
  await expect(bar).toHaveValue(join(router.fmRoot, 'docs', 'readme.md'));

  await fm.press('Backspace');
  await expect(bar).toHaveValue(join(router.fmRoot, 'docs'));
  await expect(page.locator('[data-testid="fm-confirm-delete"]')).toHaveCount(0);
  expect(existsSync(join(router.fmRoot, 'docs', 'readme.md'))).toBe(true);

  // The selection is cleared by navigation; re-select and prove Delete is
  // still the delete key.
  await page.locator('[data-testid="fm-entry-readme.md"]').click();
  await fm.press('Delete');
  await expect(page.locator('[data-testid="fm-confirm-delete"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-confirm-delete-name"]')).toContainText('readme.md');
  await page.locator('[data-testid="fm-confirm-delete-cancel"]').click();
  expect(existsSync(join(router.fmRoot, 'docs', 'readme.md'))).toBe(true);
});
