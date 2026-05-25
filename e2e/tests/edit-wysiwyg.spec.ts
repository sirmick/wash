// wash-edit WYSIWYG markdown — end-to-end. Drives the rich-text
// path with the real router + browser, exercising:
//   - opening a .md file lands in wysiwyg by default (TipTap, not CM)
//   - markdown source renders as styled DOM (heading is an <h1>)
//   - toolbar formatting commands fire (bold roundtrips to **…**)
//   - Ctrl+S persists serialized markdown back to disk
//   - View → Source toggles the same tab into the CodeMirror view
//   - the toggle is a no-op for non-.md files

import { test, expect } from '../fixtures/router';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(
    join(root, 'notes.md'),
    '# Notes\n\nfirst paragraph\n\n- one\n- two\n',
  );
  writeFileSync(join(root, 'plain.txt'), 'no markdown here\n');
}

test.describe('wash-edit wysiwyg', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });

  async function openEditor(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
    const editor = page.locator('wash-app-edit');
    await expect(editor).toBeVisible();
    return editor;
  }

  test('opens .md in wysiwyg with rendered heading + toolbar', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-notes.md"]').dblclick();

    // Wysiwyg host appears for this tab; toolbar is visible.
    const host = editor.locator(`[data-testid="edit-wf-host-${join(router.fmRoot, 'notes.md')}"]`);
    await expect(host).toBeVisible();
    await expect(editor.locator('[data-testid="edit-wf-toolbar"]')).toBeVisible();

    // Heading renders as a real <h1> containing the title text —
    // proving the markdown was parsed (not raw text in a <pre>).
    await expect(host.locator('h1')).toContainText('Notes');
    await expect(host.locator('ul li')).toHaveCount(2);

    // CodeMirror is hidden behind the wysiwyg layer.
    await expect(editor.locator('[data-testid="edit-cm"]')).toBeHidden();
  });

  test('toolbar bold roundtrips to ** on save', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-notes.md"]').dblclick();
    const host = editor.locator(`[data-testid="edit-wf-host-${join(router.fmRoot, 'notes.md')}"]`);
    await expect(host.locator('h1')).toContainText('Notes');

    // Focus the editor surface and type some text, then select it
    // and toggle bold. Using keyboard for this keeps the test
    // resilient to TipTap's content-editable selection model.
    const body = host.locator('[data-testid="edit-wf-body"]');
    await body.click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('emphasized');
    // Select the just-typed word and bold it.
    for (let i = 0; i < 'emphasized'.length; i++) await page.keyboard.press('Shift+ArrowLeft');
    await editor.locator('[data-testid="edit-wf-bold"]').click();

    // Tab now reads dirty.
    const tab = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'notes.md')}"]`);
    await expect(tab).toHaveAttribute('data-dirty', 'true');

    await page.keyboard.press('Control+s');
    // Disk-side first: a successful save is the bedrock for the
    // dirty-clear assertion. We poll briefly so the BE has time to
    // complete the write.
    await expect.poll(() => {
      try { return readFileSync(join(router.fmRoot, 'notes.md'), 'utf8'); }
      catch { return ''; }
    }, { timeout: 5000 }).toContain('**emphasized**');
    const disk = readFileSync(join(router.fmRoot, 'notes.md'), 'utf8');
    expect(disk).toContain('# Notes');

    await expect(tab).not.toHaveAttribute('data-dirty', 'true');
  });

  test('View → Source toggles to CodeMirror', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-notes.md"]').dblclick();
    const tabID = join(router.fmRoot, 'notes.md');
    await expect(editor.locator(`[data-testid="edit-wf-host-${tabID}"]`)).toBeVisible();

    // Open View menu, click Source.
    await editor.locator('[data-testid="edit-menubar-view"]').click();
    await editor.locator('[data-testid="edit-menu-source"]').click();

    // CM is now visible; wysiwyg host hides.
    await expect(editor.locator('[data-testid="edit-cm"]')).toBeVisible();
    await expect(editor.locator(`[data-testid="edit-wf-host-${tabID}"]`)).toBeHidden();
    // CM shows raw markdown.
    await expect(editor.locator('.cm-content')).toContainText('# Notes');

    // Toggle back via Ctrl+Shift+P.
    await page.keyboard.press('Control+Shift+P');
    await expect(editor.locator(`[data-testid="edit-wf-host-${tabID}"]`)).toBeVisible();
    await expect(editor.locator('[data-testid="edit-cm"]')).toBeHidden();
  });

  test('non-.md files stay in source view', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-plain.txt"]').dblclick();
    await expect(editor.locator('[data-testid="edit-cm"]')).toBeVisible();
    await expect(editor.locator('[data-testid="edit-wf-toolbar"]')).toBeHidden();
    // Toggle command is a no-op — wysiwyg host shouldn't appear.
    await page.keyboard.press('Control+Shift+P');
    await expect(editor.locator('[data-testid^="edit-wf-host-"]')).toHaveCount(0);
  });
});
