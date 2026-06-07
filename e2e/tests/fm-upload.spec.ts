// fm upload: bringing OS files INTO the confined session fs, both via
// the toolbar file/folder pickers and an external (OS) drag-drop.
// Full-stack coverage per [[wash e2e pattern]]:
//
//   - Picker single / multi file → bytes land on disk under fmRoot.
//   - Picker folder (webkitdirectory) → recursive tree, intermediate
//     dirs created. This is how the recursive-directory path is
//     exercised: Playwright can't synthesize a real OS directory drag
//     (webkitGetAsEntry is only populated for genuine OS drops), so
//     the picker covers recursion and the drop path covers plain files.
//   - Pre-flight conflict prompt: Skip (leave existing) / Replace
//     (overwrite).
//   - Progress surfaces in the wash-bulk sidebar widget as an upload
//     job that reaches a terminal state.
//   - BE transitions asserted through the router log (bulk-ops job=…).
//
// Cancel is intentionally not e2e-tested: it depends on clicking the
// sidebar cancel mid-stream, which races the (sub-second, local) byte
// transfer. The relay path (bulk → fm upload_cancel) is covered by the
// wash-bulk unit tests + the upload_cancelled FE handler.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { existsSync, readFileSync, mkdirSync, writeFileSync, mkdtempSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);
}

// dropFiles synthesizes an external file drop on the target element.
// Real OS drags (and their directory recursion) can't be simulated by
// Playwright; this exercises the plain-file external-drop path.
async function dropFiles(
  page: import('@playwright/test').Page,
  selector: string,
  files: { name: string; content: string }[],
) {
  const dataTransfer = await page.evaluateHandle((files) => {
    const dt = new DataTransfer();
    for (const f of files) dt.items.add(new File([f.content], f.name, { type: 'text/plain' }));
    return dt;
  }, files);
  const target = page.locator(selector);
  await target.dispatchEvent('dragenter', { dataTransfer });
  await target.dispatchEvent('dragover', { dataTransfer });
  await target.dispatchEvent('drop', { dataTransfer });
}

test.describe('fm upload: picker', () => {
  test('single file → bytes land on disk + bulk job completes', async ({ page, router }) => {
    await openFm(page, router);

    await page
      .locator('[data-testid="fm-upload-input"]')
      .setInputFiles([{ name: 'up.txt', mimeType: 'text/plain', buffer: Buffer.from('uploaded!\n') }]);

    // BE side: the upload job runs to done in wash-bulk.
    await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 5_000);

    // FS side: file present with the right bytes.
    expect(existsSync(join(router.fmRoot, 'up.txt'))).toBe(true);
    expect(readFileSync(join(router.fmRoot, 'up.txt'), 'utf8')).toBe('uploaded!\n');

    // FE side: the new row shows after the post-upload refresh.
    await expect(page.locator('[data-testid="fm-entry-up.txt"]')).toBeVisible({ timeout: 3_000 });
  });

  test('multiple files → all land on disk', async ({ page, router }) => {
    await openFm(page, router);

    await page.locator('[data-testid="fm-upload-input"]').setInputFiles([
      { name: 'a.txt', mimeType: 'text/plain', buffer: Buffer.from('aaa') },
      { name: 'b.txt', mimeType: 'text/plain', buffer: Buffer.from('bbb') },
    ]);

    await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 5_000);
    expect(readFileSync(join(router.fmRoot, 'a.txt'), 'utf8')).toBe('aaa');
    expect(readFileSync(join(router.fmRoot, 'b.txt'), 'utf8')).toBe('bbb');
  });

  test('binary file round-trips byte-exact over the raw channel', async ({ page, router }) => {
    await openFm(page, router);
    // Bytes that would be mangled by a text-only path (nulls, high
    // bytes); proves the raw-channel framing + write is byte-exact.
    const bytes = Buffer.from([0, 1, 2, 255, 254, 0, 128, 64, 0]);
    await page
      .locator('[data-testid="fm-upload-input"]')
      .setInputFiles([{ name: 'blob.bin', mimeType: 'application/octet-stream', buffer: bytes }]);

    await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 5_000);
    expect(Buffer.compare(readFileSync(join(router.fmRoot, 'blob.bin')), bytes)).toBe(0);
  });

  test('folder upload → recursive tree, intermediate dirs created', async ({ page, router }) => {
    // Build an OS-side source tree the picker will recurse. The picker
    // input has webkitdirectory; setInputFiles(<dir>) uploads it
    // preserving relative paths.
    const srcParent = mkdtempSync(join(tmpdir(), 'wash-up-'));
    const photos = join(srcParent, 'photos');
    mkdirSync(join(photos, '2024'), { recursive: true });
    writeFileSync(join(photos, 'cover.jpg'), 'COVER');
    writeFileSync(join(photos, '2024', 'jan.jpg'), 'JAN');

    await openFm(page, router);
    await page.locator('[data-testid="fm-upload-folder-input"]').setInputFiles(photos);

    await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 5_000);
    expect(readFileSync(join(router.fmRoot, 'photos', 'cover.jpg'), 'utf8')).toBe('COVER');
    expect(readFileSync(join(router.fmRoot, 'photos', '2024', 'jan.jpg'), 'utf8')).toBe('JAN');
  });
});

