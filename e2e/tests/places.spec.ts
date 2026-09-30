// Places (docs/PLACES.md): the Agent, Files, Editor and Terminal windows each
// carry icons for the other three. A first click opens that app in this
// window's folder and binds the two into a GROUP; later clicks bring the
// group's window back — wherever it is on the desktop — instead of opening
// another. A group's windows share a tint so they read as belonging together.
//
// The group protocol itself is tested exhaustively in internal/places (as
// windows exchanging messages). This spec is the end-to-end half: that the
// icons, the tint, the camera and the close path behave in a real desktop.

import { test, expect } from '../fixtures/router';
import type { Locator, Page } from '@playwright/test';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

test.use({
  routerOpts: {
    apps: ['session', 'fm', 'term'],
    fmRoot: true,
    fmSeed: (root: string) => writeFileSync(join(root, 'notes.txt'), 'hello\n'),
  },
});

async function openFiles(page: Page, url: string): Promise<Locator> {
  await page.goto(url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  const fm = page.locator('wash-app-fm');
  await expect(fm).toBeVisible();
  await expect(fm.getByTestId('fm-entry-notes.txt')).toBeVisible();
  return fm;
}

// The shell's window id for the window hosting an app element.
async function windowIdOf(page: Page, element: string): Promise<number> {
  return page.evaluate((el) => {
    const w = window.wash.windows().find((x) => x.element === el);
    if (!w) throw new Error(`no ${el} window`);
    return w.windowID;
  }, element);
}

test.describe('places', () => {
  test.setTimeout(60_000);

  test('a bound pair shares a tint, and the bond survives only while both live', async ({ page, router }) => {
    const fm = await openFiles(page, router.url);
    const bar = fm.getByTestId('places-bar');
    // Alone: no group, so no tint and nothing bound.
    await expect(bar).not.toHaveAttribute('data-group', /.+/);

    await fm.getByTestId('places-term').click();
    const term = page.locator('wash-app-term');
    await expect(term).toHaveCount(1, { timeout: 20_000 });
    await expect(fm.getByTestId('places-term')).toHaveAttribute('data-bound', 'true');

    // Same group from both ends — the same id, so the same derived tint.
    const group = await bar.getAttribute('data-group');
    expect(group).toMatch(/^[0-9a-f]{16}$/);
    await expect(term.getByTestId('places-bar')).toHaveAttribute('data-group', group!);
    const tintOf = (l: Locator) => l.evaluate((el) => getComputedStyle(el).backgroundColor);
    const fmTint = await tintOf(bar);
    expect(fmTint).not.toBe('rgba(0, 0, 0, 0)');
    expect(await tintOf(term.getByTestId('places-bar'))).toBe(fmTint);

    // Close the terminal: Files forgets it, loses the tint (it is alone
    // again), and the next click opens a fresh one rather than talking to
    // nobody.
    const termWin = await windowIdOf(page, 'wash-app-term');
    await page.evaluate((id) => window.wash.closeWindow(id), termWin);
    // A live shell asks first (term-close-confirm.spec.ts); say yes. A shell
    // that has not started yet closes without asking, so the confirm is
    // optional — the bound flag above does not wait for the shell.
    const ok = page.locator('[data-testid="term-close-confirm-ok"]');
    try {
      await ok.waitFor({ state: 'visible', timeout: 1_500 });
      await ok.click();
    } catch {
      // no confirm: the window closed on the request
    }
    await expect(term).toHaveCount(0, { timeout: 20_000 });
    await expect(fm.getByTestId('places-term')).toHaveAttribute('data-bound', 'false');
    await expect(bar).not.toHaveAttribute('data-group', /.+/);

    await fm.getByTestId('places-term').click();
    await expect(page.locator('wash-app-term')).toHaveCount(1, { timeout: 20_000 });
    await expect(fm.getByTestId('places-term')).toHaveAttribute('data-bound', 'true');
  });

  // The promise is "wherever it is on the desktop". A raise alone is
  // invisible when the window sits in another viewport cell — the router
  // sends window.reveal for a raise the user did not click, and the shell
  // brings the camera to it.
  test('bringing a bound window back pans the camera to wherever it is', async ({ page, router }) => {
    const fm = await openFiles(page, router.url);
    await fm.getByTestId('places-term').click();
    await expect(page.locator('wash-app-term')).toHaveCount(1, { timeout: 20_000 });
    await expect(page.locator('[data-testid="pager-cell-0-0"]')).toHaveAttribute('data-active', 'true');

    // Move the terminal two cells over, out of sight, while we stay on (0,0).
    const termWin = await windowIdOf(page, 'wash-app-term');
    const screen = await page.evaluate(() => ({ w: window.innerWidth, h: window.innerHeight }));
    await page.evaluate(
      ([id, x, y]) => window.wash.moveWindow(id, x, y),
      [termWin, screen.w * 2 + 40, screen.h + 40] as const,
    );
    await expect
      .poll(() => page.evaluate((id) => window.wash.windows().find((w) => w.windowID === id)?.x, termWin))
      .toBe(screen.w * 2 + 40);
    await expect(page.locator('[data-testid="pager-cell-0-0"]')).toHaveAttribute('data-active', 'true');

    // From Files, bring the terminal back: the camera goes to cell (2,1).
    await fm.getByTestId('places-term').click();
    await expect(page.locator('[data-testid="pager-cell-2-1"]')).toHaveAttribute('data-active', 'true', { timeout: 10_000 });
    // And it is still the one terminal.
    await expect(page.locator('wash-app-term')).toHaveCount(1);
  });

  test('a window already on screen does not move the camera', async ({ page, router }) => {
    const fm = await openFiles(page, router.url);
    await fm.getByTestId('places-term').click();
    await expect(page.locator('wash-app-term')).toHaveCount(1, { timeout: 20_000 });
    // Both on (0,0). Bringing the terminal forward must not pan anywhere.
    // Each check waits for the raise to have LANDED (the target focused) —
    // the reveal goes out with it, so a wrong pan would already be visible;
    // asserting the cell straight after the click would pass vacuously.
    const term = page.locator('wash-app-term');
    const focusedIs = (id: number) =>
      expect.poll(() => page.evaluate(() => window.wash.windows().find((w) => w.focused)?.windowID)).toBe(id);
    const fmWin = await windowIdOf(page, 'wash-app-fm');
    const termWin = await windowIdOf(page, 'wash-app-term');
    await term.getByTestId('places-fm').click();
    await focusedIs(fmWin);
    await expect(page.locator('[data-testid="pager-cell-0-0"]')).toHaveAttribute('data-active', 'true');
    await fm.getByTestId('places-term').click();
    await focusedIs(termWin);
    await expect(page.locator('[data-testid="pager-cell-0-0"]')).toHaveAttribute('data-active', 'true');
  });
});
