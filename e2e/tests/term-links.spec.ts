// Clickable output in wash-term (docs/Review-findings.md P2 → term
// "clickable plain-text URLs and file paths"):
//
//   - an http(s) URL printed by a program is a link (@xterm/addon-web-links);
//   - an absolute or ./relative path that EXISTS is a link, resolved against
//     the tab's cwd and verified by the BE — a path that does not exist is
//     left as plain text, which is the whole point of the probe;
//   - clicking a file routes through the router's open routing (a .txt gets
//     wash-app-edit), clicking a directory opens fm.
//
// The click is a real mouse click at the cell the token occupies, computed
// from the buffer and the row's box — a test that called the provider
// directly would prove nothing about whether the link is reachable.

import { test, expect } from '../fixtures/router';
import type { Locator, Page } from '@playwright/test';
import { mkdtempSync, writeFileSync, realpathSync } from 'node:fs';
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

// echoLine prints one tagged line and waits for it, so the locator below
// matches this probe rather than an older one still in scrollback.
let seq = 0;
async function echoLine(page: Page, host: Locator, text: string): Promise<string> {
  const tag = `L${++seq}`;
  await host.click();
  await page.keyboard.type(`printf '%s %s\\n' ${tag} '${text}'`);
  await page.keyboard.press('Enter');
  const line = `${tag} ${text}`;
  await expect.poll(() => bufferOf(host), { timeout: 10_000 })
    .toContain(line);
  return line;
}

// cellOf locates `token` on the screen row that holds `line` and returns the
// page coordinates of its middle cell. Column maths comes off the row
// element's own box, so it survives any font/DPI the harness runs at.
async function cellOf(host: Locator, line: string, token: string): Promise<{ x: number; y: number }> {
  const pos = await host.evaluate((el: any, [ln, tk]: string[]) => {
    const term = el.__washTerm;
    const buf = term.buffer.active;
    for (let row = 0; row < term.rows; row++) {
      const bl = buf.getLine(buf.viewportY + row);
      if (!bl) continue;
      const s = bl.translateToString(true);
      if (!s.includes(ln)) continue;
      const col = s.indexOf(tk);
      if (col < 0) continue;
      return { row, col, cols: term.cols };
    }
    return null;
  }, [line, token]);
  if (!pos) throw new Error(`cellOf: ${token} not on screen`);
  const rowEl = host.locator('.xterm-rows > div').nth(pos.row);
  const box = await rowEl.boundingBox();
  if (!box) throw new Error('cellOf: no row box');
  const cw = box.width / pos.cols;
  return { x: box.x + (pos.col + Math.min(2, token.length / 2)) * cw, y: box.y + box.height / 2 };
}

// clickLink hovers first: xterm only asks a link provider about a line the
// pointer is on, and this provider's answer is a BE round trip.
async function clickLink(page: Page, at: { x: number; y: number }) {
  await page.mouse.move(at.x, at.y);
  await page.waitForTimeout(600);
  await page.mouse.move(at.x + 1, at.y);
  await page.waitForTimeout(400);
  await page.mouse.click(at.x, at.y);
}

test.describe('term links', () => {
  test.setTimeout(90_000);

  // Each click opens a window over the terminal, so each case gets its own
  // terminal rather than clicking twice through an editor.

  test('a path-shaped token that does not exist is not a link', async ({ page, router }) => {
    const dir = realpathSync(mkdtempSync(join(tmpdir(), 'wash-links-')));
    const ghost = join(dir, 'not-there.txt');
    const host = await openTerminal(page, router.url);
    const line = await echoLine(page, host, ghost);
    await clickLink(page, await cellOf(host, line, ghost));
    await page.waitForTimeout(2000);
    await expect(page.locator('wash-app-edit')).toHaveCount(0);
  });

  test('an existing .txt path opens in edit', async ({ page, router }) => {
    const dir = realpathSync(mkdtempSync(join(tmpdir(), 'wash-links-')));
    const file = join(dir, 'note.txt');
    writeFileSync(file, 'hello from a clickable path\n');
    const host = await openTerminal(page, router.url);
    const line = await echoLine(page, host, file);
    await clickLink(page, await cellOf(host, line, file));
    await expect(page.locator('wash-app-edit')).toBeVisible({ timeout: 20_000 });
  });

  test('a ./relative path resolves against the tab cwd', async ({ page, router }) => {
    const dir = realpathSync(mkdtempSync(join(tmpdir(), 'wash-links-')));
    writeFileSync(join(dir, 'rel.txt'), 'relative\n');
    const host = await openTerminal(page, router.url);
    await host.click();
    await page.keyboard.type(`cd ${dir} && pwd`);
    await page.keyboard.press('Enter');
    await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toContain(dir);

    const line = await echoLine(page, host, './rel.txt');
    await clickLink(page, await cellOf(host, line, './rel.txt'));
    await expect(page.locator('wash-app-edit')).toBeVisible({ timeout: 20_000 });
  });

  test('a directory opens fm', async ({ page, router }) => {
    const dir = realpathSync(mkdtempSync(join(tmpdir(), 'wash-links-')));
    const host = await openTerminal(page, router.url);
    const line = await echoLine(page, host, dir);
    await clickLink(page, await cellOf(host, line, dir));
    await expect(page.locator('wash-app-fm')).toBeVisible({ timeout: 20_000 });
  });

  test('an http URL in output is a link', async ({ page, router }) => {
    // The host does not resolve, so serve it from the test — otherwise the
    // popup lands on chrome's error page and the URL is unreadable.
    await page.context().route('http://example.invalid/**', (r) =>
      r.fulfill({ status: 200, contentType: 'text/plain', body: 'ok' }));
    const host = await openTerminal(page, router.url);
    const url = 'http://example.invalid/wash-links';
    const line = await echoLine(page, host, url);
    const at = await cellOf(host, line, url);
    const popup = page.waitForEvent('popup', { timeout: 20_000 });
    await clickLink(page, at);
    const win = await popup;
    await win.waitForLoadState('domcontentloaded').catch(() => {});
    expect(win.url()).toContain('example.invalid');
    await win.close();
  });
});
