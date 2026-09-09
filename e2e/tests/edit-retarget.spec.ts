// An open tab follows its file. When the editor itself renames or moves
// the file (F2 in the sidebar, a tree drop) the tab is re-keyed to the new
// path and keeps its buffer + dirty state; when something ELSE removes the
// file from under a tab, the status bar says so and Ctrl+S goes through
// the picker instead of silently recreating the old path.
//
// Both halves: the tab testid/status (FE) and what ends up on disk (BE).

import { test, expect } from '../fixtures/router';
import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';

function seed(root: string): void {
  writeFileSync(join(root, 'hello.txt'), 'hello world\n');
  mkdirSync(join(root, 'docs'));
}

test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });

async function openEditor(page: Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor.locator('[data-testid="edit-entry-hello.txt"]')).toBeVisible();
  return editor;
}

test.describe('wash-edit: tabs follow renames', () => {
  test('F2 rename of an open file re-keys the tab; Ctrl+S writes the new path', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-hello.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('hello world');
    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('edited ');
    await expect(editor.locator('[data-testid="edit-status"]')).toContainText('modified');

    // Select the row (blurs CM so F2 reaches the sidebar) and rename.
    await editor.locator('[data-testid="edit-entry-hello.txt"]').click();
    await page.keyboard.press('F2');
    const input = editor.locator('[data-testid="edit-rename-input"]');
    await expect(input).toBeVisible();
    await input.fill('renamed.txt');
    await input.press('Enter');

    const oldTab = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'hello.txt')}"]`);
    const newTab = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'renamed.txt')}"]`);
    await expect(newTab).toBeVisible({ timeout: 3_000 });
    await expect(newTab).toHaveAttribute('data-active', 'true');
    await expect(oldTab).toHaveCount(0);
    // Buffer, edits and dirty flag survived the re-key.
    await expect(editor.locator('.cm-content')).toContainText('hello worldedited');
    await expect(editor.locator('[data-testid="edit-status"]')).toContainText('renamed.txt');
    await expect(editor.locator('[data-testid="edit-status"]')).toContainText('modified');
    await expect(editor.locator('[data-testid="edit-status-missing"]')).toHaveCount(0);

    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('again');
    await page.keyboard.press('Control+s');
    await expect(editor.locator('[data-testid="edit-status"]')).not.toContainText('modified');
    expect(readFileSync(join(router.fmRoot, 'renamed.txt'), 'utf8')).toBe('hello world\nedited again');
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(false);
    // The old row does not come back either.
    await expect(editor.locator('[data-testid="edit-entry-hello.txt"]')).toHaveCount(0);
  });

  test('tree drop (move) of an open file re-keys the tab into the folder', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-hello.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('hello world');

    await editor.locator('[data-testid="edit-entry-hello.txt"]').dragTo(editor.locator('[data-testid="edit-entry-docs"]'));

    const newTab = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'docs', 'hello.txt')}"]`);
    await expect(newTab).toBeVisible({ timeout: 3_000 });
    await expect(editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'hello.txt')}"]`)).toHaveCount(0);

    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('moved');
    await page.keyboard.press('Control+s');
    await expect(editor.locator('[data-testid="edit-status"]')).not.toContainText('modified');
    expect(readFileSync(join(router.fmRoot, 'docs', 'hello.txt'), 'utf8')).toBe('hello world\nmoved');
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(false);
  });

  test('external rename marks the tab deleted-on-disk; Ctrl+S asks for a path instead of recreating it', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-hello.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('hello world');

    renameSync(join(router.fmRoot, 'hello.txt'), join(router.fmRoot, 'elsewhere.txt'));

    await expect(editor.locator('[data-testid="edit-status-missing"]')).toBeVisible({ timeout: 3_000 });
    // The buffer is still there, on the old tab.
    await expect(editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'hello.txt')}"]`)).toBeVisible();
    await expect(editor.locator('.cm-content')).toContainText('hello world');

    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+s');
    const picker = page.locator('[data-testid="edit-picker"]');
    await expect(picker).toBeVisible();
    await expect(picker.locator('[data-testid="fp-save-name"]')).toHaveValue('hello.txt');
    await expect(picker.locator('[data-testid="fp-path"]')).toHaveValue(router.fmRoot);
    // Nothing was written behind the dialog.
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(false);

    // Confirming the seeded name is the explicit "yes, recreate it".
    await picker.locator('[data-testid="fp-confirm"]').click();
    await expect(picker).toHaveCount(0);
    await expect(editor.locator('[data-testid="edit-status-missing"]')).toHaveCount(0, { timeout: 3_000 });
    expect(readFileSync(join(router.fmRoot, 'hello.txt'), 'utf8')).toBe('hello world\n');
  });
});
