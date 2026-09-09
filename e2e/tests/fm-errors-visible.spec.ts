// Two failures that used to vanish (docs/Review-findings.md, P1 → fm):
//
//  (a) A FAILED bulk job disappeared from fm's Jobs strip the moment it
//      failed — the strip filtered to queued/running only — so the error
//      text was never seen. It now stays, with its error, until dismissed.
//  (b) A path-bar typo was committed eagerly: path() and the Back history
//      took the bad path before the BE said list_err. Now list_err rolls
//      path()/history back to the previous good location while the typed
//      text stays in the bar for correction.
//
// Both halves each time: the bulk / fm BE audit lines and the FE state.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { join } from 'node:path';

test.use({ routerOpts: { apps: ['session', 'fm', 'bulk', 'notify'], fmRoot: true, fmSeed: seedSimpleTree } });

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Files', exact: true }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);
}

test('a failed bulk job stays in the strip with its error until dismissed', async ({ page, router }) => {
  test.setTimeout(60_000);
  await openFm(page, router);
  const fm = page.locator('wash-app-fm').first();

  // Enqueue straight into the service, the way fm-jobs.spec.ts does: a copy
  // whose source does not exist fails as soon as the worker counts it.
  const bulk = await router.controlRequest({ t: 'launch', app_id: 'com.wash.bulk' });
  const bulkInst = String(bulk.instance_id ?? '');
  expect(bulkInst).not.toBe('');
  const reply = await router.sendAppMsg(bulkInst, {
    kind: 'enqueue', id: 'fail-me', op: 'copy',
    paths: [join(router.fmRoot, 'nope')], dest: join(router.fmRoot, 'docs'),
  });
  expect(reply.kind).toBe('enqueue_ok');
  const jobID = String(reply.job_id);
  await router.waitForLog(new RegExp(`bulk-ops job=${escapeRe(jobID)} .*status=failed`), 20_000);

  // FE: the failed row is there, red-lettered with the error, and stays.
  const strip = fm.locator('[data-testid="fm-jobs"]');
  await expect(strip).toBeVisible({ timeout: 20_000 });
  const row = strip.locator(`[data-testid="fm-failed-job-${jobID}"]`);
  await expect(row).toBeVisible({ timeout: 20_000 });
  await expect(row).toContainText('failed');
  await expect(row).toContainText(/nope/);
  await page.waitForTimeout(1_500);
  await expect(row).toBeVisible();

  // Dismiss → gone (and the strip folds away with nothing else in it).
  await row.locator(`[data-testid="fm-dismiss-job-${jobID}"]`).click();
  await expect(row).toHaveCount(0);
  await expect(strip).toHaveCount(0);
});

test('a path-bar typo does not move the cursor or pollute Back history', async ({ page, router }) => {
  await openFm(page, router);
  const bar = page.locator('[data-testid="fm-path"]');
  const status = page.locator('[data-testid="fm-status"]');
  const docs = join(router.fmRoot, 'docs');

  // History: root → docs.
  await page.locator('[data-testid="fm-entry-docs"]').dblclick();
  await expect(bar).toHaveValue(docs);
  await expect(page.locator('[data-testid="fm-entry-readme.md"]')).toBeVisible();

  const bad = join(router.fmRoot, 'nope');
  const from = router.logCursor();
  await bar.fill(bad);
  await bar.press('Enter');
  await router.waitForLog(new RegExp(`fm: list path="${escapeRe(bad)}": .*no such file`), 10_000, from);

  // Error shown; the typed text stays for correction.
  await expect(status.locator('[data-status-kind="error"]')).toBeVisible();
  await expect(status).toContainText(/error:/);
  await expect(bar).toHaveValue(bad);
  // The tree still shows docs' rows: the cursor did not move.
  await expect(page.locator('[data-testid="fm-entry-readme.md"]')).toBeVisible();

  // Back goes to root — not to docs, which is where it would land had the
  // bad path been pushed on top of docs.
  await page.locator('[data-testid="fm-back"]').click();
  await expect(bar).toHaveValue(router.fmRoot);
  await page.locator('[data-testid="fm-forward"]').click();
  await expect(bar).toHaveValue(docs);
});
