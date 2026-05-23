// Screenshot button: clicking the taskbar 📷 captures the viewport
// via html-to-image and POSTs the PNG to /screenshot, which the
// router writes under cfg.ScreenshotDir.

import { test, expect } from '../fixtures/router';
import { existsSync, readdirSync, statSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

test.describe('screenshot button', () => {
  test('clicking the button saves a PNG to the screenshot dir', async ({ page, router }) => {
    // chromium PDF/PNG capture + BE-side file write can take a
    // beat under load; the 5s default isn't enough for the 8s
    // "saved <name>" assertion below.
    test.setTimeout(15_000);
    await page.goto(router.url);

    // The session taskbar button is testid="screenshot-btn".
    const btn = page.locator('[data-testid="screenshot-btn"]');
    await expect(btn).toBeVisible();
    await btn.click();

    // Status span flips to "saved <name>"; pattern is the timestamped
    // filename the BE returns.
    const status = page.locator('[data-testid="screenshot-status"]');
    await expect(status).toHaveText(/^saved \d{4}-\d{2}-\d{2}T.+\.png$/, { timeout: 8_000 });

    // BE log line confirms the write — and we can match the same path
    // we'll then load from disk.
    const line = await router.waitForLog(/screenshot saved .+ \(\d+ bytes\)/);
    const match = /screenshot saved (\S+) \((\d+) bytes\)/.exec(line);
    expect(match).not.toBeNull();
    const savedPath = match![1];
    const savedBytes = parseInt(match![2], 10);

    // File exists, has the PNG magic, and matches the reported size.
    expect(existsSync(savedPath)).toBe(true);
    expect(savedPath.startsWith(router.screenshotDir)).toBe(true);
    const st = statSync(savedPath);
    expect(st.size).toBe(savedBytes);
    expect(st.size).toBeGreaterThan(100);
    const head = readFileSync(savedPath).subarray(0, 8);
    expect(Array.from(head)).toEqual([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);

    // And the screenshots dir got one file from this test.
    const entries = readdirSync(router.screenshotDir).filter((n) => n.endsWith('.png'));
    expect(entries.length).toBe(1);
    expect(join(router.screenshotDir, entries[0])).toBe(savedPath);
  });

  test('rejects non-PNG bodies', async ({ page, router }) => {
    await page.goto(router.url);
    const resp = await page.request.post(`${router.url}screenshot`, {
      data: 'not a png',
      headers: { 'content-type': 'application/octet-stream' },
    });
    expect(resp.status()).toBe(415);
  });
});
