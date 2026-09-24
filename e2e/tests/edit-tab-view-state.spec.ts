// Per-tab word wrap and syntax override in wash-edit
// (docs/Review-findings.md P2 → edit).
//
// Both used to be one window-wide signal that was reset on every tab
// switch and never persisted: turning wrap on for a log, or forcing a
// syntax onto an extensionless file, lasted exactly as long as you
// stayed on that tab. They are properties of what you are looking at,
// so they now live on the tab and in the window's persisted state.

import { test, expect } from '../fixtures/router';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'long.log'), `${'word '.repeat(200)}\n`);
  writeFileSync(join(root, 'plain.txt'), 'nothing special\n');
}

type Page = import('@playwright/test').Page;
type Router = import('../fixtures/router').RouterHandle;

async function openEditor(page: Page, router: Router) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor).toBeVisible();
  await expect(editor.locator('[data-testid="edit-entry-long.log"]')).toBeVisible();
  return editor;
}

const wrapped = (page: Page) =>
  page.locator('wash-app-edit .cm-content').evaluate((el) => el.classList.contains('cm-lineWrapping'));

test.describe('wash-edit per-tab view state', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });
  test.setTimeout(40_000);

  test('wrap and syntax stay with their tab, and survive a reload', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-long.log"]').dblclick();
    await expect(editor.locator('[data-testid="edit-status-lang"]')).toHaveText('Plain Text');
    expect(await wrapped(page)).toBe(false);

    // Clicked through the DOM: the Syntax menu lists every language and
    // is taller than this viewport, so its last row (Word Wrap) is off
    // screen. That the menu needs a scroll is a real gap, and not this
    // test's subject.
    await editor.locator('[data-testid="edit-menubar-syntax"]').click();
    await page.locator('[data-testid="edit-menu-wrap"]').evaluate((el) => (el as HTMLElement).click());
    await expect.poll(() => wrapped(page)).toBe(true);
    // A syntax the path would never have chosen.
    await editor.locator('[data-testid="edit-status-lang"]').click();
    await page.locator('[data-testid="edit-menu-lang-go"]').evaluate((el) => (el as HTMLElement).click());
    await expect(editor.locator('[data-testid="edit-status-lang"]')).toHaveText('Go');

    // The other tab is untouched — this is per tab, not per window.
    await editor.locator('[data-testid="edit-entry-plain.txt"]').dblclick();
    await expect(editor.locator('[data-testid="edit-status-lang"]')).toHaveText('Plain Text');
    await expect.poll(() => wrapped(page)).toBe(false);

    // And switching back restores them.
    await editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'long.log')}"]`).click();
    await expect(editor.locator('[data-testid="edit-status-lang"]')).toHaveText('Go');
    await expect.poll(() => wrapped(page)).toBe(true);

    // Persisted with the window, so a reload comes back the same.
    const cursor = router.logCursor();
    await router.waitForLog(/app_state\.set instance=/, 5_000, cursor);
    await page.reload();
    const editor2 = page.locator('wash-app-edit');
    await expect(editor2).toBeVisible();
    await expect(editor2.locator('[data-testid="edit-status-lang"]')).toHaveText('Go');
    await expect.poll(() => wrapped(page)).toBe(true);
  });
});
