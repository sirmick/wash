// Window layouts (docs/WM_LAYOUT.md): a viewport cell given a layout tiles
// its windows into panels, live, and only the head tab places them.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

const TITLEBAR = 28;
const BORDER = 1;
const GUTTER = 4;
const TASKBAR = 40;

async function launchTestApp(page: Page) {
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu-com.wash.test"]').click();
}

async function openTwo(page: Page) {
  await launchTestApp(page);
  await expect(page.locator('wash-app-test')).toHaveCount(1);
  await launchTestApp(page);
  await expect(page.locator('wash-app-test')).toHaveCount(2);
}

// The work area of the cell in view, as the shell computes it.
async function workArea(page: Page) {
  return page.evaluate(() => ({
    w: window.innerWidth - (parseInt(getComputedStyle(document.documentElement).getPropertyValue('--wash-reserved-right'), 10) || 0),
    h: window.innerHeight,
  }));
}

// Window geometry from the shell's store, sorted left to right.
async function geoms(page: Page) {
  return page.evaluate(() =>
    window.wash.windows()
      .filter((w) => w.element === 'wash-app-test')
      .map(({ x, y, w, h, state }) => ({ x, y, w, h, state }))
      .sort((a, b) => a.x - b.x),
  );
}

// halves is where an r2 layout puts two chromed windows.
function halves(area: { w: number; h: number }) {
  const inner = area.w - GUTTER;
  const leftW = Math.round(inner / 2);
  return [
    { x: 0, y: 0, w: leftW - 2 * BORDER, h: area.h - TASKBAR - TITLEBAR - 2 * BORDER },
    { x: leftW + GUTTER, y: 0, w: area.w - leftW - GUTTER - 2 * BORDER, h: area.h - TASKBAR - TITLEBAR - 2 * BORDER },
  ];
}

const pos = (g: { x: number; y: number; w: number; h: number }) => ({ x: g.x, y: g.y, w: g.w, h: g.h });

// rightClickDesktop right-clicks bare desktop in the cell in view: left of
// the sidebar, above the taskbar, below where new windows cascade.
async function rightClickDesktop(page: Page) {
  const { w, h } = await workArea(page);
  await page.mouse.click(w - 30, h - TASKBAR - 30, { button: 'right' });
}

// rightClickTaskbar right-clicks the empty end of the taskbar's window list.
async function rightClickTaskbar(page: Page) {
  const box = (await page.locator('[data-testid="taskbar-windows"]').boundingBox())!;
  await page.mouse.click(box.x + box.width - 10, box.y + box.height / 2, { button: 'right' });
}

