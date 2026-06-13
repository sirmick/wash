// Integrated copy/paste — the wash clipboard as the bus between the
// editor, terminals, and the sidebar widget:
//   - terminal select-to-copy lands in the wash clipboard and shows
//     in the sidebar Clipboard widget,
//   - editor right-click → Paste inserts it into CodeMirror,
//   - editor native copy (Ctrl+C) is mirrored into the wash clipboard
//     by the shell's copy listener,
//   - terminal right-click pastes the wash clipboard into the pty
//     (PuTTY semantics, native context menu suppressed),
//   - the widget's paste-import box folds a system-clipboard paste
//     event into the wash clipboard.

import { test, expect } from '../fixtures/router';
import type { Page, Locator } from '@playwright/test';

// Read the full xterm scrollback for the terminal mounted on `host`.
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

// Select `needle` inside the terminal's buffer via the xterm API and
// fire mouseup on the host — the component's selection-end hook
// (select = copy) listens there. A real mouse drag would exercise the
// same path but is brittle against font metrics in headless runs.
async function selectInTerm(host: Locator, needle: string): Promise<void> {
  await host.evaluate((el: any, want: string) => {
    const term = el.__washTerm;
    const buf = term.buffer.active;
    for (let y = 0; y < buf.length; y++) {
      const line = buf.getLine(y);
      if (!line) continue;
      const text = line.translateToString(true);
      const col = text.indexOf(want);
      if (col >= 0) {
        term.select(col, y, want.length);
        // Fire mouseup from INSIDE the component's mount div (the
        // .xterm element) so it bubbles up through the listener;
        // dispatching on the outer host would never reach it.
        const inner = el.querySelector('.xterm') ?? el;
        inner.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
        return;
      }
    }
    throw new Error(`"${want}" not found in terminal buffer`);
  }, needle);
}

test.describe('clipboard integration', () => {
  test.setTimeout(30_000);

  test('terminal → sidebar → editor: select-copy, preview, ctx-menu paste', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    // Terminal: print a marker and select it.
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
    const termHost = page.locator('[data-testid="term-host"]').first();
    await expect(termHost).toBeVisible();
    await expect.poll(() => termBuffer(termHost), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);
    await termHost.click();
    await page.keyboard.type('printf wash-clip-7777');
    await page.keyboard.press('Enter');
    await expect.poll(() => termBuffer(termHost)).toContain('wash-clip-7777');

    // Select the printed marker (not the typed command line — pick the
    // output row, which is the last buffer hit) → wash clipboard.
    await selectInTerm(termHost, 'wash-clip-7777');

    // Sidebar widget previews it.
    await page.locator('[data-testid="sidebar-section-header-clipboard"]').click();
    await expect(page.locator('[data-testid="clipboard-preview"]')).toContainText('wash-clip-7777');

    // Editor: fresh buffer, right-click the text area, Paste.
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
    const editor = page.locator('wash-app-edit');
    await expect(editor).toBeVisible();
    await editor.locator('[data-testid="edit-cm"]').click();
    await page.keyboard.press('Control+n');
    await expect(editor.locator('.cm-content')).toBeVisible();
    await editor.locator('[data-testid="edit-cm"]').click({ button: 'right' });
    await page.locator('[data-testid="edit-text-ctx-paste"]').click();
    await expect(editor.locator('.cm-content')).toContainText('wash-clip-7777');
  });

  test('editor Ctrl+C mirror → terminal right-click paste (edit terminal pane)', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
    const editor = page.locator('wash-app-edit');
    await expect(editor).toBeVisible();

    // Type a marker into an untitled buffer, select all, native copy.
    // The shell's copy listener mirrors the DOM selection into the
    // wash clipboard — no editor-side wiring involved.
    await editor.locator('[data-testid="edit-cm"]').click();
    await page.keyboard.press('Control+n');
    await editor.locator('.cm-content').click();
    await page.keyboard.type('edit-clip-4242');
    await page.keyboard.press('Control+a');
    await page.keyboard.press('Control+c');

    // Open the terminal pane; wait for a live prompt.
    await page.keyboard.press('Control+`');
    const termHost = editor.locator('[data-testid^="edit-term-host-"]').locator('visible=true');
    await expect(termHost).toBeVisible();
    await expect.poll(() => termBuffer(termHost), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);

    // Right-click opens the terminal context menu; its Paste rides
    // the wash clipboard into the pty; bash echoes it on the prompt.
    await termHost.click({ button: 'right' });
    await page.locator('[data-testid="term-ctx-paste"]').click();
    await expect.poll(() => termBuffer(termHost)).toContain('edit-clip-4242');
  });

  test('widget paste-import box folds a paste event into the wash clipboard', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.locator('[data-testid="sidebar-section-header-clipboard"]').click();
    const box = page.locator('[data-testid="clipboard-import"]');
    await expect(box).toBeVisible();

    // Synthesize the paste event a real Ctrl+V would deliver — the
    // headless browser has no OS clipboard to drive, so we hand the
    // handler the same ClipboardEvent shape with a DataTransfer.
    await box.evaluate((el) => {
      const dt = new DataTransfer();
      dt.setData('text/plain', 'imported-from-system');
      el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    });

    await expect(page.locator('[data-testid="clipboard-preview"]')).toContainText('imported-from-system');
    await expect(page.locator('[data-testid="clipboard-flash"]')).toContainText('imported');
  });
});
