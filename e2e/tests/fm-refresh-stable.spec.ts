// A watch-driven refresh must not tear the directory's rows down.
// invalidateAndList used to delete the cached listing before re-requesting
// it; flattenTree stops at a missing listing, so every fs.watch tick
// unmounted the directory's rows and remounted them as new DOM on list_ok —
// flicker, a scroll jump, clicks racing the rebuild, and spurious
// ghost-selection logs (docs/Review-findings.md, P1 → fm). The listing is
// now kept until the fresh one lands.
//
// FE half: with a folder expanded, the list scrolled and a row selected, a
// sibling file is touched on disk (fs.watch fires, the folder is re-listed).
// The selected row's DOM element must be the SAME element afterwards (a
// marker set on it before survives), the list's scrollTop must be unchanged,
// and the selection invariant must stay silent. BE half: the router log
// shows the folder was in fact re-listed — otherwise the FE assertions
// would pass trivially.

import { test, expect } from '../fixtures/router';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seedTallFolder(root: string): void {
  writeFileSync(join(root, 'hello.txt'), 'hello world\n');
  mkdirSync(join(root, 'docs'));
  // Enough rows under docs to overflow the tree viewport several times, so
  // a teardown would visibly clamp scrollTop.
  for (let i = 0; i < 80; i++) writeFileSync(join(root, 'docs', `f-${String(i).padStart(2, '0')}.txt`), `${i}\n`);
}

test.use({ routerOpts: { fmRoot: true, fmSeed: seedTallFolder } });

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

test('fs.watch refresh keeps row DOM identity, scroll position and selection', async ({ page, router }) => {
  const violations: string[] = [];
  page.on('console', (msg) => {
    if (msg.type() === 'error' && msg.text().includes('[fm] selection invariant')) violations.push(msg.text());
  });

  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();

  const docs = join(router.fmRoot, 'docs');
  await page.locator('[data-testid="fm-entry-docs"]').dblclick();
  await expect(page.locator('[data-testid="fm-entry-f-79.txt"]')).toBeVisible();

  // Scroll deep into the folder and select a row near the bottom.
  const list = page.locator('[data-testid="fm-list"]');
  await list.evaluate((el) => el.scrollTo(0, el.scrollHeight));
  const row = page.locator('[data-testid="fm-entry-f-70.txt"]');
  await row.click();
  await expect(row).toHaveAttribute('data-selected', 'true');
  const scrollBefore = await list.evaluate((el) => el.scrollTop);
  expect(scrollBefore).toBeGreaterThan(0);
  // Stamp the live element: a remount produces a fresh node without it.
  await row.evaluate((el) => { (el as HTMLElement).dataset.e2eMarker = 'survivor'; });

  // Touch a sibling on disk → fs.watch → re-list of docs.
  const from = router.logCursor();
  writeFileSync(join(docs, 'f-05.txt'), 'changed\n');
  await router.waitForLog(new RegExp(`fm: list path="${escapeRe(docs)}"`), 10_000, from);
  // Let the list_ok land and render.
  await page.waitForTimeout(300);

  await expect(row).toHaveAttribute('data-e2e-marker', 'survivor');
  await expect(row).toHaveAttribute('data-selected', 'true');
  const scrollAfter = await list.evaluate((el) => el.scrollTop);
  expect(scrollAfter).toBe(scrollBefore);
  // The touched sibling itself is still there (its row was refreshed, not the tree).
  await expect(page.locator('[data-testid="fm-entry-f-05.txt"]')).toBeVisible();
  expect(violations).toEqual([]);
});
