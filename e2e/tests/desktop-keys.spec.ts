// Desktop-background browser-key guard (docs/Review-findings.md P2).
//
// With nothing focused, Ctrl+W / Ctrl+N / Ctrl+T are the browser's, and in
// a desktop that lives in a tab that reads as "everything just vanished".
// The shell default-prevents them while no wash window has focus.
//
// The keys are dispatched synthetically on purpose: Chromium reserves
// Ctrl+W / Ctrl+T / Ctrl+N and never delivers them to the page at all, so a
// real page.keyboard.press would test the browser, not the guard. What the
// guard is worth is what this asserts — the page-visible half — plus the
// FE-side proof that nothing was closed.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

test.use({ routerOpts: { apps: ['session', 'about'] } });

// dispatch fires a trusted-shaped keydown at document.body and reports
// whether a listener called preventDefault on it.
function dispatch(page: Page, key: string, ctrl = true): Promise<boolean> {
  return page.evaluate(
    ({ key, ctrl }) => {
      const ev = new KeyboardEvent('keydown', { key, ctrlKey: ctrl, bubbles: true, cancelable: true });
      document.body.dispatchEvent(ev);
      return ev.defaultPrevented;
    },
    { key, ctrl },
  );
}

test.describe('desktop key guard', () => {
  test('Ctrl+W / Ctrl+N / Ctrl+T on the desktop are swallowed and close nothing', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    for (const key of ['w', 'n', 't']) {
      expect(await dispatch(page, key), `ctrl+${key} should be default-prevented`).toBe(true);
    }
    // Without Ctrl they are just typing, and the guard keeps its hands off.
    expect(await dispatch(page, 'w', false)).toBe(false);
    // F5 is deliberately left alone: reload is how you recover a wedged shell.
    expect(await dispatch(page, 'F5', false)).toBe(false);

    // Nothing closed, and the desktop is still here.
    await expect(page.locator('wash-app-session')).toBeVisible();
    expect(await page.evaluate(() => window.wash.windows().length)).toBe(0);
  });

  test('a focused window keeps its own Ctrl+W — the guard only covers the background', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /About wash/ }).click();
    await expect(page.locator('wash-app-about')).toBeVisible();
    await expect.poll(() => page.evaluate(() => window.wash.windows().some((w) => w.focused))).toBe(true);

    // A window has focus, so the shell does not intercept: whatever the app
    // binds Ctrl+W to is the app's business.
    expect(await dispatch(page, 'w')).toBe(false);
    await expect(page.locator('wash-app-about')).toBeVisible();
  });
});
