// "Reveal in Files": the editor's sidebar button opens a Files (fm) window
// AT the active file's folder instead of fm's default root. The chain is
// edit FE -> edit BE spawn{open} -> router spawn.request{open} -> fm
// spawned with `--open <path>` -> fm BE LaunchOpenPath -> initial list of
// that folder.
//
// Both halves: fm's path input shows the folder (FE); fm's BE logs the
// launch path it honoured (router log).

import { test, expect } from '../fixtures/router';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';

function seed(root: string): void {
  writeFileSync(join(root, 'top.txt'), 'top\n');
  mkdirSync(join(root, 'sub'));
  writeFileSync(join(root, 'sub', 'x.txt'), 'x marks the spot\n');
}

test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });

async function openEditor(page: Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor.locator('[data-testid="edit-entry-sub"]')).toBeVisible();
  return editor;
}

test.describe('wash-edit reveal in Files', () => {
  test('reveals the active file: fm opens listing its folder', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-sub"]').dblclick();
    await editor.locator('[data-testid="edit-entry-x.txt"]').dblclick();
    await expect(editor.locator('.cm-content')).toContainText('x marks the spot');

    await expect(page.locator('wash-app-fm')).toHaveCount(0);
    await editor.locator('[data-testid="edit-reveal-in-fm"]').click();

    const fm = page.locator('wash-app-fm');
    await expect(fm).toBeVisible({ timeout: 5_000 });
    const sub = join(router.fmRoot, 'sub');
    await expect(fm.locator('[data-testid="fm-path"]')).toHaveValue(sub, { timeout: 5_000 });
    await expect(fm.locator('[data-testid="fm-entry-x.txt"]')).toBeVisible();

    // BE half: fm was launched with the path and resolved it to the folder.
    await router.waitForLog(new RegExp(`wash-fm: launch open=${sub}/x\\.txt dir=${sub}`), 5_000);
  });

  test('with no file open, reveals the sidebar selection or the project root', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-entry-sub"]').click();
    await editor.locator('[data-testid="edit-reveal-in-fm"]').click();

    const fm = page.locator('wash-app-fm');
    await expect(fm).toBeVisible({ timeout: 5_000 });
    await expect(fm.locator('[data-testid="fm-path"]')).toHaveValue(join(router.fmRoot, 'sub'), { timeout: 5_000 });
    await expect(fm.locator('[data-testid="fm-entry-x.txt"]')).toBeVisible();
  });
});
