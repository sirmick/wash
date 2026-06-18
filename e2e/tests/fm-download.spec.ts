// fm browser download, full-stack:
//
//   context menu → download_begin (JSON) → fm opens a raw channel
//   → streams the file (or a zip of a multi-selection) → download_done
//   → FE concatenates the chunks into a Blob and clicks a synthetic
//   <a download> → the browser saves it. A "Download ready" toast fires
//   via the notify helpers.
//
// We assert the real browser download (Playwright's download event) so
// the test proves bytes reached the browser, plus the BE log line and
// the success toast. Per [[wash e2e pattern]].

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { writeFileSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);
}

test('single file → browser download with exact bytes + toast', async ({ page, router }) => {
  const body = 'download me, byte-for-byte\n';
  writeFileSync(join(router.fmRoot, 'doc.txt'), body);
  await openFm(page, router);

  await page.locator('[data-testid="fm-entry-doc.txt"]').click({ button: 'right' });
  const downloadPromise = page.waitForEvent('download');
  await page.locator('[data-testid="fm-ctx-download"]').click();

  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe('doc.txt');
  const path = await download.path();
  expect(readFileSync(path!, 'utf8')).toBe(body);

  await router.waitForLog(/wash-fm download id=\S+ status=done file="doc.txt" zip=false/, 5_000);
  await expect(
    page.locator('[data-testid="notification"][data-level="info"] [data-testid="notification-title"]').last(),
  ).toHaveText('Download ready', { timeout: 5_000 });
});

test('multi-select → single zip download', async ({ page, router }) => {
  writeFileSync(join(router.fmRoot, 'z-a.txt'), 'a');
  writeFileSync(join(router.fmRoot, 'z-b.txt'), 'b');
  await openFm(page, router);

  // Select both rows.
  await page.locator('[data-testid="fm-entry-z-a.txt"]').click();
  await page.locator('[data-testid="fm-entry-z-b.txt"]').click({ modifiers: ['Control'] });

  // Right-click a selected row — the menu item reflects the count.
  await page.locator('[data-testid="fm-entry-z-a.txt"]').click({ button: 'right' });
  await expect(page.locator('[data-testid="fm-ctx-download"]')).toContainText('Download 2 items');

  const downloadPromise = page.waitForEvent('download');
  await page.locator('[data-testid="fm-ctx-download"]').click();

  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe('wash-download.zip');
  // A non-empty zip lands on disk (PK magic).
  const path = await download.path();
  const buf = readFileSync(path!);
  expect(buf.length).toBeGreaterThan(0);
  expect(buf.subarray(0, 2).toString('latin1')).toBe('PK');

  await router.waitForLog(/wash-fm download id=\S+ status=done file="wash-download.zip" zip=true/, 5_000);
});

test('directory → zipped download named <dir>.zip', async ({ page, router }) => {
  await openFm(page, router);
  // seedSimpleTree provides a 'docs' directory.
  await page.locator('[data-testid="fm-entry-docs"]').click({ button: 'right' });

  const downloadPromise = page.waitForEvent('download');
  await page.locator('[data-testid="fm-ctx-download"]').click();

  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe('docs.zip');
  await router.waitForLog(/wash-fm download id=\S+ status=done file="docs.zip" zip=true/, 5_000);
});
