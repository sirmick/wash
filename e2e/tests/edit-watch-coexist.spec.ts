// Co-existing fs.watch subscribers. The editor's sidebar and the
// embedded FilePicker both subscribe to the same path (the root)
// — the original implementation closed the shared sub on the
// first unwatch, which silently broke the surviving subscriber.
// EnableFilePicker now refcounts subs; this spec is the contract
// test for that.
//
// Scenario:
//   1. Open editor — sidebar subscribes to root.
//   2. Open picker (same root) — bumps refcount.
//   3. Close picker without picking → unwatch decrements refcount.
//   4. Drop a file from outside → sidebar still sees it.

import { test, expect } from '../fixtures/router';
import { writeFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'seed.txt'), 'seed\n');
}

test.describe('wash-edit watch refcount', () => {
  test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });

  test('sidebar watch survives picker close', async ({ page, router }) => {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
    const editor = page.locator('wash-app-edit');
    await expect(editor).toBeVisible();
    await expect(editor.locator('[data-testid="edit-entry-seed.txt"]')).toBeVisible();

    // Sanity: sidebar refreshes on external create (proves the
    // sidebar's watch is live before we touch the picker).
    const before = 'before-' + Date.now() + '.txt';
    writeFileSync(join(router.fmRoot, before), 'x');
    await expect(editor.locator(`[data-testid="edit-entry-${before}"]`))
      .toBeVisible({ timeout: 2_000 });

    // Open the picker on the same root, then close without
    // picking. With refcount the sidebar's sub survives; without
    // refcount this is what would strand it.
    await editor.click();
    await page.keyboard.press('Control+o');
    const picker = page.locator('[data-testid="edit-picker"]');
    await expect(picker).toBeVisible();
    await picker.locator('[data-testid="fp-cancel"]').click();
    await expect(picker).not.toBeVisible();

    // External create AFTER the picker close. If the picker's
    // unwatch ate the sidebar's sub, this never appears.
    const after = 'after-' + Date.now() + '.txt';
    writeFileSync(join(router.fmRoot, after), 'x');
    await expect(editor.locator(`[data-testid="edit-entry-${after}"]`))
      .toBeVisible({ timeout: 3_000 });

    try { rmSync(join(router.fmRoot, before)); } catch {}
    try { rmSync(join(router.fmRoot, after)); } catch {}
  });

  test('picker refreshes its cwd on each open (no stale snapshot)', async ({ page, router }) => {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
    const editor = page.locator('wash-app-edit');
    await expect(editor).toBeVisible();
    await editor.click();

    // Open picker (initial list against fmRoot), then close.
    await editor.click();
    await page.keyboard.press('Control+o');
    const picker = page.locator('[data-testid="edit-picker"]');
    await expect(picker).toBeVisible();
    await picker.locator('[data-testid="fp-path"]').fill(router.fmRoot);
    await picker.locator('[data-testid="fp-path"]').press('Enter');
    await expect(picker.locator('[data-testid="fp-entry-seed.txt"]')).toBeVisible();
    await picker.locator('[data-testid="fp-cancel"]').click();
    await expect(picker).not.toBeVisible();

    // Change disk while the picker is closed (no event will fire
    // to the picker — it's unsubscribed). On reopen the picker
    // must force-reload so it sees this file.
    const gap = 'while-closed-' + Date.now() + '.txt';
    writeFileSync(join(router.fmRoot, gap), 'x');

    // Reopen and confirm the new file is present without any
    // user-driven refresh action. Re-focus the editor first
    // because the picker close may have moved focus elsewhere.
    await editor.click();
    await page.keyboard.press('Control+o');
    await expect(picker).toBeVisible();
    await expect(picker.locator(`[data-testid="fp-entry-${gap}"]`))
      .toBeVisible({ timeout: 3_000 });

    try { rmSync(join(router.fmRoot, gap)); } catch {}
  });
});
