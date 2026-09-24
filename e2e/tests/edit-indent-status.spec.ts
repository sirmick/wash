// wash-edit indentation and the status bar (docs/Review-findings.md
// P2 → edit).
//
// Indentation was hardcoded to two spaces, so Tab in a Go file inserted
// spaces into a tab-indented file; the status bar said only the path and
// "modified". Now each file's indentation is detected from its own
// content, and the bar carries Ln/Col, the language, the indent and the
// line ending — each of them the control for changing it.

import { test, expect } from '../fixtures/router';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'tabbed.go'), 'package main\n\nfunc main() {\n\tif true {\n\t\tprintln(1)\n\t}\n}\n');
  writeFileSync(join(root, 'two.json'), '{\n  "a": 1,\n  "b": {\n    "c": 2\n  }\n}\n');
  writeFileSync(join(root, 'crlf.txt'), 'alpha\nbravo\n');
  writeFileSync(join(root, 'messy.txt'), 'alpha   \nbravo\t\ncharlie');
}

type Page = import('@playwright/test').Page;
type Router = import('../fixtures/router').RouterHandle;

async function openEditor(page: Page, router: Router) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor).toBeVisible();
  await expect(editor.locator('[data-testid="edit-entry-two.json"]')).toBeVisible();
  return editor;
}

test.describe('wash-edit indentation and status bar', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });
  test.setTimeout(40_000);

  test('indentation, language and Ln/Col come off the file itself', async ({ page, router }) => {
    const editor = await openEditor(page, router);

    await editor.locator('[data-testid="edit-entry-tabbed.go"]').dblclick();
    await expect(editor.locator('[data-testid="edit-status-indent"]')).toHaveText('Tab');
    await expect(editor.locator('[data-testid="edit-status-lang"]')).toHaveText('Go');
    await expect(editor.locator('[data-testid="edit-status-eol"]')).toHaveText('LF');
    await expect(editor.locator('[data-testid="edit-status-cursor"]')).toHaveText('Ln 1, Col 1');

    // The cursor cell follows the caret.
    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await expect(editor.locator('[data-testid="edit-status-cursor"]')).toHaveText('Ln 8, Col 1');

    // A different file, detected separately — the point of per-tab.
    await editor.locator('[data-testid="edit-entry-two.json"]').dblclick();
    await expect(editor.locator('[data-testid="edit-status-indent"]')).toHaveText('Spaces: 2');
    await expect(editor.locator('[data-testid="edit-status-lang"]')).toHaveText('JSON');
  });

  test('the indent picker retargets the tab, and Tab inserts what it says', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-tabbed.go"]').dblclick();
    await expect(editor.locator('[data-testid="edit-status-indent"]')).toHaveText('Tab');

    // A tab-indented file: Tab on the empty last line must insert a tab,
    // not the old hardcoded two spaces.
    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.press('Tab');
    await page.keyboard.type('x');
    await page.keyboard.press('Control+s');
    await expect.poll(() => readFileSync(join(router.fmRoot, 'tabbed.go'), 'utf8')).toMatch(/\}\n\tx$/);

    // Retargeting the tab through the picker changes what Tab inserts.
    await editor.locator('[data-testid="edit-status-indent"]').click();
    await page.locator('[data-testid="edit-menu-indent-spaces-4"]').click();
    await expect(editor.locator('[data-testid="edit-status-indent"]')).toHaveText('Spaces: 4');
    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+Home');
    await page.keyboard.press('Tab');
    await page.keyboard.press('Control+s');
    await expect.poll(() => readFileSync(join(router.fmRoot, 'tabbed.go'), 'utf8')).toMatch(/^    package main\n/);
  });

  test('the EOL cell switches line endings and the next save writes them', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-crlf.txt"]').dblclick();
    await expect(editor.locator('[data-testid="edit-status-eol"]')).toHaveText('LF');

    await editor.locator('[data-testid="edit-status-eol"]').click();
    await page.locator('[data-testid="edit-menu-eol-crlf"]').click();
    await expect(editor.locator('[data-testid="edit-status-eol"]')).toHaveText('CRLF');
    // The buffer looks identical, so the tab has to say it is dirty or
    // the change would be lost with no sign of it.
    await expect(editor.locator('[data-testid="edit-status"]')).toContainText('modified');

    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+s');
    await expect.poll(() => readFileSync(join(router.fmRoot, 'crlf.txt'), 'utf8')).toBe('alpha\r\nbravo\r\n');
  });

  test('the on-save cleanups are off until asked for, then rewrite the buffer too', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-messy.txt"]').dblclick();
    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.press('Control+s');
    // Default: exactly what was there, trailing spaces and all.
    await expect.poll(() => readFileSync(join(router.fmRoot, 'messy.txt'), 'utf8')).toBe('alpha   \nbravo\t\ncharlie');

    await editor.locator('[data-testid="edit-menubar-view"]').click();
    await page.locator('[data-testid="edit-menu-trim-trailing"]').click();
    await editor.locator('[data-testid="edit-menubar-view"]').click();
    await page.locator('[data-testid="edit-menu-final-newline"]').click();

    // A save with nothing else changed still applies them.
    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+s');
    await expect.poll(() => readFileSync(join(router.fmRoot, 'messy.txt'), 'utf8')).toBe('alpha\nbravo\ncharlie\n');
    // And the buffer was rewritten with the file, so the tab is clean
    // rather than instantly dirty against its own new baseline.
    await expect(editor.locator('[data-testid="edit-status"]')).not.toContainText('modified');

    // Desktop-wide: the setting is in prefs, so it survives a reload.
    await page.reload();
    const editor2 = page.locator('wash-app-edit');
    await expect(editor2).toBeVisible();
    await editor2.locator('[data-testid="edit-menubar-view"]').click();
    await expect(page.locator('[data-testid="edit-menu-trim-trailing"]')).toContainText('Trim Trailing');
    await expect(page.locator('[data-testid="edit-menu-trim-trailing"] svg')).toBeVisible();
  });
});
