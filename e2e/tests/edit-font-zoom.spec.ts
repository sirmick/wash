// wash-edit font zoom (docs/Review-findings.md P2 → edit). The editor
// had one font size and no way to change it; the browser's own Ctrl+±
// scales the entire desktop, chrome included, which is a different act.
//
// The size is desktop-wide (the prefs file, not the window's state), so
// the reload assertion at the end is the point of the feature, not a
// bonus.

import { test, expect } from '../fixtures/router';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'note.txt'), 'alpha\nbravo\n');
}

type Page = import('@playwright/test').Page;
type Router = import('../fixtures/router').RouterHandle;

async function openFile(page: Page, router: Router) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor).toBeVisible();
  await editor.locator('[data-testid="edit-entry-note.txt"]').dblclick();
  await expect(editor.locator('.cm-content')).toContainText('alpha');
  return editor;
}

const px = (page: Page) =>
  page.locator('wash-app-edit .cm-scroller').evaluate((el) => getComputedStyle(el).fontSize);

test.describe('wash-edit font zoom', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });
  test.setTimeout(40_000);

  test('Ctrl+= / Ctrl+- / Ctrl+0, and the size outlives the window', async ({ page, router }) => {
    const editor = await openFile(page, router);
    const cursor = router.logCursor();
    await expect.poll(() => px(page)).toBe('13px');

    await editor.press('Control+=');
    await expect.poll(() => px(page)).toBe('14px');
    await editor.press('Control+=');
    await expect.poll(() => px(page)).toBe('15px');
    await editor.press('Control+-');
    await expect.poll(() => px(page)).toBe('14px');

    // Persisted desktop-wide: a fresh window comes up zoomed. The BE
    // half — the size reached the prefs file, not just this window.
    await router.waitForLog(/wash-edit: prefs set keys=\[font_size\]/, 5_000, cursor);
    await router.waitForLog(/app_state\.set instance=/, 5_000, cursor);
    await page.reload();
    const editor2 = page.locator('wash-app-edit');
    await expect(editor2).toBeVisible();
    await expect(editor2.locator('.cm-content')).toContainText('alpha');
    await expect.poll(() => px(page)).toBe('14px');

    await editor2.press('Control+0');
    await expect.poll(() => px(page)).toBe('13px');
  });

  test('Ctrl+wheel over the editor zooms too', async ({ page, router }) => {
    const editor = await openFile(page, router);
    await expect.poll(() => px(page)).toBe('13px');

    const box = await editor.locator('.cm-content').boundingBox();
    await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2);
    await page.keyboard.down('Control');
    await page.mouse.wheel(0, -120);
    await expect.poll(() => px(page)).toBe('14px');
    await page.mouse.wheel(0, 240);
    await expect.poll(() => px(page)).toBe('13px');
    await page.keyboard.up('Control');
  });
});
