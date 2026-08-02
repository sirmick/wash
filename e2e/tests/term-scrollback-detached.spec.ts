// Detached scrollback: output produced while NOBODY is attached survives
// well past the steady-state buffer.
//
// wash never stalls a process to preserve its output — the pty keeps
// running with the lid shut — so the only lever is how much is buffered.
// The ring therefore grows while a channel has no reader (up to
// ChannelScrollbackMaxBytes) and shrinks again once a shell has taken
// delivery. This spec proves the user-visible half of that: half a
// megabyte written while disconnected is all there when you come back,
// where the old fixed 256 KiB buffer would have kept only the tail.
//
// The detach is explicit (navigate away, wait, navigate back) rather than
// a reload, because a reload reattaches in well under a second and the
// output would mostly arrive with a shell already listening.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

// ~35 bytes × 15,000 lines ≈ 525 KB: comfortably past the 256 KiB base
// buffer, comfortably inside both the 4 MiB ceiling and xterm's own
// 20,000-line client-side scrollback, so a miss is a real miss.
const LINES = 15_000;
const GEN = `seq -f 'line-%.0f-padpadpadpadpadpadpad' 1 ${LINES}`;

async function bufferText(page: Page): Promise<string> {
  return await page.locator('[data-testid="term-host"]').first().evaluate((host: any) => {
    const term = host.__washTerm;
    if (!term) return '';
    const buf = term.buffer.active;
    let out = '';
    for (let y = 0; y < buf.length; y++) {
      const line = buf.getLine(y);
      if (line) out += line.translateToString(true) + '\n';
    }
    return out;
  });
}

test.describe('detached scrollback', () => {
  test('output written while disconnected survives past the base buffer', async ({ page, router }) => {
    test.setTimeout(60_000);
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
    await expect(page.locator('wash-app-term')).toBeVisible();
    await expect.poll(() => bufferText(page), { timeout: 10_000 }).toMatch(/[$#%>][ ]?/);

    // Arm the output to start AFTER we have gone away, then leave.
    await page.locator('[data-testid="term-host"]').first().click();
    await page.keyboard.type(`sleep 2; ${GEN}; echo DETACHED-RUN-DONE`);
    await page.keyboard.press('Enter');
    await page.goto('about:blank');

    // Half a megabyte lands in a router that has nobody to give it to.
    await page.waitForTimeout(6_000);

    // Come back: bind → resync → replay.
    await page.goto(router.url);
    await expect(page.locator('wash-app-term')).toBeVisible();
    await expect.poll(() => bufferText(page), { timeout: 20_000 }).toContain('DETACHED-RUN-DONE');

    const text = await bufferText(page);
    // The FIRST line is the assertion that matters: with the old fixed
    // 256 KiB ring it would have been overwritten long before we returned.
    expect(text).toContain('line-1-padpadpadpadpadpadpad');
    // …and the tail is there too, so this is "kept everything", not
    // "kept the head instead of the tail".
    expect(text).toContain(`line-${LINES}-padpadpadpadpadpadpad`);

    // The channel is live afterwards — a grown-then-shrunk ring still works.
    const marker = `after-${Date.now()}`;
    await page.locator('[data-testid="term-host"]').first().click();
    await page.keyboard.type(`echo ${marker}`);
    await page.keyboard.press('Enter');
    await expect.poll(() => bufferText(page), { timeout: 10_000 }).toContain(marker);
  });
});
