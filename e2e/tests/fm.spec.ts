// wash-fm: tree-view file manager. The tree is rooted at the
// first list_ok the BE delivers, which is $HOME for a default
// launch. Tests that want a deterministic shape send their own
// initial `list` app_msg pointing at a tmp fixture.

import { test, expect } from '../fixtures/router';
import { mkdtempSync, writeFileSync, mkdirSync, symlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

async function navigate(page: import('@playwright/test').Page, path: string) {
  const input = page.locator('[data-testid="fm-path"]');
  await input.fill(path);
  await input.press('Enter');
}

test.describe('file manager', () => {
  test('launches and lists the home directory', async ({ page, router }) => {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /Files/ }).click();

    const fm = page.locator('wash-app-fm');
    await expect(fm).toBeVisible();
    const path = page.locator('[data-testid="fm-path"]');
    await expect(path).not.toHaveValue('');
    const list = page.locator('[data-testid="fm-list"]');
    await expect(list).toBeVisible();
    await expect(list).not.toBeEmpty();
  });

  test('tree expand: click folder to expand inline, click file to preview', async ({ page, router }) => {
    const root = mkdtempSync(join(tmpdir(), 'wash-fm-tree-'));
    mkdirSync(join(root, 'sub'));
    writeFileSync(join(root, 'top.txt'), 'top-level\n');
    writeFileSync(join(root, 'sub', 'note.txt'), 'inside subdir\n');

    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /Files/ }).click();
    await expect(page.locator('wash-app-fm')).toBeVisible();
    await navigate(page, root);

    await expect(page.locator('[data-testid="fm-entry-top.txt"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-entry-sub"]')).toBeVisible();

    // Double-click sub to navigate in (single click only selects;
    // dirs require an explicit open gesture).
    await page.locator('[data-testid="fm-entry-sub"]').dblclick();
    await expect(page.locator('[data-testid="fm-entry-note.txt"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-entry-top.txt"]')).toBeVisible();

    await page.locator('[data-testid="fm-entry-note.txt"]').click();
    await expect(page.locator('[data-testid="fm-preview"]')).toHaveText(/inside subdir/);
    await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(join(root, 'sub', 'note.txt'));
  });

  test('binary files are not previewed', async ({ page, router }) => {
    const root = mkdtempSync(join(tmpdir(), 'wash-fm-bin-'));
    writeFileSync(join(root, 'image.bin'), Buffer.from([0, 1, 2, 3, 0, 4]));

    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /Files/ }).click();
    await expect(page.locator('wash-app-fm')).toBeVisible();
    await navigate(page, root);

    await page.locator('[data-testid="fm-entry-image.bin"]').click();
    await expect(page.locator('[data-testid="fm-preview"]')).toHaveText(/\(binary,/);
  });

  test('right-click row opens context menu with Open / Copy path / Show info', async ({ page, router }) => {
    const root = mkdtempSync(join(tmpdir(), 'wash-fm-ctx-'));
    writeFileSync(join(root, 'foo.txt'), 'hi\n');

    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /Files/ }).click();
    await expect(page.locator('wash-app-fm')).toBeVisible();
    await navigate(page, root);

    await page.locator('[data-testid="fm-entry-foo.txt"]').click({ button: 'right' });
    const menu = page.locator('[data-testid="fm-context-menu"]');
    await expect(menu).toBeVisible();
    await expect(page.locator('[data-testid="fm-ctx-open"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-ctx-copy"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-ctx-info"]')).toBeVisible();

    await page.locator('[data-testid="fm-ctx-info"]').click();
    await expect(menu).toBeHidden();
    await expect(page.locator('[data-testid="fm-info"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-info"]')).toContainText('foo.txt');
    await expect(page.locator('[data-testid="fm-info"]')).toContainText('Permissions');
  });

  test('sort menu: toggle show-hidden surfaces dotfiles', async ({ page, router }) => {
    const root = mkdtempSync(join(tmpdir(), 'wash-fm-hide-'));
    writeFileSync(join(root, 'visible.txt'), 'v\n');
    writeFileSync(join(root, '.dotfile'), 'd\n');

    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /Files/ }).click();
    await expect(page.locator('wash-app-fm')).toBeVisible();
    await navigate(page, root);

    await expect(page.locator('[data-testid="fm-entry-visible.txt"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-entry-.dotfile"]')).toHaveCount(0);

    await page.locator('[data-testid="fm-sort"]').click();
    await page.locator('[data-testid="fm-show-hidden"]').click();
    await expect(page.locator('[data-testid="fm-entry-.dotfile"]')).toBeVisible();
  });

  test('symlinks: shown, follow on double-click', async ({ page, router }) => {
    const root = mkdtempSync(join(tmpdir(), 'wash-fm-link-'));
    mkdirSync(join(root, 'real'));
    writeFileSync(join(root, 'real', 'inside.txt'), 'inside\n');
    symlinkSync('real', join(root, 'link'));

    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /Files/ }).click();
    await expect(page.locator('wash-app-fm')).toBeVisible();
    await navigate(page, root);

    const linkRow = page.locator('[data-testid="fm-entry-link"]');
    await expect(linkRow).toBeVisible();
    await expect(linkRow).toHaveAttribute('data-type', 'symlink');

    await linkRow.dblclick();
    await expect(page.locator('[data-testid="fm-entry-inside.txt"]')).toBeVisible();
  });

  test('path bar autocompletes from BE matches', async ({ page, router }) => {
    const root = mkdtempSync(join(tmpdir(), 'wash-fm-auto-'));
    mkdirSync(join(root, 'apples'));
    mkdirSync(join(root, 'apricot'));
    writeFileSync(join(root, 'banana.txt'), 'b\n');

    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /Files/ }).click();
    await expect(page.locator('wash-app-fm')).toBeVisible();

    // Type fixture + "/ap" → completions for apples + apricot.
    const input = page.locator('[data-testid="fm-path"]');
    await input.click();
    await input.fill(root + '/ap');

    // Dropdown should show both ap*-prefix dirs.
    const dropdown = page.locator('[data-testid="fm-complete"]');
    await expect(dropdown).toBeVisible();
    await expect(page.locator('[data-testid="fm-complete-0"]')).toBeVisible();
    // Two matches (apples/, apricot/) — banana doesn't match prefix.
    await expect(dropdown.locator('[data-testid^="fm-complete-"]')).toHaveCount(2);

    // ArrowDown to second, Enter picks + navigates.
    await input.press('ArrowDown');
    await input.press('Enter');
    await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(join(root, 'apricot'));
  });

  test('up button: walks the selection to the parent', async ({ page, router }) => {
    const root = mkdtempSync(join(tmpdir(), 'wash-fm-up-'));
    mkdirSync(join(root, 'inner'));
    writeFileSync(join(root, 'inner', 'leaf.txt'), 'leaf\n');

    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /Files/ }).click();
    await expect(page.locator('wash-app-fm')).toBeVisible();
    await navigate(page, root);

    await page.locator('[data-testid="fm-entry-inner"]').dblclick();
    await page.locator('[data-testid="fm-entry-leaf.txt"]').click();
    await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(join(root, 'inner', 'leaf.txt'));

    await page.locator('[data-testid="fm-up"]').click();
    await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(join(root, 'inner'));
    await page.locator('[data-testid="fm-up"]').click();
    await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(root);
  });
});
