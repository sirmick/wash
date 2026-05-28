// Row-level UX alignment with fm: right-click context menu,
// F2 / Delete keybindings, double-click semantics for files /
// folders / symlinks, empty-pane drop landing in the selected
// folder. Covers the behaviors that landed in the "make edit
// match fm" alignment pass.

import { test, expect } from '../fixtures/router';
import { existsSync, writeFileSync, mkdirSync, symlinkSync, unlinkSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'plain.txt'), 'plain\n');
  mkdirSync(join(root, 'sub'));
  writeFileSync(join(root, 'sub', 'inside.txt'), 'inside\n');
  // Symlink → existing file. fm's double-click follow lands the
  // user at the target; edit now does the same.
  symlinkSync(join(root, 'sub'), join(root, 'lnk'));
}

async function openEditor(
  page: import('@playwright/test').Page,
  router: import('../fixtures/router').RouterHandle,
) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor).toBeVisible();
  await expect(editor.locator('[data-testid="edit-entry-plain.txt"]')).toBeVisible();
  return editor;
}

test.describe('wash-edit row UX (fm parity)', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });

  test('single click selects, does not open', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-plain.txt"]').click();
    // Selection visible via data-selected attribute.
    await expect(editor.locator('[data-testid="edit-entry-plain.txt"]'))
      .toHaveAttribute('data-selected', 'true');
    // No tab opened.
    await expect(editor.locator('[data-testid^="edit-tab-/"]')).toHaveCount(0);
  });

  test('double-click file opens in tab', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-plain.txt"]').dblclick();
    await expect(editor.locator('[data-testid="edit-tab-' + join(router.fmRoot, 'plain.txt') + '"]')).toBeVisible();
  });

  test('double-click folder expands it', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-sub"]').dblclick();
    await expect(editor.locator('[data-testid="edit-entry-inside.txt"]')).toBeVisible();
  });

  test('double-click symlink follows target', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    // lnk → sub. Double-clicking follows it, which expands sub
    // and reveals its contents.
    await editor.locator('[data-testid="edit-entry-lnk"]').dblclick();
    await expect(editor.locator('[data-testid="edit-entry-inside.txt"]')).toBeVisible({ timeout: 3_000 });
  });

  test('right-click opens context menu with Open / Copy path / Rename / Delete', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-plain.txt"]').click({ button: 'right' });
    // Menus portal to document.body, so scope to `page` not `editor`.
    const menu = page.locator('[data-testid="edit-ctx-menu"]');
    await expect(menu).toBeVisible();
    await expect(menu.locator('[data-testid="edit-ctx-open"]')).toBeVisible();
    await expect(menu.locator('[data-testid="edit-ctx-copy-path"]')).toBeVisible();
    await expect(menu.locator('[data-testid="edit-ctx-rename"]')).toBeVisible();
    await expect(menu.locator('[data-testid="edit-ctx-delete"]')).toBeVisible();
  });

  test('right-click → Rename pops inline edit input', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-plain.txt"]').click({ button: 'right' });
    await page.locator('[data-testid="edit-ctx-rename"]').click();
    await expect(editor.locator('[data-testid="edit-rename-input"]')).toBeVisible();
  });

  test('right-click → Delete removes the file', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-plain.txt"]').click({ button: 'right' });
    await page.locator('[data-testid="edit-ctx-delete"]').click();
    await expect(editor.locator('[data-testid="edit-entry-plain.txt"]')).toHaveCount(0, { timeout: 3_000 });
    expect(existsSync(join(router.fmRoot, 'plain.txt'))).toBe(false);
  });

  test('F2 starts inline rename on the selected row', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    // Select first (single click).
    await editor.locator('[data-testid="edit-entry-plain.txt"]').click();
    // F2 — sidebar shortcut. CM isn't focused so the guard
    // allows it through.
    await page.keyboard.press('F2');
    const input = editor.locator('[data-testid="edit-rename-input"]');
    await expect(input).toBeVisible();
    await input.fill('renamed-via-f2.txt');
    await input.press('Enter');
    await expect(editor.locator('[data-testid="edit-entry-renamed-via-f2.txt"]'))
      .toBeVisible({ timeout: 3_000 });
    expect(existsSync(join(router.fmRoot, 'renamed-via-f2.txt'))).toBe(true);
  });

  test('Delete key removes the selected row', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-plain.txt"]').click();
    await page.keyboard.press('Delete');
    await expect(editor.locator('[data-testid="edit-entry-plain.txt"]')).toHaveCount(0, { timeout: 3_000 });
  });

  test('Escape clears selection', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    const row = editor.locator('[data-testid="edit-entry-plain.txt"]');
    await row.click();
    await expect(row).toHaveAttribute('data-selected', 'true');
    await page.keyboard.press('Escape');
    await expect(row).not.toHaveAttribute('data-selected', 'true');
  });
});
