// imageview interactions: prev/next (buttons + arrow keys + wrap-around),
// zoom (buttons + keyboard + fit/reset), and drag-pan. The viewer's core UX
// had no e2e coverage. Runs unconfined with a fixed WASH_IMAGEVIEW_DIR so the
// app auto-scans a folder of several images on launch.

import { test, expect } from '../fixtures/router';
import { mkdirSync, writeFileSync, mkdtempSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAKklEQVR4nGK+Y2NzTm7fPrlzaCSDjc2dOzY2mCQDVlEbmzsDqgMQAAD//+8yV15/i6x3AAAAAElFTkSuQmCC',
  'base64',
);
// A FRESH directory per run. The assertions below count files
// ('a.png · 1/3'), so a stray leftover from a crashed run — or a
// parallel worker sharing one fixed path — turns 1/3 into 1/4 and
// fails the whole file. mkdtemp is what the other sixteen specs use.
const DIR = mkdtempSync(join(tmpdir(), 'wash-iv-'));

test.use({ routerOpts: { apps: ['session', 'about', 'imageview'], extraEnv: { WASH_IMAGEVIEW_DIR: DIR } } });

test.beforeAll(() => {
  mkdirSync(DIR, { recursive: true });
  for (const n of ['a.png', 'b.png', 'c.png']) writeFileSync(join(DIR, n), PNG);
});

async function openViewer(
  page: import('@playwright/test').Page,
  router: import('../fixtures/router').RouterHandle,
) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Image Viewer$/ }).click();
  await expect(page.locator('wash-app-imageview [data-testid="iv-image"]')).toBeVisible({ timeout: 15_000 });
}

const caption = (page: import('@playwright/test').Page) =>
  page.locator('wash-app-imageview [data-testid="iv-caption"]').first();
const zoomPct = async (page: import('@playwright/test').Page): Promise<number> =>
  Number((await caption(page).textContent())!.match(/(\d+)%/)![1]);

test.describe('imageview interactions', () => {
  test('next/prev buttons step through with wrap-around', async ({ page, router }) => {
    await openViewer(page, router);
    const iv = page.locator('wash-app-imageview').first();
    await expect(caption(page)).toContainText('a.png · 1/3');
    await iv.locator('[data-testid="iv-next"]').click({ force: true });
    await expect(caption(page)).toContainText('b.png · 2/3');
    await iv.locator('[data-testid="iv-next"]').click({ force: true });
    await expect(caption(page)).toContainText('c.png · 3/3');
    await iv.locator('[data-testid="iv-next"]').click({ force: true }); // wrap → first
    await expect(caption(page)).toContainText('a.png · 1/3');
    await iv.locator('[data-testid="iv-prev"]').click({ force: true }); // wrap → last
    await expect(caption(page)).toContainText('c.png · 3/3');
  });

  test('arrow keys navigate', async ({ page, router }) => {
    await openViewer(page, router);
    await page.locator('wash-app-imageview [data-testid="iv-main"]').click({ force: true, position: { x: 300, y: 150 } });
    await page.keyboard.press('ArrowRight');
    await expect(caption(page)).toContainText('b.png · 2/3');
    await page.keyboard.press('ArrowLeft');
    await expect(caption(page)).toContainText('a.png · 1/3');
  });

  test('zoom in/out, keyboard, and fit/reset', async ({ page, router }) => {
    await openViewer(page, router);
    const iv = page.locator('wash-app-imageview').first();
    expect(await zoomPct(page)).toBe(100);
    await iv.locator('[data-testid="iv-zoom-in"]').click({ force: true });
    expect(await zoomPct(page)).toBeGreaterThan(100);
    await iv.locator('[data-testid="iv-fit"]').click({ force: true });
    expect(await zoomPct(page)).toBe(100);
    // keyboard: '+' zooms in, '0' resets.
    await page.locator('wash-app-imageview [data-testid="iv-main"]').click({ force: true, position: { x: 300, y: 150 } });
    await page.keyboard.press('+');
    expect(await zoomPct(page)).toBeGreaterThan(100);
    await page.keyboard.press('0');
    expect(await zoomPct(page)).toBe(100);
  });

  test('navigating resets zoom for the new image', async ({ page, router }) => {
    await openViewer(page, router);
    const iv = page.locator('wash-app-imageview').first();
    await iv.locator('[data-testid="iv-zoom-in"]').click({ force: true });
    await iv.locator('[data-testid="iv-zoom-in"]').click({ force: true });
    expect(await zoomPct(page)).toBeGreaterThan(100);
    await iv.locator('[data-testid="iv-next"]').click({ force: true });
    await expect(caption(page)).toContainText('b.png · 2/3');
    expect(await zoomPct(page)).toBe(100); // resetView on select
  });

  test('drag pans the image', async ({ page, router }) => {
    await openViewer(page, router);
    const img = page.locator('wash-app-imageview [data-testid="iv-image"]').first();
    const before = await img.getAttribute('style');
    const box = await page.locator('wash-app-imageview [data-testid="iv-main"]').first().boundingBox();
    // drag in the upper area to avoid the bottom toolbar
    await page.mouse.move(box!.x + box!.width / 2, box!.y + 120);
    await page.mouse.down();
    await page.mouse.move(box!.x + box!.width / 2 + 60, box!.y + 120 + 40, { steps: 5 });
    await page.mouse.up();
    const after = await img.getAttribute('style');
    expect(after).not.toEqual(before);
    expect(after).toMatch(/translate\(\s*[1-9]/); // non-zero translate applied
  });
});
