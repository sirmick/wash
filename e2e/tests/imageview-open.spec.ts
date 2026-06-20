// imageview Open dialog: the viewer can open a folder (scan it) or a single
// image (scan its folder + select it) via @wash/ui's <FilePicker>, the same
// bridge wash-edit uses (sdk.EnableFilePicker on the BE). Launched standalone
// from the start menu — not via fm open-routing — to exercise the in-app
// dialog. The picker is unconfined here (no sandbox root), so we navigate by
// typing the seeded path into the picker's path bar.

import { test, expect } from '../fixtures/router';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

// A valid 8×8 PNG (decodable by internal/thumbs' stdlib image/png).
const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAKklEQVR4nGK+Y2NzTm7fPrlzaCSDjc2dOzY2mCQDVlEbmzsDqgMQAAD//+8yV15/i6x3AAAAAElFTkSuQmCC',
  'base64',
);

function seedGallery(root: string): void {
  mkdirSync(join(root, 'gallery'));
  writeFileSync(join(root, 'gallery', 'apple.png'), PNG);
  writeFileSync(join(root, 'gallery', 'banana.png'), PNG);
}

test.use({ routerOpts: { apps: ['session', 'about', 'imageview'], fmRoot: true, fmSeed: seedGallery } });

async function openViewer(
  page: import('@playwright/test').Page,
  router: import('../fixtures/router').RouterHandle,
) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Image Viewer$/ }).click();
  await expect(page.locator('wash-app-imageview')).toBeVisible();
}

// Type an absolute path into the picker's path bar and load it.
async function gotoInPicker(page: import('@playwright/test').Page, path: string) {
  const bar = page.locator('[data-testid="iv-picker"] [data-testid="fp-path"]');
  await bar.fill(path);
  await bar.press('Enter');
}

test.describe('imageview open dialog', () => {
  test('Open Folder button scans the chosen folder', async ({ page, router }) => {
    await openViewer(page, router);

    await page.locator('[data-testid="iv-open-folder"]').click();
    await expect(page.locator('[data-testid="iv-picker"]')).toBeVisible();
    // Directory mode chooses folders.
    await expect(page.locator('[data-testid="iv-picker"]')).toContainText('Open Folder');

    await gotoInPicker(page, router.fmRoot);
    await page.locator('[data-testid="iv-picker"] [data-testid="fp-entry-gallery"]').click();
    await page.locator('[data-testid="iv-picker"] [data-testid="fp-confirm"]').click();

    // Picker closed; the gallery's images now populate the list + main view.
    await expect(page.locator('[data-testid="iv-picker"]')).toBeHidden();
    await expect(page.locator('[data-testid="iv-thumb-apple.png"]')).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('[data-testid="iv-image"]')).toBeVisible({ timeout: 10_000 });
  });

  test('Open Image button scans the folder and selects the chosen image', async ({ page, router }) => {
    await openViewer(page, router);

    await page.locator('[data-testid="iv-open-image"]').click();
    await expect(page.locator('[data-testid="iv-picker"]')).toBeVisible();

    await gotoInPicker(page, join(router.fmRoot, 'gallery'));
    // Pick the second image so we can assert it became the selected one.
    await page.locator('[data-testid="iv-picker"] [data-testid="fp-entry-banana.png"]').click();
    await page.locator('[data-testid="iv-picker"] [data-testid="fp-confirm"]').click();

    await expect(page.locator('[data-testid="iv-picker"]')).toBeHidden();
    await expect(page.locator('[data-testid="iv-image"]')).toBeVisible({ timeout: 10_000 });
    // The caption names the selected image — proving the file was pre-selected,
    // not just that its folder was scanned.
    await expect(page.locator('[data-testid="iv-caption"]')).toContainText('banana.png');
  });

  test('Ctrl+O opens the image dialog', async ({ page, router }) => {
    await openViewer(page, router);

    // Focus the app surface so the keydown lands on it, then Ctrl+O.
    await page.locator('wash-app-imageview').click();
    await page.keyboard.press('Control+o');
    await expect(page.locator('[data-testid="iv-picker"]')).toBeVisible();
    await expect(page.locator('[data-testid="iv-picker"]')).toContainText('Open File');
  });
});

// Regression: the rest of the suite always sets WASH_FM_ROOT, which the router
// adopts as its fs sandbox root — so an UNCONFINED viewer (no root) was never
// exercised. It defaulted its root to "/", and wfs.New("/") is a degenerate
// sandbox (Confine tests hasPrefix(path, "//")) that rejects every real path →
// "No images" for any absolute dir. This guards the unconfined path.
const UNCONFINED_DIR = '/tmp/wash-iv-unconfined-e2e';

test.describe('imageview unconfined (no sandbox root)', () => {
  test.use({ routerOpts: { apps: ['session', 'about', 'imageview'], extraEnv: { WASH_IMAGEVIEW_DIR: UNCONFINED_DIR } } });

  test.beforeAll(() => {
    mkdirSync(UNCONFINED_DIR, { recursive: true });
    writeFileSync(join(UNCONFINED_DIR, 'unconfined.png'), PNG);
  });

  test('lists + loads images from an absolute dir when the router is unconfined', async ({ page, router }) => {
    await openViewer(page, router); // auto-scans WASH_IMAGEVIEW_DIR on mount
    // The thumbnail (and its blob src) appearing proves Confine accepted the
    // absolute path — i.e. the viewer is genuinely unconfined, not pinned to "/".
    const thumb = page.locator('wash-app-imageview [data-testid="iv-thumb-unconfined.png"]');
    await expect(thumb).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('wash-app-imageview [data-testid="iv-image"]')).toBeVisible({ timeout: 10_000 });
  });
});
