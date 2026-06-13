// Clipboard on an INSECURE origin — plain HTTP on a non-localhost
// host, i.e. how wash actually runs on the LAN today
// (http://192.168.x.x:11000). Chromium treats only localhost /
// 127.0.0.1 as "potentially trustworthy", so the default e2e
// environment silently runs every clipboard test in a SECURE context;
// this spec is the only coverage of the degraded path:
//   - navigator.clipboard is undefined — asserted, not assumed;
//   - all wash-internal flows (select-copy, right-click paste, ctx
//     menu, sidebar widget) still work via the wash clipboard;
//   - the widget's paste-import box — the designed inbound path when
//     programmatic system reads are impossible — works.
//
// The trick: --host-resolver-rules maps insecure.wash.test → 127.0.0.1
// so the page loads from the same loopback router but on an origin
// the browser refuses to bless.
//
// Counterpart: clipboard-secure.spec.ts (https through a TLS
// terminator — full system-clipboard bridge).

import { test, expect } from '../fixtures/router';
import type { Locator } from '@playwright/test';

const INSECURE_HOST = 'insecure.wash.test';

test.use({
  launchOptions: {
    args: [`--host-resolver-rules=MAP ${INSECURE_HOST} 127.0.0.1`],
  },
});

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

// router.url is http://127.0.0.1:<port>; same port, insecure hostname.
const insecureURL = (routerURL: string) => {
  const u = new URL(routerURL);
  return `http://${INSECURE_HOST}:${u.port}`;
};

test.describe('clipboard on an insecure origin (plain HTTP, non-localhost)', () => {
  test.setTimeout(30_000);

  test('context is insecure; navigator.clipboard is gone', async ({ page, router }) => {
    await page.goto(insecureURL(router.url));
    await expect(page.locator('wash-app-session')).toBeVisible();
    const ctx = await page.evaluate(() => ({
      secure: window.isSecureContext,
      clipboard: typeof navigator.clipboard,
    }));
    expect(ctx).toEqual({ secure: false, clipboard: 'undefined' });

    // The widget tells the user the bridge is degraded.
    await page.locator('[data-testid="sidebar-section-header-clipboard"]').click();
    await expect(page.locator('[data-testid="clipboard-warning"]')).toContainText('no HTTPS');
  });

  test('terminal select-copy → sidebar → editor paste, all without the system API', async ({ page, router }) => {
    await page.goto(insecureURL(router.url));
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
    const termHost = page.locator('[data-testid="term-host"]').first();
    await expect(termHost).toBeVisible();
    await expect.poll(() => termBuffer(termHost), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);
    await termHost.click();
    await page.keyboard.type('printf insec-clip-8642');
    await page.keyboard.press('Enter');
    await expect.poll(() => termBuffer(termHost)).toContain('insec-clip-8642');

    // Select-to-copy must land in the wash clipboard even though the
    // system mirror degrades to execCommand (or fails outright).
    await selectInTerm(termHost, 'insec-clip-8642');
    await page.locator('[data-testid="sidebar-section-header-clipboard"]').click();
    await expect(page.locator('[data-testid="clipboard-preview"]')).toContainText('insec-clip-8642');

    // Editor context-menu paste pulls from the wash clipboard — no
    // system involvement at all.
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
    const editor = page.locator('wash-app-edit');
    await expect(editor).toBeVisible();
    await editor.locator('[data-testid="edit-cm"]').click();
    await page.keyboard.press('Control+n');
    await expect(editor.locator('.cm-content')).toBeVisible();
    await editor.locator('[data-testid="edit-cm"]').click({ button: 'right' });
    await page.locator('[data-testid="edit-text-ctx-paste"]').click();
    await expect(editor.locator('.cm-content')).toContainText('insec-clip-8642');
  });

  test('editor Ctrl+C mirror → terminal right-click paste still works', async ({ page, router }) => {
    await page.goto(insecureURL(router.url));
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
    const editor = page.locator('wash-app-edit');
    await expect(editor).toBeVisible();

    await editor.locator('[data-testid="edit-cm"]').click();
    await page.keyboard.press('Control+n');
    await editor.locator('.cm-content').click();
    await page.keyboard.type('insec-edit-1212');
    await page.keyboard.press('Control+a');
    await page.keyboard.press('Control+c');

    await page.keyboard.press('Control+`');
    const termHost = editor.locator('[data-testid^="edit-term-host-"]').locator('visible=true');
    await expect(termHost).toBeVisible();
    await expect.poll(() => termBuffer(termHost), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);
    await termHost.click({ button: 'right' });
    await page.locator('[data-testid="term-ctx-paste"]').click();
    await expect.poll(() => termBuffer(termHost)).toContain('insec-edit-1212');
  });

  test('widget paste-import box imports without navigator.clipboard', async ({ page, router }) => {
    await page.goto(insecureURL(router.url));
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.locator('[data-testid="sidebar-section-header-clipboard"]').click();
    const box = page.locator('[data-testid="clipboard-import"]');
    await expect(box).toBeVisible();
    await box.evaluate((el) => {
      const dt = new DataTransfer();
      dt.setData('text/plain', 'insec-import-55');
      el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    });
    await expect(page.locator('[data-testid="clipboard-preview"]')).toContainText('insec-import-55');
  });
});
