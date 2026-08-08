// A symlink to a folder must behave as a folder.
//
// The BE types an entry by the LINK's own kind ("symlink"), so every FE test
// of `type === 'dir'` silently excluded linked folders. The directory-mode
// file picker filtered them out of the list entirely — you could not choose
// your own symlinked project folder in the Agent app, wash-music or
// wash-imageview — and the file tree gave them no expand chevron. fm got away
// with it: its double-click round-trips through the router's open routing,
// which resolves the link server-side.
//
// internal/fs now reports link_type (what the link resolves to) and the FE
// decides with isDirLike(). This spec drives the path that was broken, end to
// end through a real symlink on disk: pick a linked folder in a directory
// picker and prove the app actually scanned the target.
//
// Sibling coverage: internal/fs TestListSymlinkTargetType (link_type for
// dir/file/dangling), fm.spec.ts "symlinked folder" (fm navigation).

import { test, expect } from '../fixtures/router';
import { mkdirSync, writeFileSync, symlinkSync } from 'node:fs';
import { join } from 'node:path';

// 1x1 PNG — imageview only needs the header to list it as an image.
const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64',
);

function seedLinkedGallery(root: string): void {
  mkdirSync(join(root, 'real-gallery'));
  writeFileSync(join(root, 'real-gallery', 'apple.png'), PNG);
  writeFileSync(join(root, 'plain.txt'), 'not a folder\n');
  // The entry under test: a link to the folder.
  symlinkSync('real-gallery', join(root, 'linked-gallery'));
  // Two links that must NOT pass as folders.
  symlinkSync('plain.txt', join(root, 'linked-file'));
  symlinkSync('nowhere', join(root, 'dangling'));
}

test.use({
  routerOpts: { apps: ['session', 'about', 'imageview'], fmRoot: true, fmSeed: seedLinkedGallery },
});

test('a symlinked folder is offered by a directory picker, and opens its target', async ({ page, router }) => {
  test.setTimeout(45_000);
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Image Viewer$/ }).click();
  await expect(page.locator('wash-app-imageview')).toBeVisible();

  await page.locator('[data-testid="iv-open-folder"]').click();
  const picker = page.locator('[data-testid="iv-picker"]');
  await expect(picker).toBeVisible();

  // Navigate to the sandbox root via the path bar.
  const bar = picker.locator('[data-testid="fp-path"]');
  await bar.click();
  await bar.fill(router.fmRoot);
  await bar.press('Enter');

  // The linked folder is listed — this is the bug: directory mode used to
  // filter on type === 'dir' and drop it.
  await expect(picker.locator('[data-testid="fp-entry-linked-gallery"]')).toBeVisible({ timeout: 10_000 });
  // The real folder is there too, and the two non-folder links are not:
  // "unknown" (a dangling link) must never read as "directory".
  await expect(picker.locator('[data-testid="fp-entry-real-gallery"]')).toBeVisible();
  await expect(picker.locator('[data-testid="fp-entry-linked-file"]')).toHaveCount(0);
  await expect(picker.locator('[data-testid="fp-entry-dangling"]')).toHaveCount(0);

  // Choosing it must scan the TARGET, not fail on the link.
  await picker.locator('[data-testid="fp-entry-linked-gallery"]').click();
  await picker.locator('[data-testid="fp-confirm"]').click();
  await expect(picker).toBeHidden();
  await expect(page.locator('[data-testid="iv-thumb-apple.png"]')).toBeVisible({ timeout: 10_000 });
});

test('double-clicking a symlinked folder in a picker navigates into it', async ({ page, router }) => {
  test.setTimeout(45_000);
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Image Viewer$/ }).click();
  await expect(page.locator('wash-app-imageview')).toBeVisible();

  // Open mode (pick an image) lists files as well as folders.
  await page.locator('[data-testid="iv-open-image"]').click();
  const picker = page.locator('[data-testid="iv-picker"]');
  await expect(picker).toBeVisible();

  const bar = picker.locator('[data-testid="fp-path"]');
  await bar.click();
  await bar.fill(router.fmRoot);
  await bar.press('Enter');

  // Entering a linked folder used to be impossible — the double-click
  // handler only navigated for type === 'dir'.
  await picker.locator('[data-testid="fp-entry-linked-gallery"]').dblclick();
  await expect(picker.locator('[data-testid="fp-entry-apple.png"]')).toBeVisible({ timeout: 10_000 });
});
