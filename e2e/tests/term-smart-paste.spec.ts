// Smart paste, M5 (docs/AGENT_TERM.md §10, issue #19 item 3).
//
// The assertion that matters is not "an overlay appeared" but "these exact
// bytes reached the pty", so every case pastes into `cat > file` and the
// test reads the file back off the local filesystem. That is the real
// contract: what the shell would have executed.
//
// Payloads are injected through the browser clipboard (the wash paste path
// prefers the system clipboard on a secure origin) and, in one case, as a
// synthesized native paste event — the path a plain Ctrl+V takes, which
// bypasses wash's own menu entirely.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';
import { mkdtempSync, readFileSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

// A command a chat soft-wrapped into two lines: no shell structure, no
// trailing operators, lines clustered near a wrap column.
const WRAPPED =
  'docker run --rm -it --name my-container --volume /home/mick/data:/data\n' +
  '--env MODE=production --network host ghcr.io/example/image:latest';
const WRAPPED_JOINED =
  'docker run --rm -it --name my-container --volume /home/mick/data:/data ' +
  '--env MODE=production --network host ghcr.io/example/image:latest';

// One line carrying a non-breaking space and curly quotes — invisible junk,
// nothing structural.
const JUNKED = 'git commit -m “fix the thing”';
const JUNKED_CLEAN = 'git commit -m "fix the thing"';

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

async function openTerminal(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
  await expect(page.locator('wash-app-term')).toBeVisible();
  await expect(page.locator('[data-testid="term-host"]').first()).toBeVisible();
  await expect.poll(() => bufferText(page), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);
  await page.locator('[data-testid="term-host"]').first().click();
}

// capture starts `cat > path` so whatever is pasted next lands in a file the
// test can read. Returns a finish() that closes the capture.
async function capture(page: Page, path: string) {
  await page.locator('[data-testid="term-host"]').first().click();
  await page.keyboard.type(`cat > ${path}`);
  await page.keyboard.press('Enter');
  // The prompt is gone once cat is reading; give the shell a beat.
  await page.waitForTimeout(300);
  return async () => {
    await page.locator('[data-testid="term-host"]').first().click();
    await page.keyboard.press('Control+d');
    await page.waitForTimeout(300);
  };
}

// pasted reads back what reached the pty. xterm's paste path normalizes
// newlines to CR, so CRs are folded back for comparison.
function pasted(path: string): string {
  if (!existsSync(path)) return '';
  return readFileSync(path, 'utf8').replace(/\r\n?/g, '\n');
}

async function seedClipboard(page: Page, text: string) {
  await page.evaluate((t) => navigator.clipboard.writeText(t), text);
}

// menuPaste drives Edit ▸ Paste. (The menubar also has a "Paste" MENU for
// the smart-paste setting — different testid, term-menu-paste-btn.)
async function menuPaste(page: Page) {
  await page.locator('[data-testid="term-menu-edit-btn"]').click();
  await page.locator('[data-testid="term-menu-edit"]').waitFor();
  await page.locator('[data-testid="term-menu-paste"]').click();
}

test.describe('smart paste (M5)', () => {
  test.setTimeout(45_000);

  let dir = '';
  test.beforeEach(async ({ context, router }) => {
    dir = mkdtempSync(join(tmpdir(), 'wash-e2e-paste-'));
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: router.url });
  });

  test('invisible junk is fixed silently — no overlay, clean bytes in the pty', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await seedClipboard(page, JUNKED);
    const out = join(dir, 'junk.txt');
    const finish = await capture(page, out);
    await menuPaste(page);
    await page.waitForTimeout(400);
    // A single line of invisible junk is never worth a dialog.
    await expect(page.locator('[data-testid="term-paste-overlay"]')).toHaveCount(0);
    await finish();
    expect(pasted(out).trim()).toBe(JUNKED_CLEAN);
  });

  test('a wrapped command asks first, then pastes as one line', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await seedClipboard(page, WRAPPED);
    const out = join(dir, 'wrapped.txt');
    const finish = await capture(page, out);
    await menuPaste(page);

    const overlay = page.locator('[data-testid="term-paste-overlay"]');
    await expect(overlay).toBeVisible({ timeout: 10_000 });
    await expect(overlay).toContainText('one wrapped command');
    await expect(overlay.locator('[data-issue="wrapped"]')).toBeVisible();
    // The preview shows what would be sent, not what was copied.
    await expect(overlay.locator('[data-testid="term-paste-preview"]')).toContainText('--env MODE=production');

    await overlay.locator('[data-testid="term-paste-cleaned"]').click();
    await expect(overlay).toHaveCount(0);
    await finish();
    const got = pasted(out).trim();
    expect(got).toBe(WRAPPED_JOINED);
    expect(got).not.toContain('\n');
  });

  test('“Paste as-is” sends exactly what was copied', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await seedClipboard(page, WRAPPED);
    const out = join(dir, 'asis.txt');
    const finish = await capture(page, out);
    await menuPaste(page);
    const overlay = page.locator('[data-testid="term-paste-overlay"]');
    await expect(overlay).toBeVisible({ timeout: 10_000 });
    await overlay.locator('[data-testid="term-paste-asis"]').click();
    await expect(overlay).toHaveCount(0);
    await finish();
    expect(pasted(out).trim()).toBe(WRAPPED.trim());
  });

  test('Cancel sends nothing at all', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await seedClipboard(page, WRAPPED);
    const out = join(dir, 'cancel.txt');
    const finish = await capture(page, out);
    await menuPaste(page);
    const overlay = page.locator('[data-testid="term-paste-overlay"]');
    await expect(overlay).toBeVisible({ timeout: 10_000 });
    await overlay.locator('[data-testid="term-paste-cancel"]').click();
    await expect(overlay).toHaveCount(0);
    await finish();
    expect(pasted(out)).toBe('');
  });

  test('“Smart paste: off” disables the filter entirely', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.locator('[data-testid="term-menu-paste-btn"]').click();
    await page.locator('[data-testid="term-menu-paste-off"]').click();

    await seedClipboard(page, JUNKED);
    const out = join(dir, 'off.txt');
    const finish = await capture(page, out);
    await menuPaste(page);
    await page.waitForTimeout(400);
    await expect(page.locator('[data-testid="term-paste-overlay"]')).toHaveCount(0);
    await finish();
    // The junk arrives untouched — that is what "off" means.
    expect(pasted(out).trim()).toBe(JUNKED);
  });

  test('“Smart paste: always” repairs without asking', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.locator('[data-testid="term-menu-paste-btn"]').click();
    await page.locator('[data-testid="term-menu-paste-always"]').click();

    await seedClipboard(page, WRAPPED);
    const out = join(dir, 'always.txt');
    const finish = await capture(page, out);
    await menuPaste(page);
    await page.waitForTimeout(400);
    await expect(page.locator('[data-testid="term-paste-overlay"]')).toHaveCount(0);
    await finish();
    expect(pasted(out).trim()).toBe(WRAPPED_JOINED);
  });

  test('a native paste event (plain Ctrl+V) goes through the filter too', async ({ page, router }) => {
    // The path that bypasses wash's own menu: the browser delivers a paste
    // event straight to xterm's textarea. Without the capture-phase
    // interception this would land unfiltered.
    await openTerminal(page, router.url);
    const out = join(dir, 'native.txt');
    const finish = await capture(page, out);
    // Dispatch where the browser really delivers it: xterm's hidden
    // textarea, inside the component's own element. The filter listens in
    // the capture phase on that element, so the event has to originate at
    // (or under) it — dispatching on the outer wrapper would miss.
    await page.locator('[data-testid="term-host"]').first().evaluate((host, text) => {
      const target =
        host.querySelector('.xterm-helper-textarea') ??
        host.querySelector('.xterm-screen') ??
        host.querySelector('.xterm')!;
      const dt = new DataTransfer();
      dt.setData('text/plain', text as string);
      target.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    }, WRAPPED);

    const overlay = page.locator('[data-testid="term-paste-overlay"]');
    await expect(overlay).toBeVisible({ timeout: 10_000 });
    await overlay.locator('[data-testid="term-paste-cleaned"]').click();
    await finish();
    expect(pasted(out).trim()).toBe(WRAPPED_JOINED);
  });
});
