// wash-edit unsaved-changes guard (docs/Review-findings.md 2026-09-08 P0 #5).
//
// Two doors: a dirty tab's Ctrl+W / ×, and the window's titlebar ✕. The
// window path rides the router's close handshake the way wash-term does —
// the BE vetoes at once and asks the FE, which answers close_window_confirmed
// when nothing is unsaved. Both halves: the dialog on screen, and the bytes
// (or the window) afterwards.

import { test, expect } from '../fixtures/router';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'a.txt'), 'alpha\n');
  writeFileSync(join(root, 'b.txt'), 'beta\n');
}

test.describe('wash-edit close guard', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });
  test.setTimeout(30_000);

  async function openEditor(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
    const editor = page.locator('wash-app-edit');
    await expect(editor).toBeVisible();
    await expect(editor.locator('[data-testid="edit-entry-a.txt"]')).toBeVisible();
    return editor;
  }

  async function dirtyUp(page: import('@playwright/test').Page, editor: import('@playwright/test').Locator, name: string, text: string) {
    await editor.locator(`[data-testid="edit-entry-${name}"]`).dblclick();
    await expect(editor.locator('.cm-content')).toBeVisible();
    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type(text);
    await expect(editor.locator('[data-testid="edit-status"]')).toContainText('modified');
  }

  test('Ctrl+W on a dirty tab asks; Cancel keeps it, Don\'t save drops it', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await dirtyUp(page, editor, 'a.txt', 'more');
    const tab = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'a.txt')}"]`);
    const dialog = page.locator('[data-testid="edit-close-dialog"]');

    await editor.press('Control+w');
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('[data-testid="edit-close-dialog-item"]')).toHaveText(['a.txt']);
    await page.locator('[data-testid="edit-close-cancel"]').click();
    await expect(dialog).toHaveCount(0);
    await expect(tab).toBeVisible();
    await expect(editor.locator('.cm-content')).toContainText('more');

    await editor.press('Control+w');
    await page.locator('[data-testid="edit-close-discard"]').click();
    await expect(tab).toHaveCount(0);
    expect(readFileSync(join(router.fmRoot, 'a.txt'), 'utf8')).toBe('alpha\n');
  });

  test('Save on the tab prompt writes the file and then closes it', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await dirtyUp(page, editor, 'a.txt', 'saved');
    const tab = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'a.txt')}"]`);
    await editor.locator(`[data-testid="edit-tab-close-${join(router.fmRoot, 'a.txt')}"]`).click();
    await page.locator('[data-testid="edit-close-save"]').click();
    await expect(tab).toHaveCount(0);
    expect(readFileSync(join(router.fmRoot, 'a.txt'), 'utf8')).toBe('alpha\nsaved');
  });

  test('a clean tab closes without asking', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-a.txt"]').dblclick();
    const tab = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'a.txt')}"]`);
    await expect(tab).toBeVisible();
    await editor.press('Control+w');
    await expect(tab).toHaveCount(0);
    await expect(page.locator('[data-testid="edit-close-dialog"]')).toHaveCount(0);
  });

  test('the titlebar ✕ asks about every dirty tab, and the answers hold', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await dirtyUp(page, editor, 'a.txt', '1');
    await dirtyUp(page, editor, 'b.txt', '2');
    const win = page.locator('.wash-window', { has: editor });
    const closeBtn = win.locator('[data-testid="window-close"]');
    const dialog = page.locator('[data-testid="edit-close-dialog"]');

    // ✕ → BE vetoes (router log) → FE asks. Cancel: the window survives
    // the router's 5s force-kill grace, which is what the veto is for.
    await closeBtn.click();
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('[data-testid="edit-close-dialog-item"]')).toHaveCount(2);
    await page.locator('[data-testid="edit-close-cancel"]').click();
    await expect(dialog).toHaveCount(0);
    await page.waitForTimeout(6_000);
    await expect(editor).toBeVisible();

    // ✕ → Save: both files written, window gone.
    await closeBtn.click();
    await expect(dialog).toBeVisible();
    await page.locator('[data-testid="edit-close-save"]').click();
    await expect(editor).toHaveCount(0, { timeout: 10_000 });
    expect(readFileSync(join(router.fmRoot, 'a.txt'), 'utf8')).toBe('alpha\n1');
    expect(readFileSync(join(router.fmRoot, 'b.txt'), 'utf8')).toBe('beta\n2');
  });

  test('the titlebar ✕ on a clean editor closes at once', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-a.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('alpha');
    const win = page.locator('.wash-window', { has: editor });
    await win.locator('[data-testid="window-close"]').click();
    await expect(editor).toHaveCount(0, { timeout: 10_000 });
    await expect(page.locator('[data-testid="edit-close-dialog"]')).toHaveCount(0);
  });

  test('Don\'t save on the window prompt discards and closes', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await dirtyUp(page, editor, 'a.txt', 'gone');
    const win = page.locator('.wash-window', { has: editor });
    await win.locator('[data-testid="window-close"]').click();
    await page.locator('[data-testid="edit-close-discard"]').click();
    await expect(editor).toHaveCount(0, { timeout: 10_000 });
    expect(readFileSync(join(router.fmRoot, 'a.txt'), 'utf8')).toBe('alpha\n');
  });
});
