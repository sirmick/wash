// Open routing: double-clicking a file in fm asks the router to launch it
// in the app that declared its extension (manifest Opens), passing the path
// as `--open <path>` argv. Images → the image viewer; text/code → the
// editor. This exercises the full chain: fm CapOpen → open.request →
// router resolveOpen → spawn with argv → the target app's launch-open hook.

import { test, expect } from '../fixtures/router';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAKklEQVR4nGK+Y2NzTm7fPrlzaCSDjc2dOzY2mCQDVlEbmzsDqgMQAAD//+8yV15/i6x3AAAAAElFTkSuQmCC',
  'base64',
);

function seedOpenables(root: string): void {
  writeFileSync(join(root, 'photo.png'), PNG);
  writeFileSync(join(root, 'notes.md'), '# notes\n\nhello from the editor\n');
  // No app registers ".xyz" → double-click falls back to the preview pane.
  writeFileSync(join(root, 'mystery.xyz'), 'UNHANDLED-PREVIEW-BODY\n');
}

test.use({
  routerOpts: {
    apps: ['session', 'about', 'fm', 'edit', 'imageview'],
    fmRoot: true,
    fmSeed: seedOpenables,
  },
});

async function openFm(
  page: import('@playwright/test').Page,
  router: import('../fixtures/router').RouterHandle,
) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-photo.png"]')).toBeVisible();
}

test.describe('open routing', () => {
  test('double-clicking an image opens it in the image viewer', async ({ page, router }) => {
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-photo.png"]').dblclick();

    // A fresh imageview window appears and renders the opened image (its
    // <img> only mounts once the full bytes arrive over the channel).
    await expect(page.locator('wash-app-imageview')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('wash-app-imageview [data-testid="iv-image"]')).toBeVisible({ timeout: 15_000 });
    // The list found the sibling image too.
    await expect(page.locator('wash-app-imageview [data-testid="iv-thumb-photo.png"]')).toBeVisible();
  });

  test('double-clicking a markdown file opens it in the editor', async ({ page, router }) => {
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-notes.md"]').dblclick();

    // An editor window is not the claim — the claim is that the router
    // spawned it WITH `--open <path>` and the editor's launch hook opened
    // that file. A dropped argv still gives you a visible editor, so the
    // buffer contents are what actually tests the routing.
    const editor = page.locator('wash-app-edit');
    await expect(editor).toBeVisible({ timeout: 15_000 });
    await expect(editor.locator('.cm-content')).toContainText('hello from the editor', {
      timeout: 15_000,
    });
  });

  test('double-clicking a file with no registered handler falls back to preview', async ({ page, router }) => {
    await openFm(page, router);
    await expect(page.locator('[data-testid="fm-entry-mystery.xyz"]')).toBeVisible();

    await page.locator('[data-testid="fm-entry-mystery.xyz"]').dblclick();

    // It shows in the preview pane instead of launching anything…
    await expect(page.locator('wash-app-fm [data-testid="fm-preview"]')).toContainText('UNHANDLED-PREVIEW-BODY', { timeout: 5_000 });
    await expect(page.locator('wash-app-fm [data-testid="fm-status"]')).toContainText(/no app for/i);
    // …and no editor/viewer window was spawned for it.
    await expect(page.locator('wash-app-edit')).toHaveCount(0);
    await expect(page.locator('wash-app-imageview')).toHaveCount(0);
  });
});
