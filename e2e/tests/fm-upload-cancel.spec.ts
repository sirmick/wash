// fm upload cancel: cancelling an in-flight upload from the wash-bulk
// sidebar widget. The committed fm-upload.spec couldn't cover this —
// a local upload finishes sub-second, so there's no window to click
// cancel. Here we THROTTLE the connection via CDP so the transfer
// stays in-flight, then assert the cancel propagates end-to-end:
//
//   sidebar cancel → session bulk_cancel → bulk cancel → fm
//   upload_cancel → abort + report status=cancelled
//
// Regression guard for the head-of-line bug: writeRaw queues into the
// single shell socket, so without FE backpressure the whole file lands
// in the send buffer at once and the cancel control frame can't reach
// the BE until the bytes drain (i.e. never, in practice). fm paces the
// stream against rawBufferedAmount so the cancel jumps ahead.
import { test, expect, seedSimpleTree } from '../fixtures/router';
import { existsSync, statSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

test('sidebar cancel aborts an in-flight upload', async ({ page, router }) => {
  // Throttle the upload so 8 MiB takes long enough to click cancel.
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Network.enable');
  await cdp.send('Network.emulateNetworkConditions', {
    offline: false,
    latency: 0,
    downloadThroughput: -1,
    uploadThroughput: 250_000, // ~2 Mbps
  });

  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();

  const big = Buffer.alloc(8 * 1024 * 1024, 7);
  await page
    .locator('[data-testid="fm-upload-input"]')
    .setInputFiles([{ name: 'huge.bin', mimeType: 'application/octet-stream', buffer: big }]);

  // Wait for the running job row, then cancel it from the sidebar.
  const row = page.locator('[data-testid^="bulk-job-up-"]').first();
  await expect(row).toBeVisible({ timeout: 8_000 });
  await expect(row).toHaveAttribute('data-status', 'running', { timeout: 8_000 });
  const jobId = (await row.getAttribute('data-testid'))!.replace('bulk-job-', '');
  await page.getByTestId(`bulk-cancel-${jobId}`).click();

  // BE: the upload job reaches a terminal cancelled state.
  await router.waitForLog(new RegExp(`bulk-ops job=${jobId} op=upload status=cancelled`), 10_000);
  // FE: the row reflects it.
  await expect(row).toHaveAttribute('data-status', 'cancelled', { timeout: 5_000 });
  // FS: the upload wasn't completed (temp discarded, no full file left).
  const dest = join(router.fmRoot, 'huge.bin');
  if (existsSync(dest)) {
    expect(statSync(dest).size).toBeLessThan(big.length);
  }
});
