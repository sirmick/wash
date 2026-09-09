// Selection must follow a mutation. applySelection was never written by
// commitRename, the single-delete path or the single drag-move, so the set
// kept the OLD path and the next F2 / Delete / Ctrl+C / drag acted on a file
// that no longer existed (`not_found`). That is the mechanism the observe-only
// "[fm] selection invariant" logger had been reporting (docs/Review-findings.md,
// P1 → fm). These pin the fix from the user's side:
//   rename → the selection is re-keyed to the new path (F2 again works);
//   delete → the next sibling inherits the selection;
//   single drag-move → the selection is cleared;
// and the invariant logger must stay silent throughout.
//
// Both halves: the BE's `fm: rename/delete` audit lines and the sandbox
// filesystem confirm which path each verb actually acted on.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { existsSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

function captureViolations(page: import('@playwright/test').Page): string[] {
  const out: string[] = [];
  page.on('console', (msg) => {
    if (msg.type() === 'error' && msg.text().includes('[fm] selection invariant')) out.push(msg.text());
  });
  return out;
}

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);
}

test('F2 rename re-keys the selection: F2 again edits the new name, Delete names it', async ({ page, router }) => {
  const violations = captureViolations(page);
  await openFm(page, router);
  const fm = page.locator('wash-app-fm');

  await page.locator('[data-testid="fm-entry-hello.txt"]').click();
  await fm.press('F2');
  const input = page.locator('[data-testid="fm-rename-input"]');
  await expect(input).toBeVisible();
  await input.fill('renamed.txt');
  await input.press('Enter');
  await expect(page.locator('[data-testid="fm-entry-renamed.txt"]')).toBeVisible();
  await router.waitForLog(new RegExp(`fm: rename from="${escapeRe(join(router.fmRoot, 'hello.txt'))}" to="${escapeRe(join(router.fmRoot, 'renamed.txt'))}" replace=false ok`), 5_000);

  // The renamed row is still the selection…
  await expect(page.locator('[data-testid="fm-entry-renamed.txt"]')).toHaveAttribute('data-selected', 'true');
  // …so F2 opens the rename input on the NEW name (not a not_found error).
  await fm.press('F2');
  await expect(input).toBeVisible();
  await expect(input).toHaveValue('renamed.txt');
  await input.press('Escape');
  await expect(input).toHaveCount(0);
  await expect(page.locator('[data-testid="fm-status"]')).not.toContainText(/not_found|rename:/);

  // …and Delete confirms for the new name and removes that file.
  await fm.press('Delete');
  const overlay = page.locator('[data-testid="fm-confirm-delete"]');
  await expect(overlay).toBeVisible();
  await expect(page.locator('[data-testid="fm-confirm-delete-name"]')).toContainText('renamed.txt');
  await page.locator('[data-testid="fm-confirm-delete-yes"]').click();
  await expect(overlay).toBeHidden();
  await router.waitForLog(new RegExp(`fm: delete path="${escapeRe(join(router.fmRoot, 'renamed.txt'))}" ok`), 5_000);
  await expect(page.locator('[data-testid="fm-entry-renamed.txt"]')).toHaveCount(0);
  expect(existsSync(join(router.fmRoot, 'renamed.txt'))).toBe(false);
  expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(false);

  // Desktop-FM convention: the deleted row's neighbour inherits the
  // selection. renamed.txt was the last row, so its previous sibling wins.
  await expect(page.locator('[data-testid="fm-entry-binary.bin"]')).toHaveAttribute('data-selected', 'true');

  await page.waitForTimeout(300);
  expect(violations).toEqual([]);
});

test('renaming an expanded folder keeps its rows and re-keys the cursor', async ({ page, router }) => {
  const violations = captureViolations(page);
  await openFm(page, router);

  await page.locator('[data-testid="fm-entry-docs"]').dblclick();
  await expect(page.locator('[data-testid="fm-entry-readme.md"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(join(router.fmRoot, 'docs'));

  await page.locator('[data-testid="fm-entry-docs"]').click({ button: 'right' });
  await page.locator('[data-testid="fm-ctx-rename"]').click();
  const input = page.locator('[data-testid="fm-rename-input"]');
  await input.fill('notes');
  await input.press('Enter');

  await expect(page.locator('[data-testid="fm-entry-notes"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-docs"]')).toHaveCount(0);
  // The subtree came along: still expanded, child row still there, and the
  // cursor/path bar follow the rename.
  await expect(page.locator('[data-testid="fm-entry-readme.md"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(join(router.fmRoot, 'notes'));
  await expect(page.locator('[data-testid="fm-entry-notes"]')).toHaveAttribute('data-selected', 'true');
  expect(existsSync(join(router.fmRoot, 'notes', 'readme.md'))).toBe(true);

  await page.waitForTimeout(300);
  expect(violations).toEqual([]);
});

test('a single drag-move clears the selection so Delete has nothing stale to act on', async ({ page, router }) => {
  const violations = captureViolations(page);
  await openFm(page, router);
  const fm = page.locator('wash-app-fm');

  await page.locator('[data-testid="fm-entry-hello.txt"]').click();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toHaveAttribute('data-selected', 'true');
  await page.locator('[data-testid="fm-entry-hello.txt"]').dragTo(page.locator('[data-testid="fm-entry-docs"]'));
  await router.waitForLog(new RegExp(`fm: rename from="${escapeRe(join(router.fmRoot, 'hello.txt'))}" to="${escapeRe(join(router.fmRoot, 'docs', 'hello.txt'))}" replace=false ok`), 5_000);
  // The moved row shows up under docs (expanded by the drop).
  await expect(page.locator('[data-testid="fm-entry-docs"] ~ [data-testid="fm-entry-hello.txt"]')).toBeVisible();
  expect(existsSync(join(router.fmRoot, 'docs', 'hello.txt'))).toBe(true);

  // Nothing is selected now: no row carries the marker and Delete is inert.
  await expect(page.locator('[data-selected="true"]')).toHaveCount(0);
  await fm.press('Delete');
  await page.waitForTimeout(200);
  await expect(page.locator('[data-testid="fm-confirm-delete"]')).toHaveCount(0);
  await expect(page.locator('[data-testid="fm-status"]')).not.toContainText(/not_found|delete:/);

  expect(violations).toEqual([]);
});
