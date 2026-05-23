// Terminal reattach: PTY scrollback survives a browser refresh.
//
// Sequence:
//   1. Open wash-term, run a command whose output ends with a unique marker.
//   2. Wait for the marker to appear in the xterm.
//   3. Refresh the browser.
//   4. After the snapshot + reattach + scrollback replay, the marker
//      should still be visible in the xterm — proof that the ring
//      buffer captured the pty output and the new shell received the
//      replay frame.

import { test, expect } from '../fixtures/router';

test.describe('terminal reattach', () => {
  test('pty scrollback survives browser refresh', async ({ page, router }) => {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
    const term = page.locator('wash-app-term');
    await expect(term).toBeVisible();
    // The first prompt arrives within a beat.
    await expect(term).toContainText(/\$|#|>/, { timeout: 10_000 });

    // Type a command that emits a unique tag, then wait until it lands
    // in the visible xterm canvas (xterm uses a canvas/textarea pair,
    // and Playwright's toContainText matches the DOM text layer).
    const marker = `wash-marker-${Date.now()}`;
    await term.click();
    await page.keyboard.type(`echo ${marker}`);
    await page.keyboard.press('Enter');
    await expect(term).toContainText(marker, { timeout: 5_000 });

    // Refresh. The wash-term process keeps running (router-side
    // supervision); the channel binding is reattached to the new
    // shell and the ring buffer's contents are replayed before
    // any new pty output.
    await page.reload();
    const term2 = page.locator('wash-app-term');
    await expect(term2).toBeVisible();

    // Same marker should still be visible after the replay lands.
    await expect(term2).toContainText(marker, { timeout: 10_000 });

    // And the channel is live: typing a new command echoes back.
    const marker2 = `wash-after-${Date.now()}`;
    await term2.click();
    await page.keyboard.type(`echo ${marker2}`);
    await page.keyboard.press('Enter');
    await expect(term2).toContainText(marker2, { timeout: 5_000 });
  });
});
