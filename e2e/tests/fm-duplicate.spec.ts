// fm Duplicate (docs/Review-findings.md P2 → fm). "Give me another one of
// these, right here" needed Copy, then Paste, then a rename — and Paste
// into the source's own folder was rejected outright ("already in").
//
// Ctrl+D (and a context-menu Duplicate) now names the copy with
// bulkops.UniqueCopyName — "x (copy).txt", then "x (copy 2).txt" — and
// hands the copying to wash-bulk as a named job (bulkops.Job.Names), so a
// duplicated folder gets the queue's progress and cancel.
//
// Both halves: the new rows in the tree, and the fm BE's
// `fm: duplicate n=… dest=… names=[…]` audit line.

import { test, expect } from '../fixtures/router';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'notes.txt'), 'note body\n');
  writeFileSync(join(root, 'other.txt'), 'other body\n');
  mkdirSync(join(root, 'photos', 'sub'), { recursive: true });
  writeFileSync(join(root, 'photos', 'sub', 'deep.txt'), 'deep\n');
}

test.use({
  routerOpts: { apps: ['session', 'fm', 'bulk'], fmRoot: true, fmSeed: seed },
});

async function openFm(
  page: import('@playwright/test').Page,
  router: import('../fixtures/router').RouterHandle,
) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-notes.txt"]')).toBeVisible();
}

const row = (page: import('@playwright/test').Page, name: string) =>
  page.locator(`[data-testid="fm-entry-${name}"]`);

test.describe('fm duplicate', () => {
  test.setTimeout(45_000);

  test('Ctrl+D makes a sibling copy, and again counts up', async ({ page, router }) => {
    await openFm(page, router);

    await row(page, 'notes.txt').click();
    const from = router.logCursor();
    await page.locator('wash-app-fm').press('Control+d');
    await router.waitForLog(/fm: duplicate n=1 dest=.* names=\[notes \(copy\)\.txt\]/, 10_000, from);
    await expect(row(page, 'notes (copy).txt')).toBeVisible({ timeout: 15_000 });

    // Duplicating again counts up instead of stacking the suffix.
    await row(page, 'notes.txt').click();
    const from2 = router.logCursor();
    await page.locator('wash-app-fm').press('Control+d');
    await router.waitForLog(/fm: duplicate n=1 dest=.* names=\[notes \(copy 2\)\.txt\]/, 10_000, from2);
    await expect(row(page, 'notes (copy 2).txt')).toBeVisible({ timeout: 15_000 });

    // …and duplicating the copy itself joins the same sequence.
    await row(page, 'notes (copy).txt').click();
    const from3 = router.logCursor();
    await page.locator('wash-app-fm').press('Control+d');
    await router.waitForLog(/fm: duplicate n=1 dest=.* names=\[notes \(copy 3\)\.txt\]/, 10_000, from3);
    await expect(row(page, 'notes (copy 3).txt')).toBeVisible({ timeout: 15_000 });
  });

  test('the context menu duplicates a folder, contents and all, through bulk', async ({ page, router }) => {
    await openFm(page, router);

    const from = router.logCursor();
    await row(page, 'photos').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-duplicate"]').click();
    await router.waitForLog(/fm: duplicate n=1 dest=.* names=\[photos \(copy\)\]/, 10_000, from);
    // wash-bulk ran it as a real copy job.
    await router.waitForLog(/bulk-ops job=\S+ op=copy status=done/, 15_000, from);
    await expect(row(page, 'photos (copy)')).toBeVisible({ timeout: 15_000 });

    // The tree copy is a real recursive copy.
    await row(page, 'photos (copy)').dblclick();
    await expect(row(page, 'sub')).toBeVisible({ timeout: 15_000 });
  });

  test('a multi-selection duplicates every item in one job', async ({ page, router }) => {
    await openFm(page, router);

    await row(page, 'notes.txt').click();
    await row(page, 'other.txt').click({ modifiers: ['Control'] });
    const from = router.logCursor();
    await page.locator('wash-app-fm').press('Control+d');
    await router.waitForLog(
      /fm: duplicate n=2 dest=.* names=\[notes \(copy\)\.txt other \(copy\)\.txt\]/,
      10_000,
      from,
    );
    await expect(row(page, 'notes (copy).txt')).toBeVisible({ timeout: 15_000 });
    await expect(row(page, 'other (copy).txt')).toBeVisible({ timeout: 15_000 });
  });
});
