// Clipboard over TLS — the production LAN shape: wash behind a
// TLS terminator, origin https://, SECURE browser context. Here
// navigator.clipboard exists, so the system-clipboard bridge is the
// real async API instead of the execCommand fallback:
//   - wash-side copies (terminal select, widget button) land in the
//     real system clipboard, asserted via clipboard.readText();
//   - system→wash flows through a native Ctrl+V paste;
//   - and the whole shell stack (WS handshake included) works through
//     an https/wss terminator — regression cover for the proxy path
//     itself, not just the clipboard.
//
// Counterpart: clipboard-insecure.spec.ts (plain HTTP on a
// non-localhost origin — no navigator.clipboard at all).

import { test, expect } from '../fixtures/router';
import { startTlsProxy, type TlsProxyHandle } from '../fixtures/tls-proxy';
import type { Locator } from '@playwright/test';

function termBuffer(host: Locator): Promise<string> {
  return host.evaluate((el: any) => {
    const term = el.__washTerm;
    if (!term) return '';
    const buf = term.buffer.active;
    let out = '';
    for (let y = 0; y < buf.length; y++) {
      const line = buf.getLine(y);
      if (!line) continue;
      out += line.translateToString(true) + '\n';
    }
    return out;
  });
}

async function selectInTerm(host: Locator, needle: string): Promise<void> {
  await host.evaluate((el: any, want: string) => {
    const term = el.__washTerm;
    const buf = term.buffer.active;
    for (let y = 0; y < buf.length; y++) {
      const line = buf.getLine(y);
      if (!line) continue;
      const col = line.translateToString(true).indexOf(want);
      if (col >= 0) {
        term.select(col, y, want.length);
        const inner = el.querySelector('.xterm') ?? el;
        inner.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
        return;
      }
    }
    throw new Error(`"${want}" not found in terminal buffer`);
  }, needle);
}

test.describe('clipboard over TLS (secure context)', () => {
  test.setTimeout(30_000);
  // The proxy cert is a self-signed throwaway.
  test.use({ ignoreHTTPSErrors: true });

  let proxy: TlsProxyHandle | null = null;
  test.afterEach(async () => {
    await proxy?.close();
    proxy = null;
  });

  test('shell loads over https/wss; secure context confirmed', async ({ page, router }) => {
    proxy = await startTlsProxy(router.url);
    await page.goto(proxy.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const ctx = await page.evaluate(() => ({
      secure: window.isSecureContext,
      hasClipboard: typeof navigator.clipboard?.readText === 'function',
      proto: window.location.protocol,
    }));
    expect(ctx).toEqual({ secure: true, hasClipboard: true, proto: 'https:' });

    // Healthy bridge → no degraded-clipboard warning in the widget.
    await page.locator('[data-testid="sidebar-section-header-clipboard"]').click();
    await expect(page.locator('[data-testid="clipboard-preview"]')).toBeVisible();
    await expect(page.locator('[data-testid="clipboard-warning"]')).toHaveCount(0);
  });

  test('terminal select-copy reaches the real system clipboard', async ({ page, router, context }) => {
    proxy = await startTlsProxy(router.url);
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: proxy.url });
    await page.goto(proxy.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
    const termHost = page.locator('[data-testid="term-host"]').first();
    await expect(termHost).toBeVisible();
    await expect.poll(() => termBuffer(termHost), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);
    await termHost.click();
    await page.keyboard.type('printf tls-clip-1357');
    await page.keyboard.press('Enter');
    await expect.poll(() => termBuffer(termHost)).toContain('tls-clip-1357');

    await selectInTerm(termHost, 'tls-clip-1357');

    // Wash clipboard got it (sidebar preview)…
    await page.locator('[data-testid="sidebar-section-header-clipboard"]').click();
    await expect(page.locator('[data-testid="clipboard-preview"]')).toContainText('tls-clip-1357');
    // …and so did the SYSTEM clipboard, via the async API this
    // secure context provides.
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe('tls-clip-1357');
  });

  test('system clipboard → terminal via native Ctrl+V', async ({ page, router, context }) => {
    proxy = await startTlsProxy(router.url);
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: proxy.url });
    await page.goto(proxy.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
    const termHost = page.locator('[data-testid="term-host"]').first();
    await expect(termHost).toBeVisible();
    await expect.poll(() => termBuffer(termHost), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);

    // Seed the real system clipboard, then paste natively — xterm's
    // textarea receives the browser paste event and forwards to the
    // pty; bash echoes it on the prompt line. keyboard.press synthesizes
    // the keystroke but Chromium doesn't run the paste editing command
    // for synthetic events, so send the command itself over CDP (the
    // same renderer path a physical Ctrl+V takes).
    await page.evaluate(() => navigator.clipboard.writeText('sys-paste-2468'));
    await termHost.click();
    const cdp = await context.newCDPSession(page);
    await cdp.send('Input.dispatchKeyEvent', {
      type: 'keyDown',
      key: 'v',
      code: 'KeyV',
      modifiers: 2, // Ctrl
      commands: ['paste'],
    });
    await cdp.detach();
    await expect.poll(() => termBuffer(termHost)).toContain('sys-paste-2468');
  });

  test('widget "Copy to system" writes the OS clipboard', async ({ page, router, context }) => {
    proxy = await startTlsProxy(router.url);
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: proxy.url });
    await page.goto(proxy.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    // Put something in the wash clipboard via the import box (paste
    // event), then push it out with the button.
    await page.locator('[data-testid="sidebar-section-header-clipboard"]').click();
    const box = page.locator('[data-testid="clipboard-import"]');
    await box.evaluate((el) => {
      const dt = new DataTransfer();
      dt.setData('text/plain', 'widget-sys-99');
      el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    });
    await expect(page.locator('[data-testid="clipboard-preview"]')).toContainText('widget-sys-99');

    await page.locator('[data-testid="clipboard-copy-system"]').click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe('widget-sys-99');
  });
});
