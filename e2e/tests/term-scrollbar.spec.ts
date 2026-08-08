// Terminal scrollbar geometry (docs/PTY_ROBUST.md — the scrollbar section
// of web/lib/src/scrollbars.ts explains the why).
//
// The bug this pins: xterm sized its character grid to the FULL viewport
// width and `.xterm-screen` — a positioned LATER sibling of
// `.xterm-viewport` — painted over the scrollbar. Two causes, both
// measured: the platform's overlay scrollbars reserve no width at all, and
// the 10px side inset lived on the host div where FitAddon's column
// arithmetic (host width MINUS the .xterm element's padding) could not see
// it, so the grid came out ~20px too wide.
//
// The invariant, asserted rather than described: the rendered screen never
// extends past the viewport's content box.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

interface Geom {
  cols: number;
  scrollbarSpace: number;
  screenRight: number;
  contentRight: number;
  scrollable: boolean;
}

async function geometry(page: Page): Promise<Geom> {
  return await page.locator('[data-testid="term-host"]').first().evaluate((host: any) => {
    const term = host.__washTerm;
    const el: HTMLElement = term.element;
    const vp = el.querySelector('.xterm-viewport') as HTMLElement;
    const screen = el.querySelector('.xterm-screen') as HTMLElement;
    return {
      cols: term.cols,
      scrollbarSpace: vp.offsetWidth - vp.clientWidth,
      screenRight: screen.getBoundingClientRect().right,
      contentRight: vp.getBoundingClientRect().left + vp.clientWidth,
      scrollable: vp.scrollHeight > vp.clientHeight,
    };
  });
}

async function openTerminal(page: Page, url: string) {
  await page.goto(url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
  await expect(page.locator('wash-app-term')).toBeVisible();
  await expect(page.locator('wash-app-term')).toContainText(/\$|#|>/, { timeout: 10_000 });
}

test.describe('terminal scrollbar', () => {
  test('content never extends under the scrollbar', async ({ page, router }) => {
    test.setTimeout(45_000);
    await openTerminal(page, router.url);

    // Empty terminal: the gutter is reserved up front (scrollbar-gutter:
    // stable), so the grid is right before anything scrolls.
    let g = await geometry(page);
    expect(g.scrollbarSpace).toBeGreaterThanOrEqual(8);
    expect(g.screenRight).toBeLessThanOrEqual(g.contentRight + 0.5);

    // Fill past a screenful so the bar is genuinely live.
    await page.locator('[data-testid="term-host"]').first().click();
    await page.keyboard.type("seq -f 'ruler-%.0f' 1 400");
    await page.keyboard.press('Enter');
    await expect.poll(async () => (await geometry(page)).scrollable, { timeout: 10_000 }).toBe(true);

    g = await geometry(page);
    expect(g.scrollbarSpace).toBeGreaterThanOrEqual(8);
    expect(g.screenRight).toBeLessThanOrEqual(g.contentRight + 0.5);

    // …and after a resize, which is when the grid is recomputed.
    await page.setViewportSize({ width: 1100, height: 800 });
    await page.waitForTimeout(600);
    g = await geometry(page);
    expect(g.screenRight).toBeLessThanOrEqual(g.contentRight + 0.5);

    await page.setViewportSize({ width: 900, height: 700 });
    await page.waitForTimeout(600);
    g = await geometry(page);
    expect(g.screenRight).toBeLessThanOrEqual(g.contentRight + 0.5);
  });

  test('the tab bar keeps its full width — no gutter on a horizontal strip', async ({ page, router }) => {
    // scrollbar-gutter reserves on the inline axis, so applying it to the
    // tab strip would steal 10px for a vertical bar that never appears.
    await openTerminal(page, router.url);
    const bar = await page.locator('[data-testid="term-tabbar"]').evaluate((el: HTMLElement) => ({
      offsetW: el.offsetWidth,
      clientW: el.clientWidth,
    }));
    expect(bar.clientW).toBe(bar.offsetW);
  });
});
