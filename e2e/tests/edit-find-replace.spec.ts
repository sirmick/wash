// wash-edit find and replace (docs/Review-findings.md P2 → edit).
//
// Two gaps: Ctrl+H was Chromium's History (CodeMirror's replace row was
// only reachable by opening find and clicking into it), and Ctrl+F did
// nothing at all unless the editor already had focus — CM binds Mod-f
// inside its own DOM, so pressing it with the sidebar focused was a
// no-op.

import { test, expect } from '../fixtures/router';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'poem.txt'), 'foo one\nfoo two\nbar three\n');
}

type Page = import('@playwright/test').Page;
type Router = import('../fixtures/router').RouterHandle;

async function openFile(page: Page, router: Router) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor).toBeVisible();
  await editor.locator('[data-testid="edit-entry-poem.txt"]').dblclick();
  await expect(editor.locator('.cm-content')).toContainText('foo one');
  return editor;
}

test.describe('wash-edit find and replace', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });
  test.setTimeout(40_000);

  test('Ctrl+F opens search with the sidebar focused', async ({ page, router }) => {
    const editor = await openFile(page, router);
    // Focus deliberately away from CodeMirror.
    await editor.locator('[data-testid="edit-entry-poem.txt"]').click();
    await page.keyboard.press('Control+f');
    const search = editor.locator('.cm-panel input[name="search"]');
    await expect(search).toBeVisible();
    await expect(search).toBeFocused();
  });

  test('Ctrl+H opens the replace row and replace all rewrites the buffer', async ({ page, router }) => {
    const editor = await openFile(page, router);
    await editor.locator('.cm-content').click();

    await page.keyboard.press('Control+h');
    const replace = editor.locator('.cm-panel input[name="replace"]');
    await expect(replace).toBeVisible();
    // The caret lands in replace, not in search — that is the whole
    // point of a separate binding.
    await expect(replace).toBeFocused();

    await editor.locator('.cm-panel input[name="search"]').fill('foo');
    await replace.fill('baz');
    await editor.locator('.cm-panel button[name="replaceAll"]').click();
    await expect(editor.locator('.cm-content')).toContainText('baz one');
    await expect(editor.locator('.cm-content')).toContainText('baz two');
    await expect(editor.locator('.cm-content')).not.toContainText('foo');

    // A replace is an edit: the tab is dirty and Ctrl+S writes it.
    const tab = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'poem.txt')}"]`);
    await expect(tab).toHaveAttribute('data-dirty', 'true');
  });
});
