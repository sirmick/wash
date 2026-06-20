// fm display hints: the tree colours/badges entries by what the *current
// user* can do with them — executable (green), read-only / un-enterable
// (dim), broken symlink (red), hidden dotfile (dim) — plus a setuid/setgid
// badge. The BE (internal/fs) computes the uid-aware flags; the FE turns
// them into a data-hint attribute (asserted here) and the matching colour.
//
// Device nodes and mount points need root/mknod, so those are covered by Go
// unit tests (internal/fs/hints_test.go), not here.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { chmodSync, symlinkSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seedHints(root: string): void {
  seedSimpleTree(root); // hello.txt, docs/
  writeFileSync(join(root, 'run.sh'), '#!/bin/sh\necho hi\n');
  chmodSync(join(root, 'run.sh'), 0o755); // executable

  writeFileSync(join(root, 'locked.txt'), 'no writing\n');
  chmodSync(join(root, 'locked.txt'), 0o444); // read-only to its owner

  writeFileSync(join(root, 'setid.bin'), 'pretend binary\n');
  chmodSync(join(root, 'setid.bin'), 0o4755); // setuid bit

  symlinkSync(join(root, 'nope-does-not-exist'), join(root, 'dangling')); // broken symlink

  writeFileSync(join(root, '.secret'), 'hidden\n'); // dotfile
}

test.use({ routerOpts: { apps: ['session', 'about', 'fm'], fmRoot: true, fmSeed: seedHints } });

async function openFm(
  page: import('@playwright/test').Page,
  router: import('../fixtures/router').RouterHandle,
) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-run.sh"]')).toBeVisible();
}

test.describe('fm display hints', () => {
  test('executable file is marked exec (and a plain file is not)', async ({ page, router }) => {
    await openFm(page, router);
    await expect(page.locator('[data-testid="fm-entry-run.sh"]')).toHaveAttribute('data-hint', 'exec');
    // A normal 0644 file has no hint.
    await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).not.toHaveAttribute('data-hint');
  });

  test('read-only file is dimmed', async ({ page, router }) => {
    await openFm(page, router);
    await expect(page.locator('[data-testid="fm-entry-locked.txt"]')).toHaveAttribute('data-hint', 'readonly');
  });

  test('broken symlink is flagged', async ({ page, router }) => {
    await openFm(page, router);
    await expect(page.locator('[data-testid="fm-entry-dangling"]')).toHaveAttribute('data-hint', 'broken');
  });

  test('setuid file shows the security badge', async ({ page, router }) => {
    await openFm(page, router);
    await expect(
      page.locator('[data-testid="fm-entry-setid.bin"] [data-testid="fm-setid-badge"]'),
    ).toBeVisible();
    // setuid bit also makes it executable → exec wins the colour.
    await expect(page.locator('[data-testid="fm-entry-setid.bin"]')).toHaveAttribute('data-hint', 'exec');
  });

  test('hidden dotfile is dimmed (once show-hidden is on)', async ({ page, router }) => {
    await openFm(page, router);
    // Surface dotfiles via the sort menu's show-hidden toggle.
    await page.locator('[data-testid="fm-sort"]').click();
    await page.locator('[data-testid="fm-show-hidden"]').click();
    await expect(page.locator('[data-testid="fm-entry-.secret"]')).toHaveAttribute('data-hint', 'hidden');
  });
});
