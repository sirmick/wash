// Terminal live reconnect: a same-router WS drop WITHOUT a page reload must
// not duplicate scrollback (REVIEW-RECONNECT H1 / REVIEW-DATAPATH F3).
//
// On a live-page reconnect the xterm element, its tab, and its raw
// subscription all survive. reattachChannelsToShell replays the ring
// snapshot; if it isn't preceded by a channel.resync the FE appends it to a
// terminal that already shows those bytes → the marker appears twice in the
// xterm buffer. With the A1 fix the router sends channel.resync first, so the
// terminal is reset before the replay and the marker appears exactly once.
//
// Sequence:
//   1. Open wash-term, emit a unique marker (only in OUTPUT, not the typed
//      command line — the '' split keeps the command echo from matching).
//   2. Force a transient WS drop via window.__washDropSocket() (real
//      onclose → scheduleReconnect; same router, channels persist).
//   3. Wait for the socket to come back (diag state open, a reconnect ticked).
//   4. Assert the marker appears exactly once in the full xterm buffer.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

async function bufferText(page: Page): Promise<string> {
  return await page.locator('[data-testid="term-host"]').first().evaluate((host: any) => {
    const term = host.__washTerm;
    if (!term) return '';
    const buf = term.buffer.active;
    let out = '';
    for (let y = 0; y < buf.length; y++) {
      const line = buf.getLine(y);
      if (!line) continue;
      out += line.translateToString(true) + '\n';
    }
    return out;
  });
}

test.describe('terminal live reconnect', () => {
  test('WS drop without reload does not duplicate scrollback', async ({ page, router }) => {
    test.setTimeout(30_000);
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
    const term = page.locator('wash-app-term');
    await expect(term).toBeVisible();
    await expect(term).toContainText(/\$|#|>/, { timeout: 10_000 });

    // The marker as it appears in OUTPUT. The typed command splits it with an
    // empty '' so the command-echo line never contains the contiguous string.
    const ts = Date.now();
    const marker = `wash-marker-${ts}`;
    await term.click();
    await page.keyboard.type(`echo wash-mark''er-${ts}`);
    await page.keyboard.press('Enter');
    await expect(term).toContainText(marker, { timeout: 5_000 });

    const before = await page.evaluate(() => (window as any).__washDiag?.().conn.totalReconnects ?? 0);

    // Force a transient WS drop (no page reload).
    await page.evaluate(() => (window as any).__washDropSocket?.());

    // Wait for the reconnect to complete: the socket is open again and at
    // least one reconnect has ticked.
    await expect.poll(async () => {
      const d = await page.evaluate(() => (window as any).__washDiag?.().conn ?? null);
      return !!(d && d.state === 'open' && d.totalReconnects > before);
    }, { timeout: 15_000 }).toBe(true);

    // The reattach replay + resync land after the socket opens. Poll the full
    // xterm buffer for the marker count: exactly one (reset-then-replay), not
    // two (append-without-reset).
    await expect.poll(async () => {
      const txt = await bufferText(page);
      return txt.split(marker).length - 1;
    }, { timeout: 5_000 }).toBe(1);

    // And the channel is still live after the reconnect.
    const ts2 = Date.now();
    const marker2 = `wash-after-${ts2}`;
    await term.click();
    await page.keyboard.type(`echo wash-aft''er-${ts2}`);
    await page.keyboard.press('Enter');
    await expect(term).toContainText(marker2, { timeout: 5_000 });
  });
});
