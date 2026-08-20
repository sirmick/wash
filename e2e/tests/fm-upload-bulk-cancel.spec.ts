// Bulk-upload responsiveness + cancel. The single-file cancel
// (fm-upload-cancel.spec) passes because one big file fills the send
// buffer, so awaitUploadDrain blocks and yields a macrotask that lets the
// cancel through. A bulk upload of many SMALL files never fills the
// buffer; the producer loop then leans on Blob.arrayBuffer() to yield —
// which it does only sporadically, leaving long main-thread stalls (~300ms
// for ~47 MiB here, scaling with the transfer). During a stall the WS
// message pump is starved (the sidebar cancel isn't seen) and the UI is
// frozen (the cancel button can't be clicked). The producer loop now hands
// the event loop a macrotask every UPLOAD_YIELD_MS, capping the stall.
//
// We assert this functionally: the real sidebar cancel must reach the BE
// and stop the job mid-stream. (The raw stall was measured directly during
// development — ~300 ms without the yield vs ~18 ms with it for this 47 MiB
// job — but a heartbeat threshold proved too flaky in CI: it also catches
// the post-upload re-render of 1200 rows, so it isn't committed.)

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { readdirSync } from 'node:fs';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
}

// 1200 × 40 KiB ≈ 47 MiB — under Playwright's 50 MB setInputFiles cap, big
// enough to stay in flight while we measure/cancel, small enough per file
// that the send buffer never fills (the case awaitUploadDrain misses).
const N = 1200;
function manyFiles() {
  return Array.from({ length: N }, (_, i) => ({
    name: `f-${String(i).padStart(4, '0')}.bin`,
    mimeType: 'application/octet-stream',
    buffer: Buffer.alloc(40 * 1024, i & 0xff),
  }));
}

test('a bulk upload of many small files can be cancelled mid-stream', async ({ page, router }) => {
  await openFm(page, router);
  await page.locator('[data-testid="fm-upload-input"]').setInputFiles(manyFiles());

  const row = page.locator('[data-testid^="bulk-job-up-"]').first();
  await expect(row).toBeVisible({ timeout: 8_000 });
  const jobId = (await row.getAttribute('data-testid'))!.replace('bulk-job-', '');

  // The widget re-renders on every progress report, detaching the button —
  // dispatchEvent fires the click without waiting for a stable frame. The
  // handler runs only because the thread is responsive between yields.
  await expect
    .poll(
      async () => {
        // Gone from the strip counts as terminal: fm lists only work in
        // flight, so a cancelled job vanishes rather than going grey
        // (docs/SIDEBAR.md M3a). Checked FIRST — getAttribute on a
        // detached row yields null, which would otherwise read as
        // 'running' forever and time out.
        if ((await row.count()) === 0) return 'gone';
        const st = await row.getAttribute('data-status').catch(() => null);
        if (st && st !== 'running') return st;
        await page.getByTestId(`bulk-cancel-${jobId}`).dispatchEvent('click').catch(() => {});
        return 'running';
      },
      { timeout: 20_000, intervals: [100] },
    )
    .not.toBe('running');

  // The BE is the authority on why it left flight: cancelled, not finished.
  await router.waitForLog(new RegExp(`bulk-ops job=${jobId} op=upload status=cancelled`), 10_000);
  // Cancel landed mid-stream → not every file made it to disk.
  expect(readdirSync(router.fmRoot).filter((n) => n.startsWith('f-')).length).toBeLessThan(N);
});
