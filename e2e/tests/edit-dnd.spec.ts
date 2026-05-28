// wash-edit drag-and-drop: single-file drag in the sidebar tree.
// Mirrors the within-window slice of fm's DnD coverage — fm's
// e2e patterns translate directly because the editor uses the
// same MIME constant + the same Solid dragTo semantics.
//
//   1. Drop a file onto a folder row → move via editor BE rename.
//   2. Alt-drop opens a menu with Move / Copy / Rename / Delete.
//   3. Menu → Copy routes through wash-bulk.
//   4. Menu → Rename pops the inline-edit input on the row.
//
// The fs.watch wiring landed earlier handles the post-mutation
// tree refresh; we use the moved row's visibility as the sync
// barrier before asserting filesystem state.

import { test, expect } from '../fixtures/router';
import { existsSync, readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'hello.txt'), 'hello world\n');
  mkdirSync(join(root, 'docs'));
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
  // Wait for the initial sidebar list.
  await expect(editor.locator('[data-testid="edit-entry-hello.txt"]')).toBeVisible();
  return editor;
}

test.describe('wash-edit DnD', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });

  test('drop a file onto a folder row → file moves', async ({ page, router }) => {
    const editor = await openEditor(page, router);

    const src = editor.locator('[data-testid="edit-entry-hello.txt"]');
    const dst = editor.locator('[data-testid="edit-entry-docs"]');
    await src.dragTo(dst);

    // fs.watch on root + docs catches up; the moved row vanishes
    // from root and (once docs is expanded) reappears inside.
    await expect(editor.locator('[data-testid="edit-entry-hello.txt"]')).toHaveCount(0, { timeout: 3_000 });
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(false);
    expect(existsSync(join(router.fmRoot, 'docs', 'hello.txt'))).toBe(true);
    expect(readFileSync(join(router.fmRoot, 'docs', 'hello.txt'), 'utf8')).toBe('hello world\n');
  });

  test('alt-drop opens menu with Move / Copy / Rename / Delete', async ({ page, router }) => {
    const editor = await openEditor(page, router);

    const src = editor.locator('[data-testid="edit-entry-hello.txt"]');
    const dst = editor.locator('[data-testid="edit-entry-docs"]');
    // Hold Alt during the drag so the drop pops the menu instead
    // of committing a move. Playwright's dragTo doesn't expose
    // modifiers directly, so we do the manual gesture.
    const srcBox = (await src.boundingBox())!;
    const dstBox = (await dst.boundingBox())!;
    await page.mouse.move(srcBox.x + srcBox.width / 2, srcBox.y + srcBox.height / 2);
    await page.mouse.down();
    await page.keyboard.down('Alt');
    await page.mouse.move(dstBox.x + dstBox.width / 2, dstBox.y + dstBox.height / 2, { steps: 8 });
    await page.mouse.up();
    await page.keyboard.up('Alt');

    // Menus portal to document.body, so scope to `page` not `editor`.
    const menu = page.locator('[data-testid="edit-drop-menu"]');
    await expect(menu).toBeVisible();
    await expect(menu.locator('[data-testid="edit-drop-move"]')).toBeVisible();
    await expect(menu.locator('[data-testid="edit-drop-copy"]')).toBeVisible();
    await expect(menu.locator('[data-testid="edit-drop-rename"]')).toBeVisible();
    await expect(menu.locator('[data-testid="edit-drop-delete"]')).toBeVisible();
  });

  test('alt-drop → Copy routes through wash-bulk', async ({ page, router }) => {
    const editor = await openEditor(page, router);

    const src = editor.locator('[data-testid="edit-entry-hello.txt"]');
    const dst = editor.locator('[data-testid="edit-entry-docs"]');
    const srcBox = (await src.boundingBox())!;
    const dstBox = (await dst.boundingBox())!;
    await page.mouse.move(srcBox.x + srcBox.width / 2, srcBox.y + srcBox.height / 2);
    await page.mouse.down();
    await page.keyboard.down('Alt');
    await page.mouse.move(dstBox.x + dstBox.width / 2, dstBox.y + dstBox.height / 2, { steps: 8 });
    await page.mouse.up();
    await page.keyboard.up('Alt');

    await page.locator('[data-testid="edit-drop-copy"]').click();

    // bulk-ops queues the job → router log records it. fs.watch
    // catches up to the new file in docs/.
    await router.waitForLog(/bulk-ops job=\S+ op=copy status=done/, 5_000);
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(true); // original stays
    expect(existsSync(join(router.fmRoot, 'docs', 'hello.txt'))).toBe(true); // copy lands
  });

  test('alt-drop → Rename pops inline-edit input on the dragged row', async ({ page, router }) => {
    const editor = await openEditor(page, router);

    const src = editor.locator('[data-testid="edit-entry-hello.txt"]');
    const dst = editor.locator('[data-testid="edit-entry-docs"]');
    const srcBox = (await src.boundingBox())!;
    const dstBox = (await dst.boundingBox())!;
    await page.mouse.move(srcBox.x + srcBox.width / 2, srcBox.y + srcBox.height / 2);
    await page.mouse.down();
    await page.keyboard.down('Alt');
    await page.mouse.move(dstBox.x + dstBox.width / 2, dstBox.y + dstBox.height / 2, { steps: 8 });
    await page.mouse.up();
    await page.keyboard.up('Alt');

    await page.locator('[data-testid="edit-drop-rename"]').click();

    const input = editor.locator('[data-testid="edit-rename-input"]');
    await expect(input).toBeVisible();
    await input.fill('renamed.txt');
    await input.press('Enter');

    await expect(editor.locator('[data-testid="edit-entry-renamed.txt"]')).toBeVisible({ timeout: 3_000 });
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(false);
    expect(existsSync(join(router.fmRoot, 'renamed.txt'))).toBe(true);
  });

  test('alt-drop → Delete (single file) removes the file', async ({ page, router }) => {
    const editor = await openEditor(page, router);

    const src = editor.locator('[data-testid="edit-entry-hello.txt"]');
    const dst = editor.locator('[data-testid="edit-entry-docs"]');
    const srcBox = (await src.boundingBox())!;
    const dstBox = (await dst.boundingBox())!;
    await page.mouse.move(srcBox.x + srcBox.width / 2, srcBox.y + srcBox.height / 2);
    await page.mouse.down();
    await page.keyboard.down('Alt');
    await page.mouse.move(dstBox.x + dstBox.width / 2, dstBox.y + dstBox.height / 2, { steps: 8 });
    await page.mouse.up();
    await page.keyboard.up('Alt');

    await page.locator('[data-testid="edit-drop-delete"]').click();

    await expect(editor.locator('[data-testid="edit-entry-hello.txt"]')).toHaveCount(0, { timeout: 3_000 });
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(false);
  });

  test('Open in fm button spawns a wash-fm window', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    // wash-fm should NOT be present yet.
    await expect(page.locator('wash-app-fm')).toHaveCount(0);
    await editor.locator('[data-testid="edit-open-in-fm"]').click();
    await expect(page.locator('wash-app-fm')).toBeVisible({ timeout: 3_000 });
  });
});
