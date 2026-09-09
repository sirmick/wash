// Tab verbs in wash-term (docs/Review-findings.md P2 → term "manual tab
// rename", "window title never reflects the active tab", "restart-hung-shell
// verb", "file drop from fm"):
//
//   - double-click (or the tab context menu) renames a tab, and the name
//     beats the OSC titles the shell keeps setting until it is cleared;
//   - the window's titlebar says what the focused tab says;
//   - "Restart shell" replaces the tab's pty in the same pane, in the same
//     directory;
//   - dropping paths on a pane types them, shell-quoted, with no Enter.

import { test, expect } from '../fixtures/router';
import type { Locator, Page } from '@playwright/test';
import { mkdtempSync, realpathSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

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

async function tabIds(page: Page): Promise<number[]> {
  const ids = await page.locator('[data-testid^="term-tab-"]').evaluateAll((els) =>
    els.map((e) => (e.getAttribute('data-testid') ?? '').match(/^term-tab-(\d+)$/)?.[1])
      .filter((v): v is string => !!v));
  return ids.map(Number);
}

// windowTitle reads the router's own session state, not the app's DOM —
// the claim is that the WINDOW is named, which is the shell's business.
const windowTitle = (page: Page) => page.evaluate(() =>
  (window as any).wash.windows().find((w: any) => w.element === 'wash-app-term')?.title ?? '');

test.describe('term tab verbs', () => {
  test.setTimeout(90_000);

  test('a renamed tab keeps its name through the shell’s own titles, and the window follows', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    const id = (await tabIds(page))[0];
    const tab = page.locator(`[data-testid="term-tab-${id}"]`);

    await tab.dblclick();
    const box = page.locator(`[data-testid="term-tab-rename-${id}"]`);
    await expect(box).toBeFocused();
    await box.fill('build');
    await box.press('Enter');
    await expect(tab).toHaveText(/build/);
    await expect.poll(() => windowTitle(page), { timeout: 10_000 }).toBe('build');

    // The shell retitles on every prompt, so the OSC title is set and then
    // HELD (the sleep keeps the next prompt away) — otherwise the window
    // in which the two could disagree is a few milliseconds wide.
    const setTitle = async (t: string) => {
      await host.click();
      await page.keyboard.type(`printf '\\033]0;${t}\\a'; sleep 4`);
      await page.keyboard.press('Enter');
    };

    await setTitle('shell-said-this');
    await page.waitForTimeout(1500);
    // The manual name wins for as long as it exists.
    await expect(tab).toHaveText(/build/);
    expect(await windowTitle(page)).toBe('build');

    // Clearing the name hands the tab back to the program's titles.
    await tab.dblclick();
    // Wait for focus to settle before typing: the click that opens the box
    // also refocuses the pane, and the box commits on blur.
    await expect(box).toBeFocused();
    await box.fill('');
    await box.press('Enter');
    await setTitle('shell-said-that');
    await expect(tab).toHaveText(/shell-said-that/, { timeout: 10_000 });
    await expect.poll(() => windowTitle(page), { timeout: 10_000 }).toBe('shell-said-that');
  });

  test('Escape abandons a rename', async ({ page, router }) => {
    await openTerminal(page, router.url);
    const id = (await tabIds(page))[0];
    const tab = page.locator(`[data-testid="term-tab-${id}"]`);
    const before = await tab.textContent();
    await tab.dblclick();
    const box = page.locator(`[data-testid="term-tab-rename-${id}"]`);
    await expect(box).toBeFocused();
    await box.fill('discarded');
    await box.press('Escape');
    await expect(box).toHaveCount(0);
    expect(await tab.textContent()).toBe(before);
  });

  test('Restart shell replaces the pty in the same directory', async ({ page, router }) => {
    const dir = realpathSync(mkdtempSync(join(tmpdir(), 'wash-restart-')));
    const host = await openTerminal(page, router.url);
    const before = (await tabIds(page))[0];

    // Go somewhere identifiable, and leave a mark the new shell cannot have.
    await host.click();
    await page.keyboard.type(`cd ${dir} && echo OLDSHELL=$$`);
    await page.keyboard.press('Enter');
    await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/^OLDSHELL=\d+$/m);

    await page.locator(`[data-testid="term-tab-${before}"]`).click({ button: 'right' });
    await page.locator('[data-testid="term-tab-restart"]').click();

    // A NEW channel, in a strip that still has exactly one tab.
    await expect.poll(async () => {
      const ids = await tabIds(page);
      return ids.length === 1 && ids[0] !== before;
    }, { timeout: 20_000 }).toBe(true);

    const fresh = page.locator('[data-testid="term-host"]').first();
    await expect.poll(() => bufferOf(fresh), { timeout: 15_000 }).toMatch(/[$#%>][ ]?/);
    // Same directory…
    await fresh.click();
    await page.keyboard.type('pwd');
    await page.keyboard.press('Enter');
    await expect.poll(() => bufferOf(fresh), { timeout: 10_000 }).toContain(dir);
    // …and genuinely a different shell: the old one's scrollback is gone.
    expect(await bufferOf(fresh)).not.toMatch(/OLDSHELL=/);
  });

  test('dropping paths on a pane types them shell-quoted, without running anything', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    await host.click();
    await page.keyboard.type('echo ');

    // Synthesise the drag fm makes: application/x-wash-paths, JSON array.
    await host.evaluate((el: HTMLElement) => {
      const dt = new DataTransfer();
      dt.setData('application/x-wash-paths', JSON.stringify(['/etc/hosts', '/tmp/My Docs/a b.txt']));
      el.dispatchEvent(new DragEvent('dragover', { dataTransfer: dt, bubbles: true, cancelable: true }));
      el.dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true }));
    });

    // The paths are on the command LINE — quoted, space-separated, not run.
    await expect.poll(() => bufferOf(host), { timeout: 10_000 })
      .toMatch(/echo \/etc\/hosts '\/tmp\/My Docs\/a b\.txt'/);
    expect(await bufferOf(host)).not.toMatch(/^\/etc\/hosts/m);

    // Only when the user says so does it run.
    await page.keyboard.press('Enter');
    await expect.poll(() => bufferOf(host), { timeout: 10_000 })
      .toMatch(/^\/etc\/hosts \/tmp\/My Docs\/a b\.txt$/m);
  });
});
