// Drop-to-open: a file dragged from a Files (fm) window onto the editor
// BODY or tab strip opens it in a tab. It is not moved — only the sidebar
// tree has move semantics — and CodeMirror's default drop, which used to
// paste the drag's text/plain fallback (the path) into whatever buffer
// was under the cursor, is suppressed.
//
// Both halves: the FE shows the new tab with the file's content and no
// stray path text; the filesystem still has the file at its old path.

import { test, expect } from '../fixtures/router';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';

function seed(root: string): void {
  writeFileSync(join(root, 'hello.txt'), 'hello world\n');
}

// Two 900/760 px windows cascade by 24 px, so the second covers the
// first; a wide viewport leaves room to drag one clear of the other.
test.use({ routerOpts: { fmRoot: true, fmSeed: seed }, viewport: { width: 1900, height: 1000 } });

async function launch(page: Page, name: RegExp) {
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name }).click();
}

// moveWindowBy drags the titlebar of the window hosting `app` by dx.
async function moveWindowBy(page: Page, app: string, dx: number) {
  const titlebar = page.locator('.wash-window', { has: page.locator(app) }).locator('.wash-titlebar');
  const box = (await titlebar.boundingBox())!;
  const x = box.x + 150;
  const y = box.y + box.height / 2;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x + dx, y, { steps: 6 });
  await page.mouse.up();
}

test.describe('wash-edit drop-to-open', () => {
  test('dragging a file from fm onto the editor body opens it without moving it', async ({ page, router }) => {
    await page.goto(router.url);
    await launch(page, /Editor/);
    const editor = page.locator('wash-app-edit');
    await expect(editor.locator('[data-testid="edit-entry-hello.txt"]')).toBeVisible();

    // A buffer under the drop target, to prove nothing gets pasted.
    await editor.locator('[data-testid="edit-cm"]').click();
    await page.keyboard.press('Control+n');
    await expect(editor.locator('[data-testid="edit-tab-untitled-1"]')).toBeVisible();
    await editor.locator('.cm-content').click();
    await page.keyboard.type('scratch');

    await launch(page, /^Files$/);
    const fm = page.locator('wash-app-fm');
    await expect(fm.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
    // fm spawned second, so it sits on top of the editor; slide it aside.
    await moveWindowBy(page, 'wash-app-fm', 950);

    const src = fm.locator('[data-testid="fm-entry-hello.txt"]');
    const dst = editor.locator('[data-testid="edit-cm"]');
    await expect(dst).toBeVisible();
    await src.dragTo(dst);

    // A tab for the dropped file appears and is active, holding the
    // file's content — and only that.
    const tab = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'hello.txt')}"]`);
    await expect(tab).toBeVisible({ timeout: 5_000 });
    await expect(tab).toHaveAttribute('data-active', 'true');
    await expect(editor.locator('.cm-content')).toHaveText(/^hello world\s*$/);

    // The scratch buffer did not receive the path as text.
    await editor.locator('[data-testid="edit-tab-untitled-1"]').click();
    await expect(editor.locator('.cm-content')).toHaveText(/^scratch\s*$/);
    await expect(editor.locator('.cm-content')).not.toContainText('hello.txt');

    // Not moved: still at its path, still in both trees.
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(true);
    expect(readFileSync(join(router.fmRoot, 'hello.txt'), 'utf8')).toBe('hello world\n');
    await expect(fm.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
    await expect(editor.locator('[data-testid="edit-entry-hello.txt"]')).toBeVisible();
  });

  test('dropping a file from fm onto the tab strip opens it', async ({ page, router }) => {
    await page.goto(router.url);
    await launch(page, /Editor/);
    const editor = page.locator('wash-app-edit');
    await expect(editor.locator('[data-testid="edit-entry-hello.txt"]')).toBeVisible();
    await launch(page, /^Files$/);
    const fm = page.locator('wash-app-fm');
    await expect(fm.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
    await moveWindowBy(page, 'wash-app-fm', 950);

    await fm.locator('[data-testid="fm-entry-hello.txt"]').dragTo(editor.locator('[data-testid="edit-tabs"]'));

    await expect(editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'hello.txt')}"]`)).toBeVisible({ timeout: 5_000 });
    await expect(editor.locator('.cm-content')).toContainText('hello world');
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(true);
  });

  test('an OS file dropped on the editor body is refused with a hint, not pasted', async ({ page, router }) => {
    await page.goto(router.url);
    await launch(page, /Editor/);
    const editor = page.locator('wash-app-edit');
    await expect(editor.locator('[data-testid="edit-entry-hello.txt"]')).toBeVisible();
    await editor.locator('[data-testid="edit-entry-hello.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('hello world');

    const dataTransfer = await page.evaluateHandle(() => {
      const dt = new DataTransfer();
      dt.items.add(new File(['OS-BYTES'], 'os.txt', { type: 'text/plain' }));
      return dt;
    });
    const target = editor.locator('.cm-content');
    await target.dispatchEvent('dragover', { dataTransfer });
    await target.dispatchEvent('drop', { dataTransfer });

    await expect(editor.locator('[data-testid="edit-status-error"]')).toContainText(/drag files from Files/);
    await expect(editor.locator('.cm-content')).not.toContainText('OS-BYTES');
    await expect(editor.locator('.cm-content')).toContainText('hello world');
  });
});
