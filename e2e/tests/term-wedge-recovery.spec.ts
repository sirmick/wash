// Terminal robustness soak (docs/PTY_ROBUST.md).
//
// The terminal must never hang. Two stressors:
//   1. Reload churn — repeated reloads stack/zombie shells and re-run the
//      channel-ownership migration (Fix A). After every reload the terminal
//      must accept input and echo it back (the head shell owns the channel;
//      a stale owner can't black-hole input, and live forwarding resumes).
//   2. Heavy output burst — output far exceeding the 64 KiB credit window
//      must fully drain without hanging (Fix B: the forward never blocks the
//      child shell), and the terminal stays interactive afterward.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

async function openTerminal(page: Page) {
  await page.locator('button[title="Apps"]').click();
  await page
    .locator('[data-testid="start-menu"]')
    .getByRole('button', { name: 'Terminal', exact: true })
    .click();
  const term = page.locator('wash-app-term');
  await expect(term).toBeVisible();
  await expect(term).toContainText(/\$|#|>/, { timeout: 10_000 });
  return term;
}

test.describe('terminal robustness soak', () => {
  test('recovers across reload churn — input + output keep working', async ({ page, router }) => {
    test.setTimeout(90_000);
    await page.goto(router.url);
    await openTerminal(page);

    // Reload repeatedly. The wash-term pty is router-supervised and
    // survives each reload; the new shell becomes the foreground head and
    // must adopt the terminal channel (Fix A). Proof per round: a unique
    // command typed after the reload echoes back — both the input path
    // (head ownership) and the output path (reattach replay + live
    // forwarding) recovered. A hang here would time out.
    for (let i = 0; i < 8; i++) {
      await page.reload();
      const term = page.locator('wash-app-term');
      await expect(term).toBeVisible();
      await expect(term).toContainText(/\$|#|>/, { timeout: 10_000 });

      const marker = `wash-churn-${i}-${Date.now()}`;
      await term.click();
      await page.keyboard.type(`echo ${marker}`);
      await page.keyboard.press('Enter');
      await expect(term).toContainText(marker, { timeout: 10_000 });
    }
  });

  test('heavy output burst drains without hanging the terminal', async ({ page, router }) => {
    // 45s drain + 15s probe has to fit, with room for the terminal to open.
    test.setTimeout(90_000);
    await page.goto(router.url);
    const term = await openTerminal(page);

    // Emit a burst that dwarfs the 64 KiB per-channel credit window many
    // times over. If the forward ever blocked on credit/scheduler the
    // child shell would stall and the final line would never appear.
    const tag = `burst-${Date.now()}`;
    await term.click();
    // E''ND, not END: the shell echoes the command line back, and
    // toContainText reads xterm's RENDERED ROWS — so a plain end marker is
    // matched by the echo of the very command that starts the burst, and the
    // barrier below passes before a single line has drained. Whether it does
    // is a race with the burst scrolling that line out of the viewport, which
    // is why this spec failed two different ways: on CI the barrier passed
    // early and the interactivity probe then ran into a terminal that was
    // still flooding; locally under CPU contention the line had already
    // scrolled off, so the barrier waited for the real marker and timed out.
    // Splitting the literal in the shell keeps typed text and output distinct
    // (docs/FLAKE_LOG.md 2026-08-06).
    await page.keyboard.type(`for i in $(seq 1 20000); do echo ${tag}-$i; done; echo ${tag}-E''ND`);
    await page.keyboard.press('Enter');

    // The terminating marker proves the whole burst drained. 20000 lines is
    // ~8x the credit window and the FE has to RENDER them, so this is the
    // slowest barrier in the suite on a loaded 2-core runner — budgeted well
    // clear of the observed worst case rather than tuned to the median.
    await expect(term).toContainText(`${tag}-END`, { timeout: 45_000 });

    // And the terminal is still fully interactive after the burst — same
    // split-marker trick, so this proves the shell RAN the command, not
    // merely that the keystrokes echoed.
    const after = `wash-after-burst-${Date.now()}`;
    await page.keyboard.type(`echo ${after}-O''K`);
    await page.keyboard.press('Enter');
    await expect(term).toContainText(`${after}-OK`, { timeout: 15_000 });
  });
});
