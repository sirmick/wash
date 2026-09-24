// wash-edit quick open (docs/Review-findings.md P2 → edit): the Ctrl+P
// fuzzy palette over the tree root, and the recent-files list it shows
// by default — which is also File ▸ Open Recent and lives in the
// desktop-wide prefs file, not the per-window state blob.
//
// BE half: the palette's listing is the edit BE's bounded `find` walk,
// so the router log is asserted for it as well as the FE result.

import { test, expect } from '../fixtures/router';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'alpha.txt'), 'alpha\n');
  writeFileSync(join(root, 'bravo.txt'), 'bravo\n');
  mkdirSync(join(root, 'sub', 'deeper'), { recursive: true });
  writeFileSync(join(root, 'sub', 'deeper', 'charlie.txt'), 'charlie\n');
  // Never listed: a dependency dir is where a tree's file count lives.
  mkdirSync(join(root, 'node_modules', 'dep'), { recursive: true });
  writeFileSync(join(root, 'node_modules', 'dep', 'index.js'), 'x\n');
}

type Page = import('@playwright/test').Page;
type Router = import('../fixtures/router').RouterHandle;

async function openEditor(page: Page, router: Router) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor).toBeVisible();
  await expect(editor.locator('[data-testid="edit-entry-alpha.txt"]')).toBeVisible();
  return editor;
}

test.describe('wash-edit quick open', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });
  test.setTimeout(40_000);

  test('Ctrl+P ranks the tree and Enter opens the pick', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    const cursor = router.logCursor();

    await editor.press('Control+p');
    const palette = page.locator('[data-testid="edit-quick-open"]');
    await expect(palette).toBeVisible();
    // Nothing opened yet, so the default list is empty rather than wrong.
    await expect(palette.locator('[data-testid="edit-qo-empty"]')).toContainText('no recent files');

    // BE half: the walk ran, bounded, and reported what it found.
    await router.waitForLog(/wash-edit: find root=.* files=\d+ truncated=false cancelled=false/, 10_000, cursor);
    await expect(palette.locator('[data-testid="edit-qo-status"]')).toContainText('3 files under');

    await palette.locator('[data-testid="edit-qo-input"]').fill('charl');
    const hit = palette.locator('[data-testid="edit-qo-item-sub/deeper/charlie.txt"]');
    await expect(hit).toBeVisible();
    await expect(hit).toHaveAttribute('data-selected', 'true');
    // The dependency dir is not in the listing at all.
    await palette.locator('[data-testid="edit-qo-input"]').fill('index');
    await expect(palette.locator('[data-testid="edit-qo-empty"]')).toContainText('no matches');

    await palette.locator('[data-testid="edit-qo-input"]').fill('charl');
    await page.keyboard.press('Enter');
    await expect(palette).toHaveCount(0);
    const tab = editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'sub/deeper/charlie.txt')}"]`);
    await expect(tab).toHaveAttribute('data-active', 'true');
    await expect(editor.locator('.cm-content')).toContainText('charlie');
  });

  test('recent files are the default list and File ▸ Open Recent', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-alpha.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('alpha');
    await editor.locator('[data-testid="edit-entry-bravo.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('bravo');

    // Most recent first, no query typed.
    await editor.press('Control+p');
    const palette = page.locator('[data-testid="edit-quick-open"]');
    await expect(palette.locator('[data-testid="edit-qo-item-bravo.txt"]')).toBeVisible();
    await expect(palette.locator('[data-testid="edit-qo-item-alpha.txt"]')).toBeVisible();
    const order = await palette
      .locator('[data-testid^="edit-qo-item-"]')
      .evaluateAll((els) => els.map((e) => (e.getAttribute('data-testid') ?? '').replace('edit-qo-item-', '')));
    expect(order).toEqual(['bravo.txt', 'alpha.txt']);
    await page.keyboard.press('Escape');
    await expect(palette).toHaveCount(0);

    // Same list under File ▸ Open Recent, and it opens the file.
    await editor.locator('[data-testid="edit-tab-close-' + join(router.fmRoot, 'alpha.txt') + '"]').click();
    await editor.locator('[data-testid="edit-menubar-file"]').click();
    await page.locator('[data-testid="edit-menu-open-recent"]').click();
    const recent = page.locator('[data-testid="edit-menu-recent"]');
    await expect(recent).toBeVisible();
    await recent.locator(`[data-testid="edit-menu-recent-${join(router.fmRoot, 'alpha.txt')}"]`).click();
    await expect(editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'alpha.txt')}"]`)).toHaveAttribute('data-active', 'true');

    // The list is desktop-wide prefs, not window state: a reload keeps it.
    await page.reload();
    const editor2 = page.locator('wash-app-edit');
    await expect(editor2).toBeVisible();
    await editor2.press('Control+p');
    await expect(page.locator('[data-testid="edit-qo-item-bravo.txt"]')).toBeVisible();
  });
});
