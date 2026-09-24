// The changed-on-disk prompt names the file but shows nothing of the
// change, and "Keep editing" followed by Ctrl+S overwrites the external
// edit blind. "Show diff" keeps the buffer (as Keep editing does) and
// opens the existing diff-tab machinery against the disk version so the
// user can see — and accept or reject chunk by chunk — before saving.

import { test, expect } from '../fixtures/router';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'note.txt'), 'original line\n');
}

test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });

test('external change while dirty → Show diff opens a Diff tab and keeps the edits', async ({ page, router }) => {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await editor.locator('[data-testid="edit-entry-note.txt"]').dblclick();
  await expect(editor.locator('.cm-content')).toContainText('original line');

  await editor.locator('.cm-content').click();
  await page.keyboard.press('Control+End');
  await page.keyboard.type('my local edit\n');
  const tab = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'note.txt')}"]`);
  await expect(tab).toHaveAttribute('data-dirty', 'true');

  writeFileSync(join(router.fmRoot, 'note.txt'), 'external rewrite\n');
  const dialog = editor.locator('[data-testid="edit-reload-dialog"]');
  await expect(dialog).toBeVisible({ timeout: 3_000 });

  await editor.locator('[data-testid="edit-reload-diff"]').click();
  await expect(dialog).toHaveCount(0);

  // A Diff: tab opened and is active, showing the buffer against disk.
  const diffTab = editor.locator('[data-testid^="edit-tab-diff-"]');
  await expect(diffTab).toBeVisible({ timeout: 5_000 });
  await expect(diffTab).toHaveAttribute('data-active', 'true');
  await expect(diffTab).toContainText(/^Diff: note\.txt/);
  await expect(editor.locator('.cm-content')).toContainText('my local edit');
  await expect(editor.locator('.cm-deletedChunk, .cm-changedLine, .cm-changedText').first()).toBeVisible();

  // The file tab kept its edits and stays dirty; disk was not touched.
  await tab.click();
  await expect(editor.locator('.cm-content')).toContainText('my local edit');
  await expect(tab).toHaveAttribute('data-dirty', 'true');
  expect(readFileSync(join(router.fmRoot, 'note.txt'), 'utf8')).toBe('external rewrite\n');
});
