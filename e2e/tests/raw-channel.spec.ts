// Raw channel round-trip: the test app opens a channel and the BE
// io.Copy(ch, ch)s its bytes. FE writes "hello"; BE reflects;
// FE reads back. This is the foundational test for the wire's
// dynamic / raw-channel surface — anything that uses raw channels
// (pty, AI streaming, file transfer) depends on this path working.

import { test, expect } from '../fixtures/router';

test.describe('raw channels (kiosk)', () => {
  test.use({ routerOpts: { kiosk: 'com.wash.test' } });

  test('open channel → write → echo back', async ({ page, router }) => {
    await page.goto(router.url);
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();

    await app.locator('[data-testid="action-open-echo"]').click();

    // The router-allocated channel id is ≥ 2; the FE just renders it.
    await expect(app.locator('[data-testid="echo-channel"]')).not.toHaveText('none');

    // BE io.Copy(ch, ch) reflects the "hello" the FE wrote on open.
    await expect(app.locator('[data-testid="echo-received"]')).toHaveText('5');
    await expect(app.locator('[data-testid="echo-text-received"]')).toHaveText('hello');
  });
});
