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
  writeFileSync(join(root, 'note.txt'), 'original line\n');
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

  // ---- open-file watching: reload an open buffer when its file
  // changes on disk. Clean buffers reload silently; dirty buffers
  // prompt so unsaved edits aren't clobbered.

  const noteTab = (router: import('../fixtures/router').RouterHandle) =>
    'edit-tab-' + join(router.fmRoot, 'note.txt');

  test('open file: external change to a clean buffer reloads silently', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-note.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('original line');

    // No unsaved edits — the buffer reloads without a prompt.
    writeFileSync(join(router.fmRoot, 'note.txt'), 'reloaded from disk\n');
    await expect(editor.locator('.cm-content'))
      .toContainText('reloaded from disk', { timeout: 3_000 });
    await expect(editor.locator('.cm-content')).not.toContainText('original line');
    await expect(editor.locator('[data-testid="edit-reload-dialog"]')).toHaveCount(0);
  });

  test('open file: external change to a dirty buffer prompts; Reload loads disk', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-note.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('original line');

    // Make unsaved edits so a reload would lose work.
    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('my local edit\n');
    const tab = editor.locator('[data-testid="' + noteTab(router) + '"]');
    await expect(tab).toHaveAttribute('data-dirty', 'true');

    // External change raises the prompt.
    writeFileSync(join(router.fmRoot, 'note.txt'), 'external rewrite\n');
    const dialog = editor.locator('[data-testid="edit-reload-dialog"]');
    await expect(dialog).toBeVisible({ timeout: 3_000 });

    // Reload discards local edits, loads disk content, clears dirty.
    await editor.locator('[data-testid="edit-reload-confirm"]').click();
    await expect(dialog).not.toBeVisible();
    await expect(editor.locator('.cm-content')).toContainText('external rewrite');
    await expect(editor.locator('.cm-content')).not.toContainText('my local edit');
    await expect(tab).not.toHaveAttribute('data-dirty', 'true');
  });

  test('open file: external change to a dirty buffer; Keep editing preserves edits', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-note.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('original line');

    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('keep this edit\n');
    const tab = editor.locator('[data-testid="' + noteTab(router) + '"]');
    await expect(tab).toHaveAttribute('data-dirty', 'true');

    writeFileSync(join(router.fmRoot, 'note.txt'), 'disk version\n');
    const dialog = editor.locator('[data-testid="edit-reload-dialog"]');
    await expect(dialog).toBeVisible({ timeout: 3_000 });

    // Keep editing: dialog closes, local edits survive, still dirty.
    await editor.locator('[data-testid="edit-reload-keep"]').click();
    await expect(dialog).not.toBeVisible();
    await expect(editor.locator('.cm-content')).toContainText('keep this edit');
    await expect(tab).toHaveAttribute('data-dirty', 'true');
  });

  test('open file: saving your own edits does not raise a reload prompt', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-note.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('original line');

    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('a self edit\n');
    const tab = editor.locator('[data-testid="' + noteTab(router) + '"]');
    await expect(tab).toHaveAttribute('data-dirty', 'true');

    // Ctrl+S writes our own file, which fires a watch event for it.
    // The baseline==disk check must swallow that so we don't prompt to
    // reload the content we just saved.
    await page.keyboard.press('Control+s');
    await expect(tab).not.toHaveAttribute('data-dirty', 'true');

    // Let the self-write's watch event round-trip, then assert no
    // prompt appeared and our content is intact.
    await page.waitForTimeout(800);
    await expect(editor.locator('[data-testid="edit-reload-dialog"]')).toHaveCount(0);
    await expect(editor.locator('.cm-content')).toContainText('a self edit');
  });

  test('open file: external delete keeps the buffer (no prompt, content retained)', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-note.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('original line');

    // Delete the file out from under the editor.
    unlinkSync(join(router.fmRoot, 'note.txt'));
    // The watch event demonstrably fired — the sidebar row is gone.
    await expect(editor.locator('[data-testid="edit-entry-note.txt"]'))
      .toHaveCount(0, { timeout: 3_000 });
    // The buffer is kept (so the user can still save it back out) and
    // no reload prompt is raised for a vanished file.
    await expect(editor.locator('.cm-content')).toContainText('original line');
    await expect(editor.locator('[data-testid="edit-reload-dialog"]')).toHaveCount(0);
  });

  test('open file: reload survives the sidebar collapsing its folder', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    // Expand src/ (sidebar watches it), open the nested file (the
    // open-file watch also subscribes to src/), then collapse src/ —
    // the sidebar releases its subtree watch, but the open-file watch
    // must keep src/ live. This is the exact case the dedicated
    // fileWatch instance exists for.
    await editor.locator('[data-testid="edit-entry-src"]').dblclick();
    await expect(editor.locator('[data-testid="edit-entry-index.js"]')).toBeVisible();
    await editor.locator('[data-testid="edit-entry-index.js"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('export {}');

    // Collapse src/ — sidebar unwatches the whole subtree.
    await editor.locator('[data-testid="edit-entry-src"]').dblclick();
    await expect(editor.locator('[data-testid="edit-entry-index.js"]')).toHaveCount(0);

    // An external change still reloads the clean buffer, proving the
    // open-file watch outlived the sidebar's collapse teardown.
    writeFileSync(join(router.fmRoot, 'src', 'index.js'), 'export const y = 2;\n');
    await expect(editor.locator('.cm-content'))
      .toContainText('export const y = 2', { timeout: 3_000 });
  });
});
