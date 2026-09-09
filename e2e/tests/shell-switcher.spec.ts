// Window switcher (Ctrl+Alt+Tab), show desktop (Ctrl+Alt+D) and taskbar
// middle-click close — docs/Review-findings.md P2 cross-app ("no Alt+Tab").
//
// Both halves per test: the FE (the overlay, which row is highlighted, which
// window ends up focused, the taskbar) and the BE (the router's `focus:` line
// for the window the shell switched to, and the close handshake actually
// removing the window from the session).

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

test.use({ routerOpts: { apps: ['session', 'about', 'fm'], fmRoot: true } });

interface WinRow {
  windowID: number;
  element: string;
  title: string;
  focused: boolean;
  state: string;
}

function wins(page: Page): Promise<WinRow[]> {
  return page.evaluate(
    () =>
      window.wash.windows().map((w) => ({
        windowID: w.windowID,
        element: w.element,
        title: w.title,
        focused: w.focused,
        state: w.state,
      })) as WinRow[],
  );
}

async function focusedElement(page: Page): Promise<string> {
  return (await wins(page)).find((w) => w.focused)?.element ?? '';
}

// Open About then Files, so the MRU order is [fm, about]: fm was focused
// last, About is "the previous window" a single Alt+Tab should return to.
async function openTwo(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /About wash/ }).click();
  await expect(page.locator('wash-app-about')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect.poll(() => focusedElement(page)).toBe('wash-app-fm');
}

test.describe('window switcher', () => {
  test.setTimeout(30_000);

  test('Ctrl+Alt+Tab shows the MRU overlay; releasing focuses the highlighted window', async ({ page, router }) => {
    await openTwo(page, router.url);

    const from = router.logCursor();
    await page.keyboard.down('Control');
    await page.keyboard.down('Alt');
    await page.keyboard.press('Tab');

    // The overlay lists both windows most-recent-first, and starts on the
    // PREVIOUS one so a single tap is "go back".
    const overlay = page.locator('[data-testid="window-switcher"]');
    await expect(overlay).toBeVisible();
    const rows = overlay.locator('[data-testid="window-switcher-item"]');
    await expect(rows).toHaveCount(2);
    await expect(rows.nth(0)).toContainText(/files/i);
    await expect(rows.nth(1)).toContainText(/about/i);
    await expect(rows.nth(1)).toHaveAttribute('data-selected', 'true');

    // A second Tab wraps back onto the current window…
    await page.keyboard.press('Tab');
    await expect(rows.nth(0)).toHaveAttribute('data-selected', 'true');
    // …and Shift+Tab steps back to the previous one.
    await page.keyboard.down('Shift');
    await page.keyboard.press('Tab');
    await page.keyboard.up('Shift');
    await expect(rows.nth(1)).toHaveAttribute('data-selected', 'true');

    // Releasing the chord commits: About takes focus and the overlay goes.
    await page.keyboard.up('Alt');
    await page.keyboard.up('Control');
    await expect(overlay).toHaveCount(0);
    await expect.poll(() => focusedElement(page)).toBe('wash-app-about');

    // BE half: the router saw the focus change the shell asked for.
    const about = (await wins(page)).find((w) => w.element === 'wash-app-about')!;
    await router.waitForLog(new RegExp(`focus: win=${about.windowID} app=com\\.wash\\.about`), 10_000, from);

    // And a second single tap goes back to where we came from — the commit
    // re-ordered the MRU list, so the switcher is a real toggle.
    await page.keyboard.down('Control');
    await page.keyboard.down('Alt');
    await page.keyboard.press('Tab');
    await page.keyboard.up('Alt');
    await page.keyboard.up('Control');
    await expect.poll(() => focusedElement(page)).toBe('wash-app-fm');
  });

  test('Ctrl+Alt+D minimises everything, and again restores it', async ({ page, router }) => {
    await openTwo(page, router.url);

    // window.wash.windows() mirrors the SERVER-authoritative store: a
    // window only reads back `minimized` once the router has applied the
    // state change and echoed the patch, so these polls are the BE half.
    await page.keyboard.press('Control+Alt+d');
    await expect.poll(async () => (await wins(page)).filter((w) => w.state !== 'minimized').length).toBe(0);

    await page.keyboard.press('Control+Alt+d');
    await expect.poll(async () => (await wins(page)).filter((w) => w.state === 'minimized').length).toBe(0);
    await expect(page.locator('wash-app-fm')).toBeVisible();
    await expect(page.locator('wash-app-about')).toBeVisible();
  });

  test('taskbar middle-click closes the window through the close handshake', async ({ page, router }) => {
    await openTwo(page, router.url);
    const pills = page.locator('[data-testid="taskbar-pill"]');
    await expect(pills).toHaveCount(2);

    const about = (await wins(page)).find((w) => w.element === 'wash-app-about')!;
    await pills.filter({ hasText: /about/i }).click({ button: 'middle' });

    await expect(page.locator('wash-app-about')).toHaveCount(0);
    await expect(pills).toHaveCount(1);
    // BE half: the window is gone from the server-authoritative window list
    // (the router ran the close handshake and broadcast the removal), not
    // merely hidden in the FE.
    await expect.poll(async () => (await wins(page)).some((w) => w.windowID === about.windowID)).toBe(false);
  });
});