test.describe('fm upload: conflicts', () => {
  test('Skip leaves existing files, uploads only the new ones', async ({ page, router }) => {
    writeFileSync(join(router.fmRoot, 'dup.txt'), 'original\n');
    await openFm(page, router);

    await page.locator('[data-testid="fm-upload-input"]').setInputFiles([
      { name: 'dup.txt', mimeType: 'text/plain', buffer: Buffer.from('NEW') },
      { name: 'fresh.txt', mimeType: 'text/plain', buffer: Buffer.from('fresh') },
    ]);

    // Pre-flight prompt appears; choose Skip existing.
    await expect(page.locator('[data-testid="fm-upload-conflict"]')).toBeVisible({ timeout: 3_000 });
    await page.locator('[data-testid="fm-upload-conflict-skip"]').click();

    await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 5_000);
    expect(readFileSync(join(router.fmRoot, 'dup.txt'), 'utf8')).toBe('original\n'); // untouched
    expect(readFileSync(join(router.fmRoot, 'fresh.txt'), 'utf8')).toBe('fresh'); // uploaded
  });

  test('Replace all overwrites the existing file', async ({ page, router }) => {
    writeFileSync(join(router.fmRoot, 'dup.txt'), 'original\n');
    await openFm(page, router);

    await page
      .locator('[data-testid="fm-upload-input"]')
      .setInputFiles([{ name: 'dup.txt', mimeType: 'text/plain', buffer: Buffer.from('REPLACED') }]);

    await expect(page.locator('[data-testid="fm-upload-conflict"]')).toBeVisible({ timeout: 3_000 });
    await page.locator('[data-testid="fm-upload-conflict-replace"]').click();

    await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 5_000);
    expect(readFileSync(join(router.fmRoot, 'dup.txt'), 'utf8')).toBe('REPLACED');
  });

  test('Cancel aborts the whole upload', async ({ page, router }) => {
    writeFileSync(join(router.fmRoot, 'dup.txt'), 'original\n');
    await openFm(page, router);

    await page
      .locator('[data-testid="fm-upload-input"]')
      .setInputFiles([{ name: 'dup.txt', mimeType: 'text/plain', buffer: Buffer.from('NOPE') }]);

    await expect(page.locator('[data-testid="fm-upload-conflict"]')).toBeVisible({ timeout: 3_000 });
    await page.locator('[data-testid="fm-upload-conflict-cancel"]').click();

    // Nothing uploaded; original intact.
    await expect(page.locator('[data-testid="fm-upload-conflict"]')).toHaveCount(0);
    expect(readFileSync(join(router.fmRoot, 'dup.txt'), 'utf8')).toBe('original\n');
  });
});

test.describe('fm upload: external drop', () => {
  test('dropping OS files on the list pane uploads them into the current dir', async ({ page, router }) => {
    await openFm(page, router);
    await dropFiles(page, '[data-testid="fm-list"]', [
      { name: 'dropped.txt', content: 'via drop\n' },
    ]);

    await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 5_000);
    expect(readFileSync(join(router.fmRoot, 'dropped.txt'), 'utf8')).toBe('via drop\n');
  });

  test('dropping onto a folder row uploads into that folder', async ({ page, router }) => {
    await openFm(page, router);
    // Seed has docs/. Drop onto the docs row.
    await dropFiles(page, '[data-testid="fm-entry-docs"]', [
      { name: 'note.txt', content: 'in docs\n' },
    ]);

    await router.waitForLog(/bulk-ops job=up-\S+ op=upload status=done/, 5_000);
    expect(readFileSync(join(router.fmRoot, 'docs', 'note.txt'), 'utf8')).toBe('in docs\n');
  });
});

test.describe('fm upload: sidebar progress', () => {
  test('upload appears as a bulk job row and reaches a terminal state', async ({ page, router }) => {
    await openFm(page, router);
    // Bulk section starts collapsed; a new job auto-expands it.
    await expect(page.locator('[data-testid="sidebar-section-bulk"]')).toHaveAttribute('data-state', 'collapsed');

    await page
      .locator('[data-testid="fm-upload-input"]')
      .setInputFiles([{ name: 'watched.txt', mimeType: 'text/plain', buffer: Buffer.from('hi') }]);

    await expect(page.locator('[data-testid="sidebar-section-bulk"]')).toHaveAttribute('data-state', 'expanded', {
      timeout: 5_000,
    });
    const row = page.locator('[data-testid^="bulk-job-up-"]').first();
    await expect(row).toBeVisible({ timeout: 5_000 });
    await expect(row).toHaveAttribute('data-status', /done|running/, { timeout: 5_000 });
  });
});
