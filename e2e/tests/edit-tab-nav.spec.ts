// wash-edit tab navigation (docs/Review-findings.md P2 → edit): cycling
// with the keyboard, drag-to-reorder in the strip, middle-click close.
//
// Ctrl+Tab / Ctrl+Shift+Tab are bound but Chromium reserves them in a
// normal browser tab (they work here because CDP-injected keys bypass the
// reservation, and in a PWA/kiosk window). The Alt alternates — the ones
// the term app also binds — are what a user in a plain tab actually gets,
// so both sets are exercised.

import { test, expect } from '../fixtures/router';
import { writeFileSync } from 'node:fs';
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
  for (const n of ['a.txt', 'b.txt', 'c.txt']) {
    await editor.locator(`[data-testid="edit-entry-${n}"]`).dblclick();
    await expect(editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, n)}"]`)).toHaveAttribute('data-active', 'true');
  }
  return editor;
}

const tabOf = (editor: ReturnType<Page['locator']>, router: Router, n: string) =>
  editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, n)}"]`);

// The strip's order, as basenames.
async function stripOrder(editor: ReturnType<Page['locator']>): Promise<string[]> {
  const ids = await editor
    .locator('[data-testid="edit-tabs"] [data-testid^="edit-tab-"]:not([data-testid^="edit-tab-close-"])')
    .evaluateAll((els) => els.map((e) => e.getAttribute('data-testid') ?? ''));
  return ids.map((id) => id.split('/').pop() ?? id);
}

test.describe('wash-edit tab navigation', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });
  test.setTimeout(40_000);

  test('Alt+N, Alt+PageUp/PageDown and Ctrl+Tab move between tabs', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    const [a, b, c] = ['a.txt', 'b.txt', 'c.txt'].map((n) => tabOf(editor, router, n));
    await expect(c).toHaveAttribute('data-active', 'true');

    await editor.press('Alt+2');
    await expect(b).toHaveAttribute('data-active', 'true');
    await expect(editor.locator('.cm-content')).toContainText('bravo');

    await editor.press('Alt+PageDown');
    await expect(c).toHaveAttribute('data-active', 'true');
    // Wraps at the end.
    await editor.press('Alt+PageDown');
    await expect(a).toHaveAttribute('data-active', 'true');
    await editor.press('Alt+PageUp');
    await expect(c).toHaveAttribute('data-active', 'true');

    await editor.press('Control+Tab');
    await expect(a).toHaveAttribute('data-active', 'true');
    await editor.press('Control+Shift+Tab');
    await expect(c).toHaveAttribute('data-active', 'true');

    await editor.press('Alt+1');
    await expect(a).toHaveAttribute('data-active', 'true');
    await expect(editor.locator('.cm-content')).toContainText('alpha');
    // Alt+9 with three tabs: nothing to jump to, nothing changes.
    await editor.press('Alt+9');
    await expect(a).toHaveAttribute('data-active', 'true');
  });

  test('dragging a tab reorders the strip, and the order survives a reload', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    expect(await stripOrder(editor)).toEqual(['a.txt', 'b.txt', 'c.txt']);

    // c dropped on a lands where a was.
    await tabOf(editor, router, 'c.txt').dragTo(tabOf(editor, router, 'a.txt'));
    await expect.poll(() => stripOrder(editor)).toEqual(['c.txt', 'a.txt', 'b.txt']);
    // The dragged tab stays the active one and its buffer is intact.
    await expect(tabOf(editor, router, 'c.txt')).toHaveAttribute('data-active', 'true');
    await expect(editor.locator('.cm-content')).toContainText('charlie');

    // a dropped on b (dragging rightwards) lands after b.
    await tabOf(editor, router, 'a.txt').dragTo(tabOf(editor, router, 'b.txt'));
    await expect.poll(() => stripOrder(editor)).toEqual(['c.txt', 'b.txt', 'a.txt']);

    // BE half: the order is what the router persisted — a reload restores it.
    const cursor = router.logCursor();
    await router.waitForLog(/app_state\.set instance=/, 5_000, cursor);
    await page.reload();
    const editor2 = page.locator('wash-app-edit');
    await expect(editor2).toBeVisible();
    await expect.poll(() => stripOrder(editor2)).toEqual(['c.txt', 'b.txt', 'a.txt']);
  });

  test('middle-click closes a clean tab and asks about a dirty one', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await tabOf(editor, router, 'b.txt').click({ button: 'middle' });
    await expect(tabOf(editor, router, 'b.txt')).toHaveCount(0);
    await expect.poll(() => stripOrder(editor)).toEqual(['a.txt', 'c.txt']);
    // The active tab (c) was not the one closed, so it stays active.
    await expect(tabOf(editor, router, 'c.txt')).toHaveAttribute('data-active', 'true');

    await editor.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('x');
    await expect(tabOf(editor, router, 'c.txt')).toHaveAttribute('data-dirty', 'true');
    await tabOf(editor, router, 'c.txt').click({ button: 'middle' });
    const dialog = page.locator('[data-testid="edit-close-dialog"]');
    await expect(dialog).toBeVisible();
    await dialog.locator('[data-testid="edit-close-cancel"]').click();
    await expect(tabOf(editor, router, 'c.txt')).toBeVisible();
    await expect(editor.locator('.cm-content')).toContainText('charlie');
  });
});
