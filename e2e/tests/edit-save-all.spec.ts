// wash-edit whole-strip verbs (docs/Review-findings.md P2 → edit): Save All,
// Close All, Close Others, Revert.
//
// Every case asserts both halves: the strip / dialog the user sees, AND
// the bytes on disk afterwards — a Save All that only cleared the dirty
// dots would pass the first and fail the second.

import { test, expect } from '../fixtures/router';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'a.txt'), 'alpha\n');
  writeFileSync(join(root, 'b.txt'), 'bravo\n');
  writeFileSync(join(root, 'c.txt'), 'charlie\n');
}

type Page = import('@playwright/test').Page;
type Router = import('../fixtures/router').RouterHandle;

async function openEditor(page: Page, router: Router) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor).toBeVisible();
  await expect(editor.locator('[data-testid="edit-entry-a.txt"]')).toBeVisible();
  return editor;
}

// openAndType opens `name` from the sidebar and appends `text` to it, so
// the tab is dirty and the edit is visible on disk once saved.
async function openAndType(page: Page, router: Router, editor: ReturnType<Page['locator']>, name: string, text: string) {
  await editor.locator(`[data-testid="edit-entry-${name}"]`).dblclick();
  const tab = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, name)}"]`);
  await expect(tab).toHaveAttribute('data-active', 'true');
  await editor.locator('.cm-content').click();
  await page.keyboard.press('Control+End');
  await page.keyboard.type(text);
  await expect(tab).toHaveAttribute('data-dirty', 'true');
  return tab;
}

const fileTabs = (editor: ReturnType<Page['locator']>) =>
  editor.locator('[data-testid="edit-tabs"] [data-testid^="edit-tab-"]:not([data-testid^="edit-tab-close-"])');

