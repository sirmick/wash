// An OS file dropped where nothing accepts it (the wallpaper, a window's
// chrome) must not take the browser's default action — navigating the tab
// to file://…, which tears the desktop down. The shell guards dragover +
// drop at the window level for `Files` drags no app claimed.
//
// A synthetic drop never navigates a real browser, so the FE half is the
// events' return value: dispatchEvent returns false only when a handler
// called preventDefault. The BE half is the router log line the shell
// writes for every swallowed drop.

import { test, expect } from '../fixtures/router';
import { seedSimpleTree } from '../fixtures/router';
import type { Page } from '@playwright/test';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

// dropOsFile fires dragover + drop carrying one File at `selector` and
// reports whether each event's default was prevented.
async function dropOsFile(page: Page, selector: string): Promise<{ dragover: boolean; drop: boolean }> {
  return page.evaluate((sel) => {
    const el = document.querySelector(sel);
    if (!el) throw new Error(`no element for ${sel}`);
    const dt = new DataTransfer();
    dt.items.add(new File(['bytes'], 'dropped.txt', { type: 'text/plain' }));
    const fire = (type: string) =>
      !el.dispatchEvent(new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: dt }));
    return { dragover: fire('dragover'), drop: fire('drop') };
  }, selector);
}

test.describe('shell: OS file drop guard', () => {
  test('a file dropped on the wallpaper is swallowed; the desktop stays up', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('[data-testid="desktop-wallpaper"]')).toBeVisible();
    const before = page.url();

    const prevented = await dropOsFile(page, '[data-testid="desktop-wallpaper"]');
    expect(prevented).toEqual({ dragover: true, drop: true });

    await router.waitForLog(/browser\/shell \[info\] swallowed an OS file drop outside any drop target files=1/, 5_000);
    expect(page.url()).toBe(before);
    await expect(page.locator('.wash-desktop')).toBeAttached();
    await expect(page.locator('[data-testid="desktop-wallpaper"]')).toBeVisible();
  });

  test('a file dropped on a window titlebar is swallowed too', async ({ page, router }) => {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /Files/ }).click();
    await expect(page.locator('wash-app-fm')).toBeVisible();

    const prevented = await dropOsFile(page, '.wash-window .wash-titlebar');
    expect(prevented).toEqual({ dragover: true, drop: true });
    await router.waitForLog(/swallowed an OS file drop outside any drop target/, 5_000);
    await expect(page.locator('wash-app-fm')).toBeVisible();
  });

  test('an internal wash drag is not touched by the guard', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('[data-testid="desktop-wallpaper"]')).toBeVisible();
    const prevented = await page.evaluate(() => {
      const el = document.querySelector('[data-testid="desktop-wallpaper"]')!;
      const dt = new DataTransfer();
      dt.setData('application/x-wash-paths', JSON.stringify(['/x']));
      const fire = (type: string) =>
        !el.dispatchEvent(new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: dt }));
      return { dragover: fire('dragover'), drop: fire('drop') };
    });
    expect(prevented).toEqual({ dragover: false, drop: false });
  });
});
