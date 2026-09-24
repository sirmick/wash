// Bell and activity in wash-term (docs/Review-findings.md P2 → term
// "bell/activity indicators", cross-app "terminal bell → attention"):
//
//   - BEL flashes the pane it came from — a bell you can see, since a wash
//     terminal has no speaker to ring;
//   - a bell in a tab you are not looking at leaves a mark on that tab
//     until you do, and so does plain output (a quieter dot);
//   - BEL also raises the WINDOW's attention flag, which is the desktop-wide
//     "this needs you" the taskbar pulses. The router only shows it while
//     the window is unfocused and clears it on focus, which is asserted
//     here from the router's own session state.

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

async function openTerminal(page: Page, url: string): Promise<Locator> {
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

// tabIds reads the channel ids off the rendered tabs, so the spec never
// has to guess what a second tab was numbered.
async function tabIds(page: Page): Promise<number[]> {
  const ids = await page.locator('[data-testid^="term-tab-"]').evaluateAll((els) =>
    els.map((e) => (e.getAttribute('data-testid') ?? '').match(/^term-tab-(\d+)$/)?.[1])
      .filter((v): v is string => !!v));
  return ids.map(Number);
}

test.describe('term bell + activity', () => {
  test.setTimeout(60_000);

  test('BEL flashes the pane it came from', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    await host.click();
    await page.keyboard.type("printf 'ding\\a'");
    const flash = page.locator('[data-testid="term-bell-flash"]');
    await expect(flash).toHaveCount(0);
    await page.keyboard.press('Enter');
    // The flash is deliberately brief, so catch it on the way past.
    await expect(flash).toBeVisible({ timeout: 5_000 });
    await expect(flash).toHaveCount(0, { timeout: 5_000 });
  });

  test('a background tab marks activity, and a bell there marks a bell; looking clears both', async ({ page, router }) => {
    await openTerminal(page, router.url);
    const first = (await tabIds(page))[0];

    // Second tab, which becomes the visible one.
    await page.locator('[data-testid="term-new-tab"]').click();
    await expect.poll(async () => (await tabIds(page)).length, { timeout: 15_000 }).toBe(2);
    const second = (await tabIds(page)).find((id) => id !== first)!;
    const host2 = page.locator('[data-testid="term-host"]').first();
    await expect.poll(() => bufferOf(host2), { timeout: 10_000 }).toMatch(/[$#%>][ ]?/);

    // Start a DELAYED printer in the first tab, then switch away, so the
    // output lands while that tab is off screen.
    await page.locator(`[data-testid="term-tab-${first}"]`).click();
    const host1 = page.locator('[data-testid="term-host"]').first();
    await host1.click();
    await page.keyboard.type('(sleep 2; printf "late\\n") &');
    await page.keyboard.press('Enter');
    await page.locator(`[data-testid="term-tab-${second}"]`).click();

    await expect(page.locator(`[data-testid="term-tab-activity-${first}"]`)).toBeVisible({ timeout: 15_000 });

    // Looking at the tab clears the dot.
    await page.locator(`[data-testid="term-tab-${first}"]`).click();
    await expect(page.locator(`[data-testid="term-tab-activity-${first}"]`)).toHaveCount(0, { timeout: 5_000 });

    // Now a bell from the background tab: a bell mark, not just a dot.
    await host1.click();
    await page.keyboard.type('(sleep 2; printf "\\a") &');
    await page.keyboard.press('Enter');
    await page.locator(`[data-testid="term-tab-${second}"]`).click();
    await expect(page.locator(`[data-testid="term-tab-bell-${first}"]`)).toBeVisible({ timeout: 15_000 });
    await page.locator(`[data-testid="term-tab-${first}"]`).click();
    await expect(page.locator(`[data-testid="term-tab-bell-${first}"]`)).toHaveCount(0, { timeout: 5_000 });
  });

  test('BEL asks for the human: the window carries attention while unfocused', async ({ page, router }) => {
    await openTerminal(page, router.url);

    // Attention is only SHOWN while the window is not the one being looked
    // at, so give the desktop another window to focus first.
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Files', exact: true }).click();
    await expect(page.locator('wash-app-fm')).toBeVisible({ timeout: 20_000 });

    // Ring the terminal's bell from behind: its shell is still running.
    const termHost = page.locator('wash-app-term [data-testid="term-host"]').first();
    await termHost.evaluate((el: any) => el.__washTerm.write(''));

    const attentionOn = () => page.evaluate(() =>
      (window as any).wash.windows().some((w: any) => w.element === 'wash-app-term' && w.attention));
    await expect.poll(attentionOn, { timeout: 15_000 }).toBe(true);

    // Looking at the window settles it — the router's job, asserted here
    // because that is what makes the flag safe to raise unconditionally.
    await page.locator('wash-app-term').click({ position: { x: 20, y: 5 } });
    await expect.poll(attentionOn, { timeout: 15_000 }).toBe(false);
  });
});