test.describe('window layouts', () => {
  test.use({ routerOpts: { showHidden: true } });

  test('the desktop menu arranges a viewport, and the layout follows close and the sidebar', async ({ page, router }) => {
    await page.goto(router.url);
    await openTwo(page);

    await rightClickDesktop(page);
    const menu = page.locator('[data-testid="layout-menu"]');
    await expect(menu).toBeVisible();
    await menu.locator('[data-testid="layout-preset-r2"]').click();
    await expect(menu).toHaveCount(0);

    expect(await page.evaluate(() => window.wash.cellLayout(0, 0))).toBe('r2');
    await expect.poll(async () => (await geoms(page)).map(pos)).toEqual(halves(await workArea(page)));

    // The menu ticks the current layout. A tiled viewport has no bare
    // desktop left to click, so this time it comes from the taskbar.
    await rightClickTaskbar(page);
    await expect(menu.locator('[data-testid="layout-preset-r2"]')).toContainText('✓');
    await page.keyboard.press('Escape');

    // Hiding the sidebar widens the work area; the panels follow.
    await page.keyboard.press('Control+Alt+s');
    await expect(page.locator('[data-testid="sidebar"]')).toHaveAttribute('data-mode', 'hidden');
    await expect.poll(async () => (await geoms(page)).map(pos)).toEqual(halves(await workArea(page)));

    // Closing one window leaves its panel empty; the other stays put.
    const before = await geoms(page);
    await page.locator('.wash-window').first().getByRole('button', { name: 'Close window' }).click();
    await expect(page.locator('wash-app-test')).toHaveCount(1);
    const after = await geoms(page);
    expect(after.length).toBe(1);
    expect(before.map(pos)).toContainEqual(pos(after[0]));
  });

  test('Float gives a window its frame back; No layout floats the rest', async ({ page, router }) => {
    await page.goto(router.url);
    await openTwo(page);
    const floating = await geoms(page);
    const floatingById = new Map(
      (await page.evaluate(() => window.wash.windows().filter((w) => w.element === 'wash-app-test')))
        .map((w) => [w.windowID, { x: w.x, y: w.y, w: w.w, h: w.h }]),
    );

    await page.evaluate(() => window.wash.setCellLayout(0, 0, 'r2'));
    await expect.poll(async () => (await geoms(page)).map(pos)).toEqual(halves(await workArea(page)));

    // Float the left one from its titlebar menu: it gets back exactly the
    // frame it had before it was tiled.
    const leftKey = await page.evaluate(() =>
      window.wash.windows().filter((x) => x.element === 'wash-app-test').sort((a, b) => a.x - b.x)[0].windowID);
    const original = floatingById.get(leftKey)!;
    await page.evaluate((id) => window.wash.openWindowMenu(id, 200, 200), leftKey);
    await page.locator('[data-testid="window-ctx-float"]').click();
    await expect.poll(async () => {
      const w = (await page.evaluate(() => window.wash.windows())).find((x) => x.windowID === leftKey)!;
      return pos(w);
    }).toEqual(original);

    // Its menu now offers Tile instead.
    await page.evaluate((id) => window.wash.openWindowMenu(id, 200, 200), leftKey);
    await expect(page.locator('[data-testid="window-ctx-tile"]')).toBeEnabled();
    await page.keyboard.press('Escape');

    // No layout, from the taskbar's empty space: everything floats again at
    // its pre-tile size.
    await rightClickTaskbar(page);
    await page.locator('[data-testid="layout-none"]').click();
    await expect.poll(() => page.evaluate(() => window.wash.cellLayout(0, 0))).toBeNull();
    await expect.poll(async () => (await geoms(page)).map((g) => [g.w, g.h])).toEqual(floating.map((g) => [g.w, g.h]));
  });

  test('a pager cell sets the layout of that cell, and windows sent there tile', async ({ page, router }) => {
    await page.goto(router.url);
    await openTwo(page);

    await page.locator('[data-testid="pager-cell-1-0"]').click({ button: 'right' });
    await page.locator('[data-testid="layout-preset-r2"]').click();
    expect(await page.evaluate(() => window.wash.cellLayout(1, 0))).toBe('r2');
    expect(await page.evaluate(() => window.wash.cellLayout(0, 0))).toBeNull();

    // Send both windows to that viewport: they take its panels.
    const ids = await page.evaluate(() => window.wash.windows().filter((w) => w.element === 'wash-app-test').map((w) => w.windowID));
    for (const id of ids) {
      await page.evaluate((wid) => window.wash.openWindowMenu(wid, 200, 200), id);
      await page.locator('[data-testid="window-ctx-send-1-0"]').click();
    }
    const area = await workArea(page);
    const W = await page.evaluate(() => window.innerWidth);
    await expect.poll(async () => (await geoms(page)).map(pos))
      .toEqual(halves(area).map((g) => ({ ...g, x: g.x + W })));
  });

  test('only the head places: a layout set elsewhere is placed for the head\'s screen', async ({ page, router, context }) => {
    test.setTimeout(60_000);
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.goto(router.url);
    await openTwo(page);

    // A second, narrower tab takes the head.
    const second = await context.newPage();
    await second.setViewportSize({ width: 1100, height: 700 });
    await second.goto(router.url);
    await expect(second.locator('wash-app-test')).toHaveCount(2, { timeout: 15_000 });
    await expect(page.locator('[data-testid="wash-connection-superseded"]')).toBeVisible({ timeout: 10_000 });

    // The first tab (not the head) sets the layout. The head places it
    // from ITS screen, and the first tab only shows the result.
    await page.evaluate(() => window.wash.setCellLayout(0, 0, 'r2'));
    await expect.poll(async () => (await geoms(second)).map(pos)).toEqual(halves(await workArea(second)));
    await expect.poll(async () => (await geoms(page)).map(pos)).toEqual(halves(await workArea(second)));

    // Use here: the first tab is the head again and re-places for its own
    // screen.
    await page.locator('[data-testid="wash-connection-use-here"]').click();
    await expect.poll(async () => (await geoms(page)).map(pos)).toEqual(halves(await workArea(page)));
    await second.close();
  });
});
