// Listing truncation must be visible. The fm BE caps a listing at 5,000
// entries and sets `truncated`; the FE used to ignore the flag, so a huge
// directory silently showed a subset (docs/Review-findings.md, P1 → fm).
//
// FE half: the status line says how many of how many are shown. BE half:
// the `fm: list` audit line carries n / truncated / total for the same dir.

import { test, expect } from '../fixtures/router';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const TOTAL = 5_050;

function seedHugeDir(root: string): void {
  writeFileSync(join(root, 'hello.txt'), 'hello world\n');
  mkdirSync(join(root, 'big'));
  for (let i = 0; i < TOTAL; i++) writeFileSync(join(root, 'big', `f-${String(i).padStart(4, '0')}`), '');
}

test.use({ routerOpts: { fmRoot: true, fmSeed: seedHugeDir } });

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

test('a truncated listing says so in the status line', async ({ page, router }) => {
  test.setTimeout(60_000);
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
  // Not truncated at the root: the plain entry count.
  await expect(page.locator('[data-testid="fm-status"]')).toHaveText(/^\d+ entries$/);

  const big = join(router.fmRoot, 'big');
  const from = router.logCursor();
  const bar = page.locator('[data-testid="fm-path"]');
  await bar.fill(big);
  await bar.press('Enter');

  await router.waitForLog(new RegExp(`fm: list path="${escapeRe(big)}" n=5000 truncated=true total=${TOTAL}`), 30_000, from);
  const status = page.locator('[data-testid="fm-status"]');
  await expect(status).toContainText('showing first 5,000 of 5,050 entries', { timeout: 30_000 });
  await expect(status.locator('[data-status-kind="truncated"]')).toBeVisible();
  // Exactly the cap rendered (which 5,000 is readdir order — the BE caps
  // before it sorts — so count, don't name).
  await expect(page.locator('[data-testid^="fm-entry-f-"]')).toHaveCount(5000);

  // Back at the root the capped folder is still expanded in the tree, so
  // the notice stays, now named, next to the entry count; collapsing the
  // folder clears it.
  await page.locator('[data-testid="fm-home"]').click();
  await expect(bar).toHaveValue(router.fmRoot);
  await expect(status).toContainText(/\d+ entries · big: showing first 5,000 of 5,050 entries/);
  await page.locator('[data-testid="fm-chevron-big"]').click();
  await expect(status).not.toContainText('showing first');
  await expect(status).toHaveText(/^\d+ entries$/);
});
