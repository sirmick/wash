// Kiosk-mode tests: wash-test mounted as the root element, no chrome.
// Focused on FE↔BE round-trips and per-app event delivery.

import { test, expect } from '../fixtures/router';

test.describe('test app (kiosk)', () => {
  test.use({ routerOpts: { kiosk: 'com.wash.test' } });

  test('mounts and reports unfocused state', async ({ page, router }) => {
    await page.goto(router.url);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();
    await expect(app.locator('[data-testid="focused"] b')).toHaveText('no');
    // Instance id was assigned by the router (non-empty).
    await expect(app.locator('[data-testid="instance"]')).not.toHaveText('');
  });

  test('ping round-trips through BE', async ({ page, router }) => {
    await page.goto(router.url);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();
    await app.locator('[data-testid="action-ping"]').click();
    await expect(app.locator('[data-testid="counter-pings"]')).toHaveText('1');
    await app.locator('[data-testid="action-ping"]').click();
    await expect(app.locator('[data-testid="counter-pings"]')).toHaveText('2');
  });

  test('set title relays back from BE', async ({ page, router }) => {
    await page.goto(router.url);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();
    await app.locator('[data-testid="action-set-title"]').click();
    await expect(app.locator('[data-testid="title"] b')).toHaveText('test-1');
    await app.locator('[data-testid="action-set-title"]').click();
    await expect(app.locator('[data-testid="title"] b')).toHaveText('test-2');
  });

  test('keyboard reaches BE-relayed FE counter', async ({ page, router }) => {
    await page.goto(router.url);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();
    await app.click(); // focus the element
    await page.keyboard.press('a');
    await page.keyboard.press('b');
    await page.keyboard.press('c');
    await expect(app.locator('[data-testid="counter-keys"]')).toHaveText('3');
  });

  test('throw forwards to server log', async ({ page, router }) => {
    await page.goto(router.url);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();
    await app.locator('[data-testid="action-throw"]').click();
    const line = await router.waitForLog(/browser\/shell \[error\] .*deliberate uncaught error/);
    expect(line).toContain('deliberate uncaught error');
  });

  test('console.error forwards to server log', async ({ page, router }) => {
    await page.goto(router.url);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();
    await app.locator('[data-testid="action-console-error"]').click();
    const line = await router.waitForLog(/browser\/console \[error\] .*deliberate console\.error/);
    expect(line).toContain('deliberate console.error');
  });

  test('unhandled rejection forwards to server log', async ({ page, router }) => {
    await page.goto(router.url);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();
    await app.locator('[data-testid="action-reject-promise"]').click();
    const line = await router.waitForLog(/browser\/shell \[error\] .*unhandled rejection.*deliberate unhandled rejection/);
    expect(line).toContain('deliberate unhandled rejection');
  });
});