test.describe('wash-edit Save All / Close All / Close Others / Revert', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });
  test.setTimeout(40_000);

  test('Ctrl+Alt+S saves every dirty tab, asking for a path once per untitled buffer', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    const a = await openAndType(page, router, editor, 'a.txt', '+a');
    const b = await openAndType(page, router, editor, 'b.txt', '+b');
    // An Untitled buffer has no path; Save All must route it through the
    // picker rather than skip it or stall on it.
    await editor.press('Control+n');
    await editor.locator('.cm-content').click();
    await page.keyboard.type('fresh');
    const u = editor.locator('[data-testid="edit-tab-untitled-1"]');
    await expect(u).toHaveAttribute('data-dirty', 'true');

    await editor.press('Control+Alt+s');
    // The two saved files are written before the picker asks about the
    // untitled one.
    await expect(a).not.toHaveAttribute('data-dirty', 'true');
    await expect(b).not.toHaveAttribute('data-dirty', 'true');
    const picker = page.locator('[data-testid="edit-picker"]');
    await expect(picker).toBeVisible();
    expect(readFileSync(join(router.fmRoot, 'a.txt'), 'utf8')).toBe('alpha\n+a');
    expect(readFileSync(join(router.fmRoot, 'b.txt'), 'utf8')).toBe('bravo\n+b');

    await picker.locator('[data-testid="fp-save-name"]').fill('fresh.txt');
    await picker.locator('[data-testid="fp-confirm"]').click();
    await expect(picker).toHaveCount(0);
    const fresh = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'fresh.txt')}"]`);
    await expect(fresh).toBeVisible();
    await expect(fresh).not.toHaveAttribute('data-dirty', 'true');
    expect(readFileSync(join(router.fmRoot, 'fresh.txt'), 'utf8')).toBe('fresh');
    // The File menu item is the same verb and greys out once nothing is dirty.
    await editor.locator('[data-testid="edit-menubar-file"]').click();
    await expect(page.locator('[data-testid="edit-menu-save-all"]')).toBeDisabled();
  });

  test('Close All raises one dialog naming every dirty tab; Save writes them all', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await openAndType(page, router, editor, 'a.txt', '+a');
    await editor.locator('[data-testid="edit-entry-c.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('charlie');
    await openAndType(page, router, editor, 'b.txt', '+b');
    await expect(fileTabs(editor)).toHaveCount(3);

    await editor.locator('[data-testid="edit-menubar-file"]').click();
    await page.locator('[data-testid="edit-menu-close-all"]').click();
    const dialog = page.locator('[data-testid="edit-close-dialog"]');
    await expect(dialog).toBeVisible();
    // Only the dirty ones are listed — c.txt is clean and just closes.
    await expect(dialog.locator('[data-testid="edit-close-dialog-item"]')).toHaveText(['a.txt', 'b.txt']);

    await dialog.locator('[data-testid="edit-close-save"]').click();
    await expect(dialog).toHaveCount(0);
    await expect(fileTabs(editor)).toHaveCount(0);
    expect(readFileSync(join(router.fmRoot, 'a.txt'), 'utf8')).toBe('alpha\n+a');
    expect(readFileSync(join(router.fmRoot, 'b.txt'), 'utf8')).toBe('bravo\n+b');
    expect(readFileSync(join(router.fmRoot, 'c.txt'), 'utf8')).toBe('charlie\n');
  });

  test("Close All → Don't save closes everything and leaves the disk alone", async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await openAndType(page, router, editor, 'a.txt', '+a');
    await openAndType(page, router, editor, 'b.txt', '+b');
    await editor.locator('[data-testid="edit-menubar-file"]').click();
    await page.locator('[data-testid="edit-menu-close-all"]').click();
    const dialog = page.locator('[data-testid="edit-close-dialog"]');
    await dialog.locator('[data-testid="edit-close-discard"]').click();
    await expect(fileTabs(editor)).toHaveCount(0);
    await page.waitForTimeout(300);
    expect(readFileSync(join(router.fmRoot, 'a.txt'), 'utf8')).toBe('alpha\n');
    expect(readFileSync(join(router.fmRoot, 'b.txt'), 'utf8')).toBe('bravo\n');
    expect(router.log()).not.toMatch(/wash-edit: write/);
  });

  test('Close Others keeps the active tab and asks only about the dirty rest', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await openAndType(page, router, editor, 'a.txt', '+a');
    await editor.locator('[data-testid="edit-entry-b.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('bravo');
    const c = await openAndType(page, router, editor, 'c.txt', '+c');

    await editor.locator('[data-testid="edit-menubar-file"]').click();
    await page.locator('[data-testid="edit-menu-close-others"]').click();
    const dialog = page.locator('[data-testid="edit-close-dialog"]');
    await expect(dialog.locator('[data-testid="edit-close-dialog-item"]')).toHaveText(['a.txt']);
    await dialog.locator('[data-testid="edit-close-save"]').click();
    await expect(fileTabs(editor)).toHaveCount(1);
    await expect(c).toHaveAttribute('data-active', 'true');
    // Still dirty: Close Others never touches the tab you are on.
    await expect(c).toHaveAttribute('data-dirty', 'true');
    expect(readFileSync(join(router.fmRoot, 'a.txt'), 'utf8')).toBe('alpha\n+a');
    expect(readFileSync(join(router.fmRoot, 'c.txt'), 'utf8')).toBe('charlie\n');
  });

  test('Revert asks when dirty, then reloads the disk version', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    const a = await openAndType(page, router, editor, 'a.txt', 'garbage');
    await expect(editor.locator('.cm-content')).toContainText('garbage');

    await editor.locator('[data-testid="edit-menubar-file"]').click();
    await page.locator('[data-testid="edit-menu-revert"]').click();
    const dialog = page.locator('[data-testid="edit-revert-dialog"]');
    await expect(dialog).toContainText('a.txt');
    // Cancel keeps the edits.
    await dialog.locator('[data-testid="edit-revert-cancel"]').click();
    await expect(editor.locator('.cm-content')).toContainText('garbage');
    await expect(a).toHaveAttribute('data-dirty', 'true');

    await editor.locator('[data-testid="edit-menubar-file"]').click();
    await page.locator('[data-testid="edit-menu-revert"]').click();
    await dialog.locator('[data-testid="edit-revert-confirm"]').click();
    await expect(editor.locator('.cm-content')).not.toContainText('garbage');
    await expect(editor.locator('.cm-content')).toContainText('alpha');
    await expect(a).not.toHaveAttribute('data-dirty', 'true');
    // Nothing was written on the way: the disk is the source, not a target.
    expect(readFileSync(join(router.fmRoot, 'a.txt'), 'utf8')).toBe('alpha\n');
    expect(router.log()).not.toMatch(/wash-edit: write/);
  });
});
