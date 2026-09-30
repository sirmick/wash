// Virtual-desktop viewport tests: 3x3 pan camera, pager widget,
// taskbar dblclick snap-to-viewport, Ctrl+Alt+Arrow keybinds, and
// auto-relocation of newly-spawned windows into the current cell.
//
// Active-cell assertions key off the stable data-active attribute on the
// pager cell (flipped synchronously by setViewport, independent of the cam
// CSS transition) — Playwright auto-retries toHaveAttribute, so there are no
// fixed sleeps and no theme-coupled rgb() color asserts to go stale.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

// The set of pager cells currently marked active (expected: exactly one).
function activeCells(page: Page) {
  return page.locator('[data-testid^="pager-cell-"][data-active="true"]');
}

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
    await expect(page.locator('[data-testid^="pager-cell-"]')).toHaveCount(9);
    // (0,0) is the active one; nothing else is.
    await expect(page.locator('[data-testid="pager-cell-0-0"]')).toHaveAttribute('data-active', 'true');
    await expect(activeCells(page)).toHaveCount(1);
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
    await expect(page.locator('[data-testid="pager-cell-1-0"]')).toHaveAttribute('data-active', 'true');

    // Cam transform reflects the pan. The cam wraps the For-of windows
    // in main.tsx with transform: translate(-W, 0). Solid sets the
    // initial transform to translate(0px, 0px) — so getComputedStyle
    // never reports 'none'. A CSS transition then interpolates toward the
    // new value; poll on the tx component reaching the target.
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

    // Pan to (2,2); wait for the active cell to settle there.
    await page.locator('[data-testid="pager-cell-2-2"]').click();
    await expect(page.locator('[data-testid="pager-cell-2-2"]')).toHaveAttribute('data-active', 'true');

    // About's titlebar appears in the taskbar as a pill. Dblclick it.
    // (Scoped by testid: "About" also names a sidebar section, so a bare
    // button:has-text("About") could match that section's header instead.)
    const pill = page.locator('[data-testid="taskbar-pill"]').filter({ hasText: 'About' }).first();
    await pill.dblclick();

    // Active cell snaps back to (0,0) where About lives.
    await expect(page.locator('[data-testid="pager-cell-0-0"]')).toHaveAttribute('data-active', 'true');
  });

  test('Ctrl+Alt+Right pans one viewport', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.keyboard.press('Control+Alt+ArrowRight');
    await expect(page.locator('[data-testid="pager-cell-1-0"]')).toHaveAttribute('data-active', 'true');
  });

  test('new windows spawn in the current viewport (auto-relocate)', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    // Pan first; wait for the active cell to settle.
    await page.locator('[data-testid="pager-cell-2-1"]').click();
    await expect(page.locator('[data-testid="pager-cell-2-1"]')).toHaveAttribute('data-active', 'true');
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

  // Regression: a reload while parked on a non-(0,0) cell used to drag every
  // restored window one cell further out. seenWindowIDs starts empty on a
  // fresh RouterClient and was never seeded from the snapshot, so the first
  // upsert for each window — FloatingWindow.onMount calls focusWindow, and
  // the router's focus() always emits one — looked like a fresh spawn and
  // got the new-window viewport offset, which was then persisted as a
  // window.move. Repeat reloads walked windows off the plane entirely.
  test('reload on a non-zero viewport leaves windows where they are', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.locator('[data-testid="pager-cell-1-1"]').click();
    await expect(page.locator('[data-testid="pager-cell-1-1"]')).toHaveAttribute('data-active', 'true');
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /About wash/ }).click();
    await expect(page.locator('wash-app-about')).toBeVisible();

    // Router-side truth, read through the shell's window registry so the
    // assert survives any FE-only bookkeeping.
    const before = await page.evaluate(() => {
      const w = window.wash.windows().find((x) => x.element === 'wash-app-about');
      return w ? { x: w.x, y: w.y } : null;
    });
    expect(before).not.toBeNull();

    // Two reloads: one shift would be a bug, two proved it compounded.
    for (let i = 0; i < 2; i++) {
      await page.reload();
      await expect(page.locator('wash-app-about')).toBeVisible();
      // The cell is restored from localStorage, so we are still off-origin.
      await expect(page.locator('[data-testid="pager-cell-1-1"]')).toHaveAttribute('data-active', 'true');
    }

    const after = await page.evaluate(() => {
      const w = window.wash.windows().find((x) => x.element === 'wash-app-about');
      return w ? { x: w.x, y: w.y } : null;
    });
    expect(after).toEqual(before);

    // And it is still where the user left it, not stranded off-plane.
    const stillInCell = await page.evaluate(() => {
      const cell = document.querySelector('[data-testid="pager-cell-1-1"]');
      return !!cell?.querySelector('[data-testid^="pager-window-"]');
    });
    expect(stillInCell).toBe(true);
  });
});
