// wash-edit go to line (docs/Review-findings.md P2 → edit): Ctrl+G and
// Edit ▸ Go to Line…. CodeMirror ships the dialog but only bound it to
// Ctrl+Alt+G, which nobody guesses; Ctrl+G was find-next, which F3
// already is.

import { test, expect } from '../fixtures/router';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'lines.txt'), ['one', 'two', 'three', 'four', 'five'].join('\n') + '\n');
}

type Page = import('@playwright/test').Page;
type Router = import('../fixtures/router').RouterHandle;

async function openFile(page: Page, router: Router) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor).toBeVisible();
  await editor.locator('[data-testid="edit-entry-lines.txt"]').dblclick();
  await expect(editor.locator('.cm-content')).toContainText('three');
  return editor;
}

test.describe('wash-edit go to line', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });
  test.setTimeout(40_000);

  test('Ctrl+G jumps the cursor, and Ctrl+Alt+G still does', async ({ page, router }) => {
    const editor = await openFile(page, router);
    await editor.locator('.cm-content').click();

    await page.keyboard.press('Control+g');
    const line = editor.locator('.cm-panel input[name="line"]');
    await expect(line).toBeVisible();
    await line.fill('4');
    await line.press('Enter');
    await expect(editor.locator('.cm-activeLine')).toHaveText('four');

    // CM's own binding is left alone.
    await page.keyboard.press('Control+Alt+g');
    const line2 = editor.locator('.cm-panel input[name="line"]');
    await expect(line2).toBeVisible();
    await line2.fill('2');
    await line2.press('Enter');
    await expect(editor.locator('.cm-activeLine')).toHaveText('two');

    // Ctrl+G took find-next's old binding, so the one place it could
    // still collide is with the search panel up. It does not: find keeps
    // its panel (and its own Enter / F3), Ctrl+G still goes to a line.
    await page.keyboard.press('Control+f');
    const search = editor.locator('.cm-panel input[name="search"]');
    await expect(search).toBeVisible();
    await search.fill('five');
    await page.keyboard.press('Control+g');
    const line3 = editor.locator('.cm-panel input[name="line"]');
    await expect(line3).toBeVisible();
    await line3.fill('1');
    await line3.press('Enter');
    await expect(editor.locator('.cm-activeLine')).toHaveText('one');
  });

  test('Edit ▸ Go to Line… opens the dialog with the sidebar focused', async ({ page, router }) => {
    const editor = await openFile(page, router);
    await editor.locator('[data-testid="edit-entry-lines.txt"]').click();
    await page.keyboard.press('Control+g');
    await expect(editor.locator('.cm-panel input[name="line"]')).toBeVisible();
    await page.keyboard.press('Escape');

    await editor.locator('[data-testid="edit-menubar-edit"]').click();
    await page.locator('[data-testid="edit-menu-goto-line"]').click();
    const line = editor.locator('.cm-panel input[name="line"]');
    await expect(line).toBeVisible();
    await line.fill('3');
    await line.press('Enter');
    await expect(editor.locator('.cm-activeLine')).toHaveText('three');
  });
});
