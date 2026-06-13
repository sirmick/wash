// Terminal reattach fidelity (audit B2/B3): terminal modes survive a
// reload even when their set-sequences scrolled out of the 256KB
// replay window, and a geometry change across the reload reflows the
// replay + resizes the pty to the new grid.

import { test, expect } from '../fixtures/router';

async function openTerminal(page: import('@playwright/test').Page, routerUrl: string) {
  await page.goto(routerUrl);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
  const term = page.locator('wash-app-term');
  await expect(term).toBeVisible();
  await expect(term).toContainText(/\$|#|>/, { timeout: 10_000 });
  return term;
}

// Read xterm state via the __washTerm handle the Terminal component
// stamps on the term-host div.
function readModes(page: import('@playwright/test').Page) {
  return page.evaluate(() => {
    const host = document.querySelector('[data-testid="term-host"]') as
      | (HTMLElement & { __washTerm?: { modes: Record<string, boolean> } })
      | null;
    const m = host?.__washTerm?.modes;
    return m
      ? { bracketedPaste: m.bracketedPasteMode, appCursor: m.applicationCursorKeysMode }
      : null;
  });
}

test.describe('terminal mode persistence', () => {
  test('modes set before the replay window survive a reload', async ({ page, router }) => {
    test.setTimeout(40_000);
    const term = await openTerminal(page, router.url);

    // Set bracketed paste + application cursor keys, then push the
    // set-sequences out of the 256KB scrollback ring with ~340KB of
    // output — the replay alone can no longer restore them.
    await term.click();
    await page.keyboard.type("printf '\\e[?2004h\\e[?1h'; seq 1 50000 | tail -1");
    await page.keyboard.press('Enter');
    await expect(term).toContainText('50000', { timeout: 20_000 });
    await expect.poll(() => readModes(page), { timeout: 5_000 }).toMatchObject({
      bracketedPaste: true,
      appCursor: true,
    });
    // Let the 500ms mode-persist debounce flush.
    await page.waitForTimeout(1_000);

    await page.reload();
    const term2 = page.locator('wash-app-term');
    await expect(term2).toBeVisible();
    // Replay landed (ring tail) — and the modes were re-seeded from
    // persisted state, not from the replay.
    await expect(term2).toContainText('50000', { timeout: 10_000 });
    await expect.poll(() => readModes(page), { timeout: 10_000 }).toMatchObject({
      bracketedPaste: true,
      appCursor: true,
    });

    // The channel is still live.
    const marker = `mode-live-${Date.now()}`;
    await term2.click();
    await page.keyboard.type(`echo ${marker}`);
    await page.keyboard.press('Enter');
    await expect(term2).toContainText(marker, { timeout: 5_000 });
  });
});

test.describe('terminal geometry across reload', () => {
  test('maximized terminal reflows and resizes the pty to the new viewport', async ({ page, router }) => {
    test.setTimeout(40_000);
    await page.setViewportSize({ width: 1200, height: 800 });
    const term = await openTerminal(page, router.url);

    // Maximize so the window (and thus the grid) tracks the viewport.
    const id = await page.evaluate(() => window.wash.windows().find((w) => w.title.includes('Terminal'))!.windowID);
    await page.evaluate((wid) => window.wash.maximizeWindow(wid), id);
    await page.waitForTimeout(300);

    await term.click();
    await page.keyboard.type('tput cols');
    await page.keyboard.press('Enter');
    // Grid at 1200px wide — remember what the pty believes.
    const colsBefore = await page.evaluate(() => {
      const host = document.querySelector('[data-testid="term-host"]') as
        | (HTMLElement & { __washTerm?: { cols: number } })
        | null;
      return host?.__washTerm?.cols ?? 0;
    });
    expect(colsBefore).toBeGreaterThan(0);
    await expect(term).toContainText(String(colsBefore), { timeout: 5_000 });

    // Come back on a much narrower viewport: the restored xterm must
    // open at the OLD grid (replay renders at the width it was
    // emitted for), then refit and push the NEW grid to the pty.
    await page.setViewportSize({ width: 700, height: 500 });
    await page.reload();
    const term2 = page.locator('wash-app-term');
    await expect(term2).toBeVisible();
    await expect(term2).toContainText(String(colsBefore), { timeout: 10_000 }); // replay survived

    await expect
      .poll(
        () =>
          page.evaluate(() => {
            const host = document.querySelector('[data-testid="term-host"]') as
              | (HTMLElement & { __washTerm?: { cols: number } })
              | null;
            return host?.__washTerm?.cols ?? 0;
          }),
        { timeout: 10_000 },
      )
      .toBeLessThan(colsBefore);

    // Full round-trip proof: the SHELL also sees the new width — the
    // refit reached the pty (SIGWINCH), not just the renderer.
    await term2.click();
    await page.keyboard.type('tput cols');
    await page.keyboard.press('Enter');
    const colsAfter = await page.evaluate(() => {
      const host = document.querySelector('[data-testid="term-host"]') as
        | (HTMLElement & { __washTerm?: { cols: number } })
        | null;
      return host?.__washTerm?.cols ?? 0;
    });
    await expect(term2.locator(`text=${colsAfter}`).first()).toBeVisible({ timeout: 5_000 });
  });
});
