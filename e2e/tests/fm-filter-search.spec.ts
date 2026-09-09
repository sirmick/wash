// fm type-to-filter + subtree search (docs/Review-findings.md P2 → fm).
// A bare printable key while the tree has focus opens the filter box and
// narrows the CURRENT folder's rows (case-insensitive substring, matches
// highlighted); Esc clears. Ctrl+Shift+F flips the box into a recursive
// name search under the current folder, answered by the fm BE's `search`
// walk (both halves: the result rows in the tree + the BE's `fm: search`
// audit line). Enter on a hit opens it, double-click reveals it.

import { test, expect } from '../fixtures/router';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'Report.md'), '# report\n');
  writeFileSync(join(root, 'notes.txt'), 'top-level notes\n');
  writeFileSync(join(root, 'other.txt'), 'other\n');
  mkdirSync(join(root, 'docs', 'deep'), { recursive: true });
  writeFileSync(join(root, 'docs', 'notes-2.txt'), 'nested notes body\n');
  writeFileSync(join(root, 'docs', 'deep', 'notes-3.txt'), 'deeper\n');
}

test.use({
  routerOpts: { apps: ['session', 'fm', 'bulk', 'edit'], fmRoot: true, fmSeed: seed },
});

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-notes.txt"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);
}

const row = (page: import('@playwright/test').Page, name: string) =>
  page.locator(`[data-testid="fm-entry-${name}"]`);

test.describe('fm filter + search', () => {
  test.setTimeout(30_000);

  test('typing opens the filter box and narrows the current folder; Esc clears', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');
    await fm.press('n');
    const input = page.locator('[data-testid="fm-filter-input"]');
    await expect(input).toBeVisible();
    await expect(input).toBeFocused();
    await expect(input).toHaveValue('n');
    await input.type('OT');
    // "not" matches notes.txt (and nothing else at the top level).
    await expect(row(page, 'notes.txt')).toBeVisible();
    await expect(row(page, 'other.txt')).toHaveCount(0);
    await expect(row(page, 'Report.md')).toHaveCount(0);
    await expect(row(page, 'docs')).toHaveCount(0);
    await expect(page.locator('[data-testid="fm-filter-status"]')).toHaveText('1 match');
    // The match is highlighted inside the name.
    await expect(row(page, 'notes.txt').locator('[data-testid="fm-filter-match"]')).toHaveText('not');
    await expect(page.locator('[data-testid="fm-status"]')).toContainText('1 entries');

    await input.press('Escape');
    await expect(input).toHaveCount(0);
    await expect(row(page, 'other.txt')).toBeVisible();
    await expect(row(page, 'Report.md')).toBeVisible();
    await expect(row(page, 'docs')).toBeVisible();
  });

  test('Ctrl+F opens the box; the filter is case-insensitive and reports no matches', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');
    await fm.press('Control+f');
    const input = page.locator('[data-testid="fm-filter-input"]');
    await expect(input).toBeFocused();
    await input.type('rEpOrT');
    await expect(row(page, 'Report.md')).toBeVisible();
    await expect(row(page, 'notes.txt')).toHaveCount(0);
    await input.fill('zzz');
    await expect(page.locator('[data-testid="fm-empty"]')).toHaveText('(no matches)');
    await expect(page.locator('[data-testid="fm-filter-status"]')).toHaveText('0 matches');
  });

  test('Ctrl+Shift+F searches the subtree via the BE; Enter opens a hit; double-click reveals it', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');
    await fm.press('n');
    const input = page.locator('[data-testid="fm-filter-input"]');
    await expect(input).toBeFocused();
    await input.type('otes');
    // Filter mode: only the top-level match.
    await expect(row(page, 'notes.txt')).toBeVisible();
    await expect(row(page, 'notes-2.txt')).toHaveCount(0);

    const from = router.logCursor();
    await input.press('Control+Shift+F');
    await expect(page.locator('[data-testid="fm-filter"]')).toHaveAttribute('data-search-mode', 'true');
    await expect(page.locator('[data-testid="fm-filter-subtree"]')).toHaveAttribute('aria-pressed', 'true');
    // Hits from every depth, shown with their path relative to the folder.
    await expect(row(page, 'notes-2.txt')).toBeVisible();
    await expect(row(page, 'notes-2.txt')).toContainText('docs/notes-2.txt');
    await expect(row(page, 'notes-3.txt')).toContainText('docs/deep/notes-3.txt');
    await expect(row(page, 'notes.txt')).toBeVisible();
    await expect(row(page, 'other.txt')).toHaveCount(0);
    await expect(page.locator('[data-testid="fm-filter-status"]')).toHaveText('3 found');
    // The BE half: the walk ran from the current folder with this query.
    const root = router.fmRoot.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    await router.waitForLog(new RegExp(`fm: search dir="${root}" q="notes" hits=3 visited=\\d+ skipped=0 truncated=false cancelled=false`), 10_000, from);

    // Enter on a hit opens it through open routing (the editor).
    await row(page, 'notes-2.txt').click();
    const from2 = router.logCursor();
    await input.press('Enter');
    const target = join(router.fmRoot, 'docs', 'notes-2.txt').replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    await router.waitForLog(new RegExp(`fm: open path="${target}"`), 10_000, from2);
    await expect(page.locator('wash-app-edit')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('wash-app-edit .cm-content')).toContainText('nested notes body', { timeout: 15_000 });

    // Double-click on a hit leaves search mode and reveals it in its folder.
    await page.locator('wash-app-fm').click({ position: { x: 5, y: 5 } });
    await row(page, 'notes-3.txt').dblclick();
    await expect(input).toHaveCount(0);
    await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(join(router.fmRoot, 'docs', 'deep'));
    await expect(row(page, 'notes-3.txt')).toHaveAttribute('data-selected', 'true');
    await expect(row(page, 'notes-3.txt')).not.toContainText('docs/');
  });

  test('changing the query re-runs the search and a cleared box cancels it', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');
    await fm.press('Control+Shift+F');
    const input = page.locator('[data-testid="fm-filter-input"]');
    await expect(input).toBeFocused();
    await expect(page.locator('[data-testid="fm-filter"]')).toHaveAttribute('data-search-mode', 'true');
    await input.type('deep');
    await expect(row(page, 'deep')).toContainText('docs/deep');
    await expect(page.locator('[data-testid="fm-filter-status"]')).toHaveText('1 found');
    await input.fill('notes-3');
    await expect(row(page, 'notes-3.txt')).toBeVisible();
    await expect(row(page, 'deep')).toHaveCount(0);
    await expect(page.locator('[data-testid="fm-filter-status"]')).toHaveText('1 found');
    await page.locator('[data-testid="fm-filter-close"]').click();
    await expect(input).toHaveCount(0);
    await expect(row(page, 'other.txt')).toBeVisible();
    await expect(row(page, 'notes-3.txt')).toHaveCount(0);
  });
});
