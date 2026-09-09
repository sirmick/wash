// wash-edit save guards (docs/Review-findings.md 2026-09-08 P0 #1, #2, #6, #7).
//
// Each case is a file the editor must never write back wrong, and each
// assertion is both halves: what the status bar / placeholder says, AND
// the bytes still on disk after the save gesture. A guard that only
// changed the UI would pass the first and fail the second.

import { test, expect } from '../fixtures/router';
import { chmodSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const CRLF_TEXT = 'first\r\nsecond\r\n';
const LATIN1 = Buffer.from([0x63, 0x61, 0x66, 0xe9, 0x0a]); // "café\n" in Latin-1
const PNG_ISH = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x00, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01]);

function seed(root: string): void {
  writeFileSync(join(root, 'notes.txt'), 'plain\n');
  writeFileSync(join(root, 'dos.txt'), CRLF_TEXT);
  writeFileSync(join(root, 'photo.png'), PNG_ISH);
  writeFileSync(join(root, 'latin1.txt'), LATIN1);
  // One byte over the editor's 4 MiB read cap.
  writeFileSync(join(root, 'huge.log'), Buffer.alloc(4 * 1024 * 1024 + 1, 0x61));
}

test.describe('wash-edit save guards', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });
  test.setTimeout(30_000);

  async function openEditor(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
    const editor = page.locator('wash-app-edit');
    await expect(editor).toBeVisible();
    await expect(editor.locator('[data-testid="edit-entry-notes.txt"]')).toBeVisible();
    return editor;
  }

  test('Ctrl+S on a binary tab leaves the file untouched', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-photo.png"]').dblclick();
    await expect(editor.locator('[data-testid="edit-placeholder-reason"]')).toContainText('binary');
    await expect(editor.locator('[data-testid="edit-status-readonly"]')).toBeVisible();

    await editor.press('Control+s');
    await expect(editor.locator('[data-testid="edit-status-error"]')).toContainText('not editable');
    // BE half: no write reached the disk.
    await page.waitForTimeout(300);
    expect(readFileSync(join(router.fmRoot, 'photo.png'))).toEqual(PNG_ISH);
    expect(router.log()).not.toMatch(/wash-edit: write/);
  });

  test('a file over the read cap opens as a placeholder, not a truncated buffer', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-huge.log"]').dblclick();
    await expect(editor.locator('[data-testid="edit-placeholder-reason"]')).toContainText('over the editor');
    await editor.press('Control+s');
    await expect(editor.locator('[data-testid="edit-status-error"]')).toContainText('not editable');
    await page.waitForTimeout(300);
    expect(readFileSync(join(router.fmRoot, 'huge.log')).length).toBe(4 * 1024 * 1024 + 1);
  });

  test('a non-UTF-8 file is refused rather than saved with replacement characters', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-latin1.txt"]').dblclick();
    await expect(editor.locator('[data-testid="edit-placeholder-reason"]')).toContainText('UTF-8');
    await editor.press('Control+s');
    await page.waitForTimeout(300);
    expect(readFileSync(join(router.fmRoot, 'latin1.txt'))).toEqual(LATIN1);
  });

  test('a CRLF file stays CRLF across an edit and save, and goes clean', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-dos.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('second');
    await expect(editor.locator('[data-testid="edit-status-eol"]')).toContainText('CRLF');
    // Untouched: opening must not make it dirty (the raw baseline used to).
    await expect(editor.locator('[data-testid="edit-status"]')).not.toContainText('modified');

    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('third');
    await expect(editor.locator('[data-testid="edit-status"]')).toContainText('modified');
    await editor.press('Control+s');
    await expect(editor.locator('[data-testid="edit-status"]')).not.toContainText('modified');

    const onDisk = readFileSync(join(router.fmRoot, 'dos.txt'), 'latin1');
    expect(onDisk).toBe('first\r\nsecond\r\nthird');
    expect(onDisk).not.toMatch(/[^\r]\n/);
  });

  test('a failed save says so in the status bar and as a toast', async ({ page, router }) => {
    test.skip(process.getuid?.() === 0, 'root ignores file modes');
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-notes.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('plain');
    // Make it read-only AFTER opening so the read succeeded.
    chmodSync(join(router.fmRoot, 'notes.txt'), 0o444);
    // A rename over a read-only file in a writable dir would succeed, so
    // deny the directory too — the tmp file cannot be created.
    chmodSync(router.fmRoot, 0o555);
    try {
      await editor.locator('.cm-content').click();
      await page.keyboard.type('x');
      await editor.press('Control+s');
      await expect(editor.locator('[data-testid="edit-status-error"]')).toContainText('save failed');
      await expect(editor.locator('[data-testid="edit-status"]')).toContainText('modified');
      await expect(page.locator('[data-testid="notification"]').filter({ hasText: 'Save failed' })).toBeVisible();
      await router.waitForLog(/wash-edit: write failed path=/, 5_000);
      expect(readFileSync(join(router.fmRoot, 'notes.txt'), 'utf8')).toBe('plain\n');
    } finally {
      chmodSync(router.fmRoot, 0o755);
      chmodSync(join(router.fmRoot, 'notes.txt'), 0o644);
    }
  });

  test('a file that will not open says why instead of doing nothing', async ({ page, router }) => {
    test.skip(process.getuid?.() === 0, 'root ignores file modes');
    chmodSync(join(router.fmRoot, 'notes.txt'), 0o000);
    try {
      const editor = await openEditor(page, router);
      await editor.locator('[data-testid="edit-entry-notes.txt"]').dblclick();
      await expect(editor.locator('[data-testid="edit-status-error"]')).toContainText('cannot open notes.txt');
      await expect(editor.locator('[data-testid^="edit-tab-"]')).toHaveCount(0);
    } finally {
      chmodSync(join(router.fmRoot, 'notes.txt'), 0o644);
    }
  });
});
