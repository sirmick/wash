// wash-edit end-to-end: drives the editor through the launcher
// (it's not Hidden so showHidden isn't needed). Exercises the
// sidebar tree, CodeMirror open + edit + save round-trip, the
// FilePicker integration for Open + Save-As, and tab management.
//
// Topology: editor BE is the data path — list/read/write happen
// in-process. The picker uses sdk.EnableFilePicker on the
// editor's BE, so its dispatch is also in-process.

import { test, expect } from '../fixtures/router';
import { readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'hello.txt'), '# hello\n\nfirst line\n');
  writeFileSync(join(root, 'config.json'), '{"a":1}\n');
  mkdirSync(join(root, 'src'));
  writeFileSync(join(root, 'src', 'index.js'), 'export const x = 1;\n');
}

test.describe('wash-edit', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });

  async function openEditor(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
    const editor = page.locator('wash-app-edit');
    await expect(editor).toBeVisible();
    return editor;
  }

  test('sidebar lists seeded files, click opens in CodeMirror', async ({ page, router }) => {
    const editor = await openEditor(page, router);

    // Sidebar shows the seeded files.
    await expect(editor.locator('[data-testid="edit-entry-hello.txt"]')).toBeVisible();
    await expect(editor.locator('[data-testid="edit-entry-config.json"]')).toBeVisible();

    // Double-click hello.txt — single click selects, double click
    // opens (matches fm). A tab appears, content lands in CM.
    await editor.locator('[data-testid="edit-entry-hello.txt"]').dblclick();
    await expect(editor.locator('[data-testid="edit-tab-' + join(router.fmRoot, 'hello.txt') + '"]')).toBeVisible();
    await expect(editor.locator('.cm-content')).toContainText('first line');
  });

  test('expand folder, open nested file', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    // Double-click folder to expand; double-click file to open.
    await editor.locator('[data-testid="edit-entry-src"]').dblclick();
    await expect(editor.locator('[data-testid="edit-entry-index.js"]')).toBeVisible();
    await editor.locator('[data-testid="edit-entry-index.js"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('export const x = 1');
  });

  test('edit then Ctrl+S writes to disk', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-hello.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('first line');

    // Focus the editor and type a new line.
    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('appended\n');

    // Dirty marker on the tab.
    const tab = editor.locator('[data-testid="edit-tab-' + join(router.fmRoot, 'hello.txt') + '"]');
    await expect(tab).toHaveAttribute('data-dirty', 'true');

    // Save.
    await page.keyboard.press('Control+s');

    // Dirty marker clears.
    await expect(tab).not.toHaveAttribute('data-dirty', 'true');

    // On-disk content updated.
    const updated = readFileSync(join(router.fmRoot, 'hello.txt'), 'utf8');
    expect(updated).toContain('appended');
  });

  // Saving must not disturb the buffer being edited. The active-tab
  // effect used to re-seed CodeMirror on every tabs() mutation, and a
  // save mutates tabs(), so Ctrl+S clobbered the live view.
  test('Ctrl+S leaves the caret and undo history alone', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    const tab = editor.locator('[data-testid="edit-tab-' + join(router.fmRoot, 'hello.txt') + '"]');
    await editor.locator('[data-testid="edit-entry-hello.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('first line');

    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('appended');
    await page.keyboard.press('Control+s');
    await expect(tab).not.toHaveAttribute('data-dirty', 'true');

    // The caret is still where the typing left it. If the save rebuilt
    // the editor state, it went back to offset 0 and this lands at the
    // top of the file instead.
    //
    // The pause is load-bearing: CodeMirror groups edits into undo
    // events by elapsed time (newGroupDelay, 500ms), so typing straight
    // after the save would join the previous group and make the undo
    // assertion below depend on how fast the machine ran the save.
    await page.waitForTimeout(700);
    await page.keyboard.type('X');
    await expect(editor.locator('.cm-content')).toContainText('appendedX');
    await page.keyboard.press('Control+s');
    await expect.poll(() => readFileSync(join(router.fmRoot, 'hello.txt'), 'utf8')).toContain('appendedX');

    // Undo history survived the save: one undo takes back the X and
    // stops there. Rebuilding the state on save left an empty history,
    // where this undo did nothing at all.
    await page.keyboard.press('Control+z');
    await expect(editor.locator('.cm-content')).not.toContainText('appendedX');
    await expect(editor.locator('.cm-content')).toContainText('appended');
    await expect(editor.locator('.cm-content')).toContainText('first line');
  });

  // The data-loss half: a tab switched away from and back carries a
  // captured EditorState, and re-applying it after a save reverted the
  // document to that snapshot while the disk held the new text — so the
  // next save wrote the stale text back over it.
  test('Ctrl+S on a revisited tab keeps the edits on screen', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    const helloTab = '[data-testid="edit-tab-' + join(router.fmRoot, 'hello.txt') + '"]';

    await editor.locator('[data-testid="edit-entry-hello.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('first line');
    // Away to a second tab (which captures hello.txt's state) and back.
    await editor.locator('[data-testid="edit-entry-config.json"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('"a":1');
    await editor.locator(helloTab).click();
    await expect(editor.locator('.cm-content')).toContainText('first line');

    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('SURVIVES');
    await page.keyboard.press('Control+s');
    await expect.poll(() => readFileSync(join(router.fmRoot, 'hello.txt'), 'utf8')).toContain('SURVIVES');
    await expect(editor.locator(helloTab)).not.toHaveAttribute('data-dirty', 'true');

    // Still on screen, and still on screen a beat later — the revert
    // arrived one effect flush after the save, so an immediate check
    // passes even when the buffer is about to be thrown away.
    await expect(editor.locator('.cm-content')).toContainText('SURVIVES');
    await page.waitForTimeout(300);
    await expect(editor.locator('.cm-content')).toContainText('SURVIVES');

    // And the save that follows must not write the stale text back.
    await page.keyboard.type('!');
    await page.keyboard.press('Control+s');
    await expect.poll(() => readFileSync(join(router.fmRoot, 'hello.txt'), 'utf8')).toContain('SURVIVES!');
  });

  // Save As is the one save that changes a tab's id, so it is the one
  // that legitimately re-seeds the view. It must seed from the live
  // buffer, not from the snapshot taken at the last tab switch.
  test('Save As carries the live buffer to the new file', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-hello.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('first line');
    // Switch away and back so the tab carries a captured state.
    await editor.locator('[data-testid="edit-entry-config.json"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('"a":1');
    await editor.locator('[data-testid="edit-tab-' + join(router.fmRoot, 'hello.txt') + '"]').click();
    await expect(editor.locator('.cm-content')).toContainText('first line');

    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('COPIED');
    await page.keyboard.press('Control+Shift+S');

    const picker = page.locator('[data-testid="edit-picker"]');
    await expect(picker).toBeVisible();
    await picker.locator('[data-testid="fp-path"]').fill(router.fmRoot);
    await picker.locator('[data-testid="fp-path"]').press('Enter');
    await picker.locator('[data-testid="fp-save-name"]').fill('copy.txt');
    await picker.locator('[data-testid="fp-confirm"]').click();
    await expect(picker).not.toBeVisible();

    await expect.poll(() => existsSync(join(router.fmRoot, 'copy.txt'))).toBe(true);
    expect(readFileSync(join(router.fmRoot, 'copy.txt'), 'utf8')).toContain('COPIED');
    // The renamed tab shows what was written, not the pre-edit snapshot.
    await page.waitForTimeout(300);
    await expect(editor.locator('.cm-content')).toContainText('COPIED');
  });

  test('Ctrl+N opens an Untitled buffer, Ctrl+S pops Save As', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    // Make sure the editor receives the keystroke — focus it.
    await editor.click();

    await page.keyboard.press('Control+n');
    await expect(editor.locator('[data-testid^="edit-tab-untitled-"]')).toBeVisible();

    // Type content into the new buffer.
    await editor.locator('.cm-content').click();
    await page.keyboard.type('hello from untitled');

    // Ctrl+S on Untitled pops the picker in save mode.
    await page.keyboard.press('Control+s');
    const picker = page.locator('[data-testid="edit-picker"]');
    await expect(picker).toBeVisible();

    // Save as a fresh name into fmRoot.
    await picker.locator('[data-testid="fp-path"]').fill(router.fmRoot);
    await picker.locator('[data-testid="fp-path"]').press('Enter');
    await picker.locator('[data-testid="fp-save-name"]').fill('new-untitled.txt');
    await picker.locator('[data-testid="fp-confirm"]').click();
    await expect(picker).not.toBeVisible();

    // File exists on disk. The picker closing is an FE event; the BE
    // write is async, so poll rather than read immediately — a bare
    // readFileSync here raced the write under load and hit ENOENT.
    const savedPath = join(router.fmRoot, 'new-untitled.txt');
    await expect
      .poll(() => (existsSync(savedPath) ? readFileSync(savedPath, 'utf8') : null))
      .toBe('hello from untitled');

    // Tab id morphed to the real path; no leftover Untitled tab.
    await expect(editor.locator('[data-testid="edit-tab-' + join(router.fmRoot, 'new-untitled.txt') + '"]')).toBeVisible();
    await expect(editor.locator('[data-testid^="edit-tab-untitled-"]')).not.toBeVisible();
  });

  test('Ctrl+O opens picker; chosen file lands in a tab', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.click();

    await page.keyboard.press('Control+o');
    const picker = page.locator('[data-testid="edit-picker"]');
    await expect(picker).toBeVisible();

    await picker.locator('[data-testid="fp-path"]').fill(router.fmRoot);
    await picker.locator('[data-testid="fp-path"]').press('Enter');
    await picker.locator('[data-testid="fp-entry-config.json"]').click();
    await picker.locator('[data-testid="fp-confirm"]').click();

    await expect(picker).not.toBeVisible();
    await expect(editor.locator('.cm-content')).toContainText('"a":1');
  });

  test('Ctrl+W closes the active tab', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-hello.txt"]').dblclick();
    const tab = editor.locator('[data-testid="edit-tab-' + join(router.fmRoot, 'hello.txt') + '"]');
    await expect(tab).toBeVisible();
    await editor.click();
    await page.keyboard.press('Control+w');
    await expect(tab).not.toBeVisible();
  });

  test('opening the same file twice converges on one tab', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-hello.txt"]').dblclick();
    await editor.locator('[data-testid="edit-entry-config.json"]').dblclick();
    await editor.locator('[data-testid="edit-entry-hello.txt"]').dblclick();
    // Exactly two tabs total. The close ✕ element shares the
    // edit-tab- prefix (edit-tab-close-…), so scope the count to
    // tabs whose id matches a real path (starts with "/").
    await expect(editor.locator('[data-testid^="edit-tab-/"]')).toHaveCount(2);
  });
});
