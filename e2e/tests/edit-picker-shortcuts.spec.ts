// The editor's file-level shortcuts (Ctrl+W/N/O/S) act on the tab behind
// a dialog, so they must not fire while the FilePicker (or any confirm
// prompt) owns the keyboard: Ctrl+W typed into the picker's path input
// used to close the tab under it.

import { test, expect } from '../fixtures/router';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'hello.txt'), 'hello world\n');
}

test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });

test('Ctrl+W / Ctrl+N / Ctrl+S in the picker leave the tabs alone', async ({ page, router }) => {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await editor.locator('[data-testid="edit-entry-hello.txt"]').dblclick();
  await expect(editor.locator('.cm-content')).toContainText('hello world');
  const tab = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'hello.txt')}"]`);
  // Make it dirty so a stray Ctrl+S would be visible on disk.
  await editor.locator('.cm-content').click();
  await page.keyboard.press('Control+End');
  await page.keyboard.type('edit');
  await expect(editor.locator('[data-testid="edit-status"]')).toContainText('modified');

  await page.keyboard.press('Control+o');
  const picker = page.locator('[data-testid="edit-picker"]');
  await expect(picker).toBeVisible();
  const pathInput = picker.locator('[data-testid="fp-path"]');
  await pathInput.click();
  await expect(pathInput).toBeFocused();

  await page.keyboard.press('Control+w');
  await expect(picker).toBeVisible();
  await expect(tab).toBeVisible();
  await expect(page.locator('[data-testid="edit-close-dialog"]')).toHaveCount(0);

  await page.keyboard.press('Control+n');
  await expect(editor.locator('[data-testid="edit-tab-untitled-1"]')).toHaveCount(0);

  await page.keyboard.press('Control+s');
  await expect(picker).toBeVisible();
  await expect(editor.locator('[data-testid="edit-status"]')).toContainText('modified');

  await picker.locator('[data-testid="fp-cancel"]').click();
  await expect(picker).toHaveCount(0);
  await expect(tab).toBeVisible();
  await expect(editor.locator('.cm-content')).toContainText('hello worldedit');
  // Nothing was saved behind the dialog.
  expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(true);
  expect(readFileSync(join(router.fmRoot, 'hello.txt'), 'utf8')).toBe('hello world\n');

  // Once the picker is gone the shortcut works again.
  await editor.locator('.cm-content').click();
  await page.keyboard.press('Control+w');
  await expect(page.locator('[data-testid="edit-close-dialog"]')).toBeVisible();
});
