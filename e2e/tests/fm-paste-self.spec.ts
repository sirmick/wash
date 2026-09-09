// fm paste into itself / its own folder (docs/Review-findings.md 2026-09-08
// P0 #3). Two layers, each proven on its own: the fm FE filters the plan
// before dispatch (status line, no bulk job), and the bulk service refuses
// a job that reaches it anyway (router log + toast, nothing on disk).

import { test, expect } from '../fixtures/router';
import { existsSync, mkdirSync, readdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'hello.txt'), 'hello world\n');
  mkdirSync(join(root, 'photos'));
  writeFileSync(join(root, 'photos', 'one.txt'), '1');
  mkdirSync(join(root, 'photos', 'inner'));
}

test.use({ routerOpts: { apps: ['session', 'fm', 'bulk', 'notify'], fmRoot: true, fmSeed: seed } });

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
}

test.describe('fm paste into self', () => {
  test.setTimeout(30_000);

  test('copy a file, paste into the same folder: nothing is enqueued, the source survives', async ({ page, router }) => {
    await openFm(page, router);
    await page.locator('[data-testid="fm-entry-hello.txt"]').click();
    await page.locator('wash-app-fm').press('Control+c');
    await expect(page.locator('[data-testid="fm-status"]')).toContainText(/copied 1/);
    // Still selected: the paste destination is the file's own folder.
    await page.locator('wash-app-fm').press('Control+v');
    await expect(page.locator('[data-testid="fm-status"]')).toContainText(/already in this folder/);
    await page.waitForTimeout(300);
    expect(router.log()).not.toMatch(/bulk-ops job=\S+ op=copy/);
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(true);
  });

  test('cut a file, paste into the same folder: the file is not deleted', async ({ page, router }) => {
    await openFm(page, router);
    await page.locator('[data-testid="fm-entry-hello.txt"]').click();
    await page.locator('wash-app-fm').press('Control+x');
    await expect(page.locator('[data-testid="fm-status"]')).toContainText(/cut 1/);
    await page.locator('wash-app-fm').press('Control+v');
    await expect(page.locator('[data-testid="fm-status"]')).toContainText(/already in this folder/);
    await page.waitForTimeout(300);
    expect(router.log()).not.toMatch(/bulk-ops job=\S+ op=move/);
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(true);
  });

  test('copy a folder, paste into that folder: refused, no recursion on disk', async ({ page, router }) => {
    await openFm(page, router);
    await page.locator('[data-testid="fm-entry-photos"]').click();
    await page.locator('wash-app-fm').press('Control+c');
    await expect(page.locator('[data-testid="fm-status"]')).toContainText(/copied 1/);
    // The selected folder is the destination.
    await page.locator('wash-app-fm').press('Control+v');
    await expect(page.locator('[data-testid="fm-status"]')).toContainText(/cannot copy photos into itself/);
    await page.waitForTimeout(300);
    expect(router.log()).not.toMatch(/bulk-ops job=\S+ op=copy/);
    expect(readdirSync(join(router.fmRoot, 'photos')).sort()).toEqual(['inner', 'one.txt']);
  });

  test('the bulk service itself refuses a job into its own subtree', async ({ page, router }) => {
    // Bypass fm's pre-filter and speak to wash-bulk directly, the way any
    // other caller could.
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const bulk = await router.controlRequest({ t: 'launch', app_id: 'com.wash.bulk' });
    await router.sendAppMsg(String(bulk.instance_id), {
      kind: 'enqueue', id: 'self-1', op: 'copy',
      paths: [join(router.fmRoot, 'photos')],
      dest: join(router.fmRoot, 'photos', 'inner'),
    });
    await router.waitForLog(/bulk-ops enqueue rejected/, 5_000);
    await expect(page.locator('[data-testid="notification"]').filter({ hasText: 'Copy failed' })).toBeVisible({ timeout: 10_000 });
    expect(router.log()).not.toMatch(/bulk-ops job=\S+ op=copy status=queued/);
    expect(readdirSync(join(router.fmRoot, 'photos', 'inner'))).toEqual([]);
  });
});
