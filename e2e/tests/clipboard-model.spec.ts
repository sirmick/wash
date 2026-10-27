// The clipboard model (docs/CLIPBOARD.md): two slots — the clipboard
// (Ctrl+C/V, menus) and the selection (whatever was last highlighted,
// pasted by middle-click, Alt+right-click, or left+right together) — and
// the "last change wins" rule between the wash clipboard and this
// computer's.
//
// A probe <input> is put on the page for the text-field cases: the shell's
// handling is document-wide, so any field behaves the same.

import { test, expect } from '../fixtures/router';
import type { Locator, Page } from '@playwright/test';
import { existsSync } from 'node:fs';

async function openTerminal(page: Page, url: string): Promise<Locator> {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
  const host = page.locator('[data-testid="term-host"]').first();
  await expect(host).toBeVisible();
  await expect.poll(() => bufferText(host), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);
  return host;
}

function bufferText(host: Locator): Promise<string> {
  return host.evaluate((el: any) => {
    const buf = el.__washTerm?.buffer.active;
    if (!buf) return '';
    let out = '';
    for (let y = 0; y < buf.length; y++) out += (buf.getLine(y)?.translateToString(true) ?? '') + '\n';
    return out;
  });
}

// probe adds a text field above everything, for the field cases.
async function probe(page: Page, value = ''): Promise<Locator> {
  await page.evaluate((v) => {
    const el = document.createElement('input');
    el.setAttribute('data-testid', 'probe');
    el.value = v;
    el.style.cssText = 'position:fixed;left:40px;top:40px;width:300px;z-index:2000000;font:16px monospace';
    document.body.appendChild(el);
  }, value);
  return page.locator('[data-testid="probe"]');
}

async function centre(l: Locator): Promise<{ x: number; y: number }> {
  const b = (await l.boundingBox())!;
  return { x: b.x + b.width / 2, y: b.y + b.height / 2 };
}

const clipboard = (page: Page) => page.evaluate(() => (window as any).wash.clipboardPeek().text as string);
const selection = (page: Page) => page.evaluate(() => (window as any).wash.selectionPeek().text as string);

