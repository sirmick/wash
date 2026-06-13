// fm upload DnD (OS → browser) ERROR/EDGE paths beyond the happy-path
// fm-upload spec. Today: concurrent conflicting uploads must each get
// their own conflict prompt instead of the second clobbering the first.
//
// The directory-walk resilience (one unreadable file/dir in a dropped
// tree must not abort the whole drop) can't be driven from Playwright —
// webkitGetAsEntry is only populated for genuine OS drops and we can't
// synthesize an unreadable FileSystemEntry — so that path is unit-tested
// in web/fs-client/src/upload.test.ts instead.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
}

test('two conflicting uploads in flight → each gets its own prompt; both complete', async ({ page, router }) => {
  // Two pre-existing files the two uploads will collide with.
  writeFileSync(join(router.fmRoot, 'dup-a.txt'), 'origA');
  writeFileSync(join(router.fmRoot, 'dup-b.txt'), 'origB');
  await openFm(page, router);

  const overlay = page.locator('[data-testid="fm-upload-conflict"]');

  // Upload 1 collides on dup-a.txt → its conflict overlay appears.
  await page.locator('[data-testid="fm-upload-input"]')
    .setInputFiles([{ name: 'dup-a.txt', mimeType: 'text/plain', buffer: Buffer.from('NEWA') }]);
  await expect(overlay).toBeVisible();

  // Upload 2 collides on dup-b.txt WHILE upload 1's prompt is still open.
  // Before the fix this second setUploadConflict clobbered the first and
  // upload 1's await hung forever (dup-a never replaced).
  await page.locator('[data-testid="fm-upload-input"]')
    .setInputFiles([{ name: 'dup-b.txt', mimeType: 'text/plain', buffer: Buffer.from('NEWB') }]);

  // Answer the first prompt → upload 1 proceeds AND the second prompt
  // (serialized behind it) now appears. Answer that one too.
  await page.locator('[data-testid="fm-upload-conflict-replace"]').click();
  await expect(overlay).toBeVisible(); // the second prompt
  await page.locator('[data-testid="fm-upload-conflict-replace"]').click();
  await expect(overlay).toHaveCount(0);

  // Both upload jobs ran to completion and both files were replaced —
  // neither upload was lost to the clobber.
  await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 10_000);
  await expect.poll(() => readFileSync(join(router.fmRoot, 'dup-a.txt'), 'utf8'), { timeout: 5000 }).toBe('NEWA');
  await expect.poll(() => readFileSync(join(router.fmRoot, 'dup-b.txt'), 'utf8'), { timeout: 5000 }).toBe('NEWB');
});
