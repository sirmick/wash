// Virtual-desktop viewport tests: 3x3 pan camera, pager widget,
// taskbar dblclick snap-to-viewport, Ctrl+Alt+Arrow keybinds, and
// auto-relocation of newly-spawned windows into the current cell.

import { test, expect } from '../fixtures/router';

test.describe('viewport', () => {
  test('pager renders 9 cells with (0,0) active by default', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const pager = page.locator('[data-testid="pager"]');
    await expect(pager).toBeVisible();
    // Nine cells.
    const cells = page.locator('[data-testid^="pager-cell-"]');
    await expect(cells).toHaveCount(9);
    // (0,0) is the active one: it has the accent border.
    const active = page.locator('[data-testid="pager-cell-0-0"]');
    const border = await active.evaluate((el) => getComputedStyle(el as HTMLElement).borderColor);
    // Active border is the #6a7adf accent — non-active cells use #2a2a4a.
    // Just assert it's NOT the muted color.
    expect(border).not.toBe('rgb(42, 42, 74)');
  });

  test('click pager cell pans the camera', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    // Open About so there's something to look at + a non-empty window list.
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /About wash/ }).click();
    await expect(page.locator('wash-app-about')).toBeVisible();

    // Jump to cell (1, 0).
    await page.locator('[data-testid="pager-cell-1-0"]').click();
    // Cam transform reflects the pan. The cam wraps the For-of windows
    // in main.tsx with transform: translate(-W, 0).
    const cam = page.locator('wash-app-session ~ div').first();
    await expect.poll(async () => {
      const t = await cam.evaluate((el) => getComputedStyle(el as HTMLElement).transform);
      return t;
    }, { timeout: 1500 }).not.toBe('none');
    const transform = await cam.evaluate((el) => getComputedStyle(el as HTMLElement).transform);
    // matrix(1,0,0,1, -W, 0) — extract tx (5th value).
    const m = /matrix\(([^)]+)\)/.exec(transform);
    expect(m).not.toBeNull();
    const parts = m![1].split(',').map((s) => parseFloat(s.trim()));
    const innerW = await page.evaluate(() => window.innerWidth);
    expect(parts[4]).toBeCloseTo(-innerW, 0);
    expect(parts[5]).toBeCloseTo(0, 0);
  });

  test('taskbar pill dblclick snaps to the window viewport', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    // Spawn About in cell (0,0).
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /About wash/ }).click();
    await expect(page.locator('wash-app-about')).toBeVisible();

    // Pan to (2,2).
    await page.locator('[data-testid="pager-cell-2-2"]').click();
    // Wait for the cam transition (220ms).
    await page.waitForTimeout(280);

    // About's titlebar appears in the taskbar as a pill. Dblclick it.
    const pill = page.locator('wash-app-session button:has-text("About")').first();
    await pill.dblclick();
    await page.waitForTimeout(280);

    // Active cell is back to (0,0).
    const active = page.locator('[data-testid="pager-cell-0-0"]');
    const border = await active.evaluate((el) => getComputedStyle(el as HTMLElement).borderColor);
    expect(border).not.toBe('rgb(42, 42, 74)');
  });

  test('Ctrl+Alt+Right pans one viewport', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.keyboard.press('Control+Alt+ArrowRight');
    await page.waitForTimeout(280);
    const active = page.locator('[data-testid="pager-cell-1-0"]');
    const border = await active.evaluate((el) => getComputedStyle(el as HTMLElement).borderColor);
    expect(border).not.toBe('rgb(42, 42, 74)');
  });

  test('new windows spawn in the current viewport (auto-relocate)', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    // Pan first.
    await page.locator('[data-testid="pager-cell-2-1"]').click();
    await page.waitForTimeout(260);
    // Now spawn About — its rect should land inside cell (2,1)'s
    // pager preview, not in (0,0).
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /About wash/ }).click();
    await expect(page.locator('wash-app-about')).toBeVisible();

    // The pager-window-* rect should appear inside cell (2,1).
    const inTargetCell = await page.evaluate(() => {
      const cell = document.querySelector('[data-testid="pager-cell-2-1"]');
      if (!cell) return false;
      return !!cell.querySelector('[data-testid^="pager-window-"]');
    });
    expect(inTargetCell).toBe(true);

    // And NOT in (0,0).
    const inOrigin = await page.evaluate(() => {
      const cell = document.querySelector('[data-testid="pager-cell-0-0"]');
      if (!cell) return false;
      return !!cell.querySelector('[data-testid^="pager-window-"]');
    });
    expect(inOrigin).toBe(false);
  });
});
