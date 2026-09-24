// A new wash-term tab opens its pty at the grid its pane will have, not at
// 80×24 to be resized a frame later (apps/term/fe/src/main.tsx openNewTab).
// The FE predicts the pane's rect through the layout kernel and converts
// it with the mounted terminal's cell metrics; the BE opens the pty at that
// size and logs it.
//
// Both halves: the BE's "tab opened … cols=C rows=R" line is the grid the
// pty was CREATED with, and `stty size` inside that pane is the grid it
// ended up with once the FE's own fit had every chance to correct it. They
// must agree — and must not be the default.

import { test, expect } from '../fixtures/router';
import type { Locator, Page } from '@playwright/test';

async function bufferOf(host: Locator): Promise<string> {
  return await host.evaluate((el: any) => {
    const term = el.__washTerm;
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

async function openTerminal(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
  await expect(page.locator('wash-app-term')).toBeVisible();
  const host = page.locator('[data-testid="term-host"]').first();
  await expect(host).toBeVisible();
  await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/[$#%>][ ]?/);
  await host.click();
  return host;
}

// sizeOf asks the pty what grid it has. Tagged so the poll cannot match a
// previous probe's output (docs/FLAKE_LOG.md stale-match failure mode).
let probeSeq = 0;
async function sizeOf(page: Page, host: Locator): Promise<{ cols: number; rows: number }> {
  const tag = `SZ${++probeSeq}`;
  await host.click();
  await page.keyboard.type(`echo ${tag}=$(stty size | tr ' ' x)`);
  await page.keyboard.press('Enter');
  let got = { cols: 0, rows: 0 };
  await expect.poll(async () => {
    const m = (await bufferOf(host)).match(new RegExp(`^${tag}=(\\d+)x(\\d+)$`, 'm'));
    if (!m) return 0;
    got = { rows: Number(m[1]), cols: Number(m[2]) };
    return got.cols;
  }, { timeout: 10_000 }).toBeGreaterThan(0);
  return got;
}

// openedGrid parses the BE's tab-opened line for the Nth tab of the window.
function openedGrids(log: string): Array<{ cols: number; rows: number }> {
  return [...log.matchAll(/wash-term tab opened ch=\d+ .*cols=(\d+) rows=(\d+)/g)]
    .map((m) => ({ cols: Number(m[1]), rows: Number(m[2]) }));
}

test.describe('term new-tab grid', () => {
  test.setTimeout(60_000);

  test('a tab opened by split-right gets its pane grid from the start', async ({ page, router }) => {
    const first = await openTerminal(page, router.url);
    await router.waitForLog(/wash-term tab opened ch=\d+/, 10_000);
    const full = await sizeOf(page, first);

    await first.click();
    await page.keyboard.press('Control+Shift+D');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2);
    await expect.poll(() => openedGrids(router.log()).length, { timeout: 10_000 }).toBe(2);
    const opened = openedGrids(router.log())[1];

    // The pane is half the window, so its grid is nowhere near the 80×24
    // default the BE would otherwise have used…
    expect(opened.cols).not.toBe(80);
    expect(opened.cols).toBeLessThan(full.cols);
    // …and it is exactly what the pty reports once the FE has fitted it.
    const right = page.locator('[data-testid="term-host"]:visible').last();
    await expect.poll(() => bufferOf(right), { timeout: 10_000 }).toMatch(/[$#%>][ ]?/);
    const actual = await sizeOf(page, right);
    expect(actual).toEqual(opened);
  });

  test('a plain New Tab in a pane gets that pane grid from the start', async ({ page, router }) => {
    const first = await openTerminal(page, router.url);
    await router.waitForLog(/wash-term tab opened ch=\d+/, 10_000);
    const full = await sizeOf(page, first);

    await page.locator('[data-testid="term-new-tab"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);
    await expect.poll(() => openedGrids(router.log()).length, { timeout: 10_000 }).toBe(2);
    const opened = openedGrids(router.log())[1];
    // Same pane, same grid — predicted, not defaulted.
    expect(opened).toEqual(full);
    const second = page.locator('[data-testid="term-host"]:visible').first();
    await expect.poll(() => bufferOf(second), { timeout: 10_000 }).toMatch(/[$#%>][ ]?/);
    expect(await sizeOf(page, second)).toEqual(opened);
  });
});
