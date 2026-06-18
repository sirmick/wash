// Virtual-desktop viewport tests: 3x3 pan camera, pager widget,
// taskbar dblclick snap-to-viewport, Ctrl+Alt+Arrow keybinds, and
// auto-relocation of newly-spawned windows into the current cell.

import { test, expect } from '../fixtures/router';

test.describe('viewport', () => {
  // Race-prone under parallel workers + tight default timeout:
  // BE round-trips for list/clipboard sync can exceed the 10s
  // playwright.config default under concurrent load. 20s gives
  // the same headroom the pre-5s-default 30s did.
  test.setTimeout(20_000);

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
    // in main.tsx with transform: translate(-W, 0). Solid sets the
    // initial transform to translate(0px, 0px) — so getComputedStyle
    // never reports 'none'. A 260ms CSS transition then interpolates
    // toward the new value; poll on the tx component reaching the
    // target instead of just "anything but none".
    const cam = page.locator('[data-testid="wash-cam"]');
    const innerW = await page.evaluate(() => window.innerWidth);
    await expect.poll(async () => {
      const t = await cam.evaluate((el) => getComputedStyle(el as HTMLElement).transform);
      const m = /matrix\(([^)]+)\)/.exec(t);
      if (!m) return null;
      const parts = m[1].split(',').map((s) => parseFloat(s.trim()));
      return parts[4];
    }, { timeout: 4_000 }).toBeCloseTo(-innerW, 0);
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
    // (Scoped by testid: "About" also names a sidebar section, so a bare
    // button:has-text("About") could match that section's header instead.)
    const pill = page.locator('[data-testid="taskbar-pill"]').filter({ hasText: 'About' }).first();
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
