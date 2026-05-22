// Live fs.watch coverage for wash-edit. The editor's sidebar and
// the embedded FilePicker both subscribe via the same sdk
// EnableFilePicker helper, so this spec verifies both:
//
//   - sidebar refreshes when a watched dir changes underneath
//   - picker refreshes when its cwd changes underneath
//
// Each test writes a file via fs/promises and expects the
// corresponding row to mount within the debounce window without
// any user action.

import { test, expect } from '../fixtures/router';
import { mkdirSync, unlinkSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';

function seed(root: string): void {
  writeFileSync(join(root, 'hello.md'), '# hi\n');
  mkdirSync(join(root, 'src'));
  writeFileSync(join(root, 'src', 'index.js'), 'export {};\n');
}

async function openEditor(page: Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor).toBeVisible();
  return editor;
}

test.describe('wash-edit live refresh', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });

  test('sidebar: external file create appears in the tree', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    // Wait for the initial root listing.
    await expect(editor.locator('[data-testid="edit-entry-hello.md"]')).toBeVisible();

    // Drop a file outside the editor; the root watch fires.
    writeFileSync(join(router.fmRoot, 'live-created.txt'), 'fresh\n');
    await expect(editor.locator('[data-testid="edit-entry-live-created.txt"]'))
      .toBeVisible({ timeout: 3_000 });
  });

  test('sidebar: external file delete removes the row', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    const row = editor.locator('[data-testid="edit-entry-hello.md"]');
    await expect(row).toBeVisible();
    unlinkSync(join(router.fmRoot, 'hello.md'));
    await expect(row).toHaveCount(0, { timeout: 3_000 });
  });

  test('sidebar: expanded folder picks up child changes', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    // Expand the src/ folder; subscribes the BE to it. (Single
    // click only selects now — double-click is the expand
    // gesture to match fm.)
    await editor.locator('[data-testid="edit-entry-src"]').dblclick();
    await expect(editor.locator('[data-testid="edit-entry-index.js"]')).toBeVisible();
    // Add a file under src/ — the expanded folder's watch fires.
    writeFileSync(join(router.fmRoot, 'src', 'new.js'), '// x\n');
    await expect(editor.locator('[data-testid="edit-entry-new.js"]'))
      .toBeVisible({ timeout: 3_000 });
  });

  test('picker: external file create appears while picker is open', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.click();
    await page.keyboard.press('Control+o');
    const picker = page.locator('[data-testid="edit-picker"]');
    await expect(picker).toBeVisible();

    // Navigate to fmRoot explicitly so we know the picker's cwd.
    await picker.locator('[data-testid="fp-path"]').fill(router.fmRoot);
    await picker.locator('[data-testid="fp-path"]').press('Enter');
    await expect(picker.locator('[data-testid="fp-entry-hello.md"]')).toBeVisible();

    writeFileSync(join(router.fmRoot, 'picker-fresh.txt'), 'pf\n');
    await expect(picker.locator('[data-testid="fp-entry-picker-fresh.txt"]'))
      .toBeVisible({ timeout: 3_000 });
  });
});
