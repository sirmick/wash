// fm FE tests for the mutation UI: rename, delete, new file, new
// folder. The BE half is exercised separately in fm-be.spec.ts; here
// we prove the Solid wiring drives those ops through clicks.
//
// Every test runs against a sandboxed fm (WASH_FM_ROOT) so a UI
// misclick can't escape the per-test tmpdir.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { existsSync, readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

// openFm launches the Files app via the chrome launcher and waits
// for the BE's initial list_ok (root listing) to land — the
// fixture's WASH_FM_ROOT means fm starts inside the sandbox, so
// hello.txt / binary.bin / docs are visible without navigation.
async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  // Wait for the seeded entries to render — proves the initial
  // list landed and the FE picked up WASH_FM_ROOT as $HOME.
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);
}

test.describe('fm FE mutations', () => {
  test('context menu shows Rename + Delete', async ({ page, router }) => {
    await openFm(page, router);
    await page.locator('[data-testid="fm-entry-hello.txt"]').click({ button: 'right' });
    await expect(page.locator('[data-testid="fm-ctx-rename"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-ctx-delete"]')).toBeVisible();
  });

  test('rename: inline edit commits to the FS', async ({ page, router }) => {
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-hello.txt"]').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-rename"]').click();

    const input = page.locator('[data-testid="fm-rename-input"]');
    await expect(input).toBeVisible();
    await expect(input).toBeFocused();
    await input.fill('hello-renamed.txt');
    await input.press('Enter');

    await expect(page.locator('[data-testid="fm-entry-hello-renamed.txt"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toHaveCount(0);
    expect(existsSync(join(router.fmRoot, 'hello-renamed.txt'))).toBe(true);
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(false);
    expect(readFileSync(join(router.fmRoot, 'hello-renamed.txt'), 'utf8')).toBe('hello world\n');
  });

  test('rename: Escape cancels without touching the FS', async ({ page, router }) => {
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-hello.txt"]').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-rename"]').click();
    const input = page.locator('[data-testid="fm-rename-input"]');
    await input.fill('SHOULD-NOT-COMMIT.txt');
    await input.press('Escape');

    await expect(input).toBeHidden();
    await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(true);
    expect(existsSync(join(router.fmRoot, 'SHOULD-NOT-COMMIT.txt'))).toBe(false);
  });

  test('delete: confirm overlay → Delete removes the file', async ({ page, router }) => {
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-hello.txt"]').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-delete"]').click();

    const overlay = page.locator('[data-testid="fm-confirm-delete"]');
    await expect(overlay).toBeVisible();
    await expect(page.locator('[data-testid="fm-confirm-delete-name"]')).toContainText('hello.txt');

    await page.locator('[data-testid="fm-confirm-delete-yes"]').click();
    await expect(overlay).toBeHidden();
    await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toHaveCount(0);
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(false);
  });

  test('delete: Cancel leaves the file alone', async ({ page, router }) => {
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-hello.txt"]').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-delete"]').click();
    await expect(page.locator('[data-testid="fm-confirm-delete"]')).toBeVisible();

    await page.locator('[data-testid="fm-confirm-delete-cancel"]').click();
    await expect(page.locator('[data-testid="fm-confirm-delete"]')).toBeHidden();
    await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(true);
  });

  test('new file: toolbar opens input → Enter creates an empty file', async ({ page, router }) => {
    await openFm(page, router);

    await page.locator('[data-testid="fm-new-file"]').click();
    const input = page.locator('[data-testid="fm-pending-new-input"]');
    await expect(input).toBeVisible();
    await expect(input).toBeFocused();
    await input.fill('fresh.txt');
    await input.press('Enter');

    await expect(page.locator('[data-testid="fm-entry-fresh.txt"]')).toBeVisible();
    const created = join(router.fmRoot, 'fresh.txt');
    expect(existsSync(created)).toBe(true);
    expect(readFileSync(created, 'utf8')).toBe('');
  });

  test('new folder: toolbar opens input → Enter creates a directory', async ({ page, router }) => {
    await openFm(page, router);

    await page.locator('[data-testid="fm-new-folder"]').click();
    const input = page.locator('[data-testid="fm-pending-new-input"]');
    await expect(input).toBeVisible();
    await input.fill('newdir');
    await input.press('Enter');

    const row = page.locator('[data-testid="fm-entry-newdir"]');
    await expect(row).toBeVisible();
    await expect(row).toHaveAttribute('data-type', 'dir');
    expect(existsSync(join(router.fmRoot, 'newdir'))).toBe(true);
  });

  test('new file: Escape closes the pending row without creating anything', async ({ page, router }) => {
    await openFm(page, router);

    await page.locator('[data-testid="fm-new-file"]').click();
    const input = page.locator('[data-testid="fm-pending-new-input"]');
    await input.fill('aborted.txt');
    await input.press('Escape');

    await expect(input).toBeHidden();
    await expect(page.locator('[data-testid="fm-entry-aborted.txt"]')).toHaveCount(0);
    expect(existsSync(join(router.fmRoot, 'aborted.txt'))).toBe(false);
  });

  test('error surfaces in status bar: rename onto existing target', async ({ page, router }) => {
    // Seed an extra file alongside hello.txt so a rename collision
    // path is reachable.
    writeFileSync(join(router.fmRoot, 'taken.txt'), 'taken\n');
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-hello.txt"]').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-rename"]').click();
    const input = page.locator('[data-testid="fm-rename-input"]');
    await input.fill('taken.txt');
    await input.press('Enter');

    // Status bar should now carry the rename error. Both files
    // remain untouched.
    await expect(page.locator('[data-testid="fm-status"]')).toContainText(/rename/);
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(true);
    expect(readFileSync(join(router.fmRoot, 'taken.txt'), 'utf8')).toBe('taken\n');
  });

  test('new file inside subdir: selection sets the parent', async ({ page, router }) => {
    mkdirSync(join(router.fmRoot, 'target'));
    await openFm(page, router);

    // Click into the target dir so it becomes "current path".
    await page.locator('[data-testid="fm-entry-target"]').click();
    await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(join(router.fmRoot, 'target'));

    await page.locator('[data-testid="fm-new-file"]').click();
    const input = page.locator('[data-testid="fm-pending-new-input"]');
    await input.fill('inner.txt');
    await input.press('Enter');

    expect(existsSync(join(router.fmRoot, 'target', 'inner.txt'))).toBe(true);
    // Sibling root must NOT have gained a file.
    expect(existsSync(join(router.fmRoot, 'inner.txt'))).toBe(false);
  });
});
