// fm archives (docs/Review-findings.md P2 → fm). fm could neither make an
// archive nor open one: a .zip was just an unopenable file, and bundling a
// folder meant a terminal.
//
// The context menu now offers "Extract here" for the containers the BE can
// unpack (.zip / .tar / .tar.gz / .tgz — .tar.xz is absent, there is no
// stdlib xz codec) and "Compress" / "Compress (.tar.gz)" for any
// selection. Both run as wash-bulk jobs, so they get the queue's progress
// and cancel, and extraction runs behind a zip-slip guard.
//
// Both halves: the resulting entries in the tree, and the BE audit lines
// (`fm: extract …` / `fm: compress …` plus wash-bulk's job transitions).

import { test, expect } from '../fixtures/router';
import { execFileSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  mkdirSync(join(root, 'proj', 'sub'), { recursive: true });
  writeFileSync(join(root, 'proj', 'sub', 'deep.txt'), 'deep body\n');
  writeFileSync(join(root, 'proj', 'top.txt'), 'top body\n');
  writeFileSync(join(root, 'loose.txt'), 'loose\n');
  // A real archive to extract, built with the system zip-less tar so the
  // fixture doesn't depend on the code under test.
  execFileSync('tar', ['-czf', join(root, 'bundle.tar.gz'), '-C', root, 'proj']);
  // And something the menu must NOT offer to extract.
  writeFileSync(join(root, 'notes.txt'), 'not an archive\n');
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
  await expect(page.locator('[data-testid="fm-entry-loose.txt"]')).toBeVisible();
}

const row = (page: import('@playwright/test').Page, name: string) =>
  page.locator(`[data-testid="fm-entry-${name}"]`);

test.describe('fm archives', () => {
  test.setTimeout(45_000);

  test('Extract here unpacks into a folder of its own, through bulk', async ({ page, router }) => {
    await openFm(page, router);

    const from = router.logCursor();
    await row(page, 'bundle.tar.gz').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-extract"]').click();
    await router.waitForLog(/fm: extract path=".*bundle\.tar\.gz" dest=".*\/bundle"/, 10_000, from);
    await router.waitForLog(/bulk-ops job=\S+ op=extract status=done/, 20_000, from);

    // The contents land under a folder named after the archive, not
    // strewn across the current one.
    await expect(row(page, 'bundle')).toBeVisible({ timeout: 15_000 });
    await row(page, 'bundle').dblclick();
    await expect(row(page, 'proj')).toBeVisible({ timeout: 15_000 });
  });

  test('Extract here is not offered for a file that is not an archive', async ({ page, router }) => {
    await openFm(page, router);

    await row(page, 'notes.txt').click({ button: 'right' });
    await expect(page.locator('[data-testid="fm-context-menu"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-ctx-extract"]')).toHaveCount(0);
    // …but Compress is, because anything can become an archive.
    await expect(page.locator('[data-testid="fm-ctx-compress"]')).toBeVisible();
  });

  test('Compress makes a .zip named after the folder, and it extracts back', async ({ page, router }) => {
    await openFm(page, router);

    const from = router.logCursor();
    await row(page, 'proj').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-compress"]').click();
    await router.waitForLog(/fm: compress n=1 dest=".*" name="proj\.zip"/, 10_000, from);
    await router.waitForLog(/bulk-ops job=\S+ op=compress status=done/, 20_000, from);
    await expect(row(page, 'proj.zip')).toBeVisible({ timeout: 15_000 });

    // Round trip: extracting what we just made gives the tree back.
    const from2 = router.logCursor();
    await row(page, 'proj.zip').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-extract"]').click();
    await router.waitForLog(/bulk-ops job=\S+ op=extract status=done/, 20_000, from2);
    // "proj" is taken, so the extraction folder gets a free name.
    await expect(row(page, 'proj (copy)')).toBeVisible({ timeout: 15_000 });
  });

  test('Compress (.tar.gz) bundles a multi-selection under the folder name', async ({ page, router }) => {
    await openFm(page, router);

    await row(page, 'loose.txt').click();
    await row(page, 'notes.txt').click({ modifiers: ['Control'] });
    const from = router.logCursor();
    await row(page, 'notes.txt').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-compress-targz"]').click();
    await router.waitForLog(/fm: compress n=2 dest=".*" name="\S+\.tar\.gz"/, 10_000, from);
    await router.waitForLog(/bulk-ops job=\S+ op=compress status=done/, 20_000, from);
    await expect(page.locator('[data-testid^="fm-entry-"][data-path$=".tar.gz"]')).toHaveCount(2, {
      timeout: 15_000,
    });
  });
});
