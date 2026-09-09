// wash-edit read-only awareness (docs/Review-findings.md P2 → edit).
//
// The editor never looked at a file's mode: a file it could not write
// opened like any other, and the first hint was a "save failed" after
// the edit had been made. Now the read reply carries `writable`, the
// status bar carries a lock, and Ctrl+S offers Save As instead of
// throwing the write at a file that will refuse it.
//
// Skipped under root, which can write anything regardless of mode — and
// so, correctly, sees no lock.

import { test, expect } from '../fixtures/router';
import { chmodSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'locked.txt'), 'sealed\n');
  writeFileSync(join(root, 'open.txt'), 'ordinary\n');
  chmodSync(join(root, 'locked.txt'), 0o444);
}

type Page = import('@playwright/test').Page;
type Router = import('../fixtures/router').RouterHandle;

async function openEditor(page: Page, router: Router) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor).toBeVisible();
  await expect(editor.locator('[data-testid="edit-entry-open.txt"]')).toBeVisible();
  return editor;
}

test.describe('wash-edit read-only files', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });
  test.setTimeout(40_000);

  test('a mode-0444 file shows a lock and Ctrl+S routes to Save As', async ({ page, router }) => {
    test.skip(process.getuid?.() === 0, 'root ignores file modes');
    const editor = await openEditor(page, router);

    // An ordinary file is not marked.
    await editor.locator('[data-testid="edit-entry-open.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('ordinary');
    await expect(editor.locator('[data-testid="edit-status-lock"]')).toHaveCount(0);

    await editor.locator('[data-testid="edit-entry-locked.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('sealed');
    await expect(editor.locator('[data-testid="edit-status-lock"]')).toBeVisible();
    await expect(editor.locator('[data-testid="edit-status-lock"]')).toContainText('read-only');
    // Still an editable buffer — the file is what is read-only, not the
    // tab (unlike a binary, which has no buffer at all).
    await editor.locator('.cm-content').click();
    await page.keyboard.type('x');
    await expect(editor.locator('.cm-content')).toContainText('sealedx');

    await editor.press('Control+s');
    await expect(editor.locator('[data-testid="edit-status-error"]')).toContainText('read-only file — Save As?');
    const picker = page.locator('[data-testid="edit-picker"]');
    await expect(picker).toBeVisible();
    // The original is untouched: nothing was thrown at the BE.
    expect(readFileSync(join(router.fmRoot, 'locked.txt'), 'utf8')).toBe('sealed\n');

    // Saving elsewhere works and clears the lock — the new file is the
    // tab's file now.
    await picker.locator('[data-testid="fp-save-name"]').fill('copy.txt');
    await picker.locator('[data-testid="fp-confirm"]').click();
    await expect(picker).toHaveCount(0);
    await expect.poll(() => readFileSync(join(router.fmRoot, 'copy.txt'), 'utf8')).toBe('sealed\nx');
    await expect(editor.locator('[data-testid="edit-status-lock"]')).toHaveCount(0);
  });
});