test.describe('clipboard model', () => {
  test.setTimeout(45_000);

  test('highlighting fills the selection, never the clipboard; middle-click pastes it in the terminal', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    await page.evaluate(() => (window as any).wash.clipboardSetText('kept-on-clipboard'));
    const field = await probe(page, 'highlight-me-123');
    await field.click({ clickCount: 3 });
    await expect.poll(() => selection(page)).toBe('highlight-me-123');
    expect(await clipboard(page)).toBe('kept-on-clipboard');

    const at = await centre(host);
    await page.mouse.click(at.x, at.y, { button: 'middle' });
    await expect.poll(() => bufferText(host)).toContain('highlight-me-123');
  });

  test('Alt+right-click and left+right paste the selection into a field', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.evaluate(() => (window as any).wash.selectionSetText('SEL'));
    const field = await probe(page, '');
    const at = await centre(field);

    await field.click();
    await page.keyboard.down('Alt');
    await page.mouse.click(at.x, at.y, { button: 'right' });
    await page.keyboard.up('Alt');
    await expect(field).toHaveValue('SEL');
    await expect(page.locator('[data-testid="text-context-menu"]')).toHaveCount(0);

    // The chord: both buttons down together is one middle-click, and no
    // menu opens for its right half.
    await page.mouse.move(at.x, at.y);
    await page.mouse.down({ button: 'left' });
    await page.mouse.down({ button: 'right' });
    await page.mouse.up({ button: 'right' });
    await page.mouse.up({ button: 'left' });
    await expect(field).toHaveValue(/SEL.*SEL|SELSEL/);
    await page.waitForTimeout(200);
    await expect(page.locator('[data-testid="text-context-menu"]')).toHaveCount(0);
  });

  test('right-click is the wash menu in the terminal; Shift+right-click is left to the browser', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    const at = await centre(host);
    await page.mouse.click(at.x, at.y, { button: 'right' });
    await expect(page.locator('[data-testid="term-context-menu"]')).toBeVisible();
    await expect(page.locator('[data-testid="term-ctx-paste-selection"]')).toBeVisible();
    await page.keyboard.press('Escape');
    await page.mouse.click(5, 5);

    // Nothing in wash may claim a Shift+right-click: it is the browser's.
    const prevented = await host.evaluate((el) => {
      const t = el.querySelector('.xterm-screen') ?? el;
      const ev = new MouseEvent('contextmenu', { bubbles: true, cancelable: true, composed: true, shiftKey: true, button: 2 });
      t.dispatchEvent(ev);
      return ev.defaultPrevented;
    });
    expect(prevented).toBe(false);
  });

  test('Ctrl+Shift+V sends a literal ^V; plain Ctrl+V is left to the browser', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    await host.click();
    await host.evaluate((el: any) => {
      (window as any).__termData = [];
      el.__washTerm.onData((d: string) => (window as any).__termData.push(d));
    });
    await page.keyboard.press('Control+Shift+V');
    await expect.poll(() => page.evaluate(() => (window as any).__termData.join(''))).toContain('\x16');
  });

  test('last change wins: an unchanged system clipboard yields to a newer wash copy', async ({ page, router }) => {
    await openTerminal(page, router.url);
    const field = await probe(page, '');
    await field.click();
    // This box last saw "stale-system" on its clipboard; since then the
    // wash clipboard got a copy from elsewhere (another box).
    await page.evaluate(() => {
      localStorage.setItem('wash.clipboard.systemSeen', 'stale-system');
      (window as any).wash.clipboardSetText('copied-on-box-a');
    });
    const paste = (text: string) => field.evaluate((el, t) => {
      const dt = new DataTransfer();
      dt.setData('text/plain', t);
      el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    }, text);
    await paste('stale-system');
    await expect(field).toHaveValue('copied-on-box-a');

    // Something new on this computer's clipboard wins, and becomes wash's.
    await field.fill('');
    await paste('copied-in-a-desktop-app');
    expect(await clipboard(page)).toBe('copied-in-a-desktop-app');
  });

  test('an image pasted into a terminal lands in a file, and its path is pasted', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    await host.click();
    await host.evaluate((el) => {
      // A 1x1 PNG.
      const b64 = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==';
      const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
      const dt = new DataTransfer();
      dt.items.add(new File([bytes], 'shot.png', { type: 'image/png' }));
      const ta = el.querySelector('textarea')!;
      ta.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    });
    let path = '';
    await expect.poll(async () => {
      path = (await bufferText(host)).match(/\S*\/wash\/paste\/paste-\S+\.png|\S*wash-paste-\d+\/paste-\S+\.png/)?.[0] ?? '';
      return path;
    }, { timeout: 10_000 }).not.toBe('');
    expect(existsSync(path.replace(/^'|'$/g, ''))).toBe(true);
    // It is the clipboard now, so the pane can show it.
    await expect.poll(() => page.evaluate(() => (window as any).wash.clipboardPeek().mime)).toBe('image/png');
  });

  test('the Clipboard pane shows both slots and the shortcuts', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.evaluate(() => {
      (window as any).wash.clipboardSetText('pane-clip');
      (window as any).wash.selectionSetText('pane-sel');
    });
    await page.locator('[data-testid="sidebar-section-header-clipboard"]').click();
    await expect(page.locator('[data-testid="clipboard-preview"]')).toContainText('pane-clip');
    await expect(page.locator('[data-testid="selection-preview"]')).toContainText('pane-sel');
    await expect(page.locator('[data-testid="clipboard-shortcuts"]')).toHaveCount(0);
    await page.locator('[data-testid="clipboard-shortcuts-toggle"]').click();
    await expect(page.locator('[data-testid="clipboard-shortcuts"]')).toContainText('Middle-click');

    await page.locator('[data-testid="selection-copy"]').click();
    await expect.poll(() => clipboard(page)).toBe('pane-sel');
    await page.locator('[data-testid="selection-clear"]').click();
    await expect(page.locator('[data-testid="selection-preview"]')).toContainText('nothing highlighted');
  });
});
