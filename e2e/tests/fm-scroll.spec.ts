// fm scroll-into-view: navigating the cursor (Home / Back / Up / path-bar /
// double-click) must bring the target row into the tree viewport, not just
// re-bold it in place. Regression for "clicked Home, it navigated but the
// tree didn't move" — on a big tree the target row was off-screen and the
// list never scrolled, so it looked like nothing happened.

import { test, expect } from '../fixtures/router';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seedMany(root: string): void {
  // Enough top-level dirs to overflow the tree viewport several times over.
  for (let i = 0; i < 60; i++) mkdirSync(join(root, `dir-${String(i).padStart(2, '0')}`));
  writeFileSync(join(root, 'dir-59', 'target.txt'), 'x\n');
}

test.use({ routerOpts: { apps: ['session', 'about', 'fm'], fmRoot: true, fmSeed: seedMany } });

async function openFm(
  page: import('@playwright/test').Page,
  router: import('../fixtures/router').RouterHandle,
) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-dir-00"]')).toBeVisible();
}

test('navigating to an off-screen folder scrolls it into the tree viewport', async ({ page, router }) => {
  await openFm(page, router);

  // Pin the tree to the very top so the last dir is well below the fold.
  await page.locator('[data-testid="fm-list"]').evaluate((el) => el.scrollTo(0, 0));

  // Navigate to the last directory via the path bar (same selectPath cursor
  // move the Home button uses).
  const bar = page.locator('[data-testid="fm-path"]');
  await bar.fill(join(router.fmRoot, 'dir-59'));
  await bar.press('Enter');
  // Its listing arriving (target.txt) is the sync barrier.
  await expect(page.locator('[data-testid="fm-entry-target.txt"]')).toBeVisible({ timeout: 5_000 });

  // The dir-59 row must now sit within the list's scroll viewport.
  const inView = await page.evaluate(() => {
    const list = document.querySelector('wash-app-fm [data-testid="fm-list"]');
    const row = document.querySelector('wash-app-fm [data-path$="/dir-59"]');
    if (!list || !row) return false;
    const lr = list.getBoundingClientRect();
    const rr = row.getBoundingClientRect();
    return rr.top >= lr.top - 1 && rr.bottom <= lr.bottom + 1;
  });
  expect(inView).toBe(true);
});
