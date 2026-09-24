// wash-term split intents are keyed to the request that made them
// (apps/term/fe/src/intents.ts). Before, a split recorded its placement in
// a FIFO that only a successful tab_opened shifted — so when the pty for a
// split FAILED to spawn (tab_error), the intent stayed queued and the next
// plain New Tab landed as a split the user never asked for.
//
// The failure is forced from outside the product: wash-term runs as a
// sibling process (the terminal-attach path) with $SHELL pointing at a
// script this test owns, and the script is made non-executable for the
// split and executable again for the New Tab. Both halves are asserted:
// the BE logs the failed fork/exec, and the layout the FE ends up with is
// one strip holding two tabs, not two panes. The BE's log is read off the
// child's own stderr — a sibling process is not tee'd into the router log.

import { test, expect } from '../fixtures/router';
import type { Locator } from '@playwright/test';
import { spawn } from 'node:child_process';
import { chmodSync, existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
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

async function waitForPrompt(host: Locator) {
  await expect(host).toBeVisible();
  await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/[$#%>][ ]?/);
  await host.click();
}

test.describe('term split intent on a failed spawn', () => {
  test.setTimeout(60_000);

  test('a split whose pty fails does not turn the next New Tab into a split', async ({ page, router }) => {
    // A shell wrapper the test can switch off and on. exec keeps bash as
    // the pty's process, so the prompt and `stty` behave as usual.
    const dir = mkdtempSync(join(tmpdir(), 'wash-term-shell-'));
    const shell = join(dir, 'shell.sh');
    writeFileSync(shell, '#!/bin/sh\nexec /bin/bash "$@"\n');
    chmodSync(shell, 0o755);

    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    const termBin = join(router.appsDir, 'wash-term');
    expect(existsSync(termBin)).toBe(true);
    const child = spawn(termBin, [], {
      env: { ...process.env, WASH_DISPLAY: router.controlSocket, SHELL: shell },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let beLog = '';
    child.stdout.on('data', (b: Buffer) => { beLog += b.toString(); });
    child.stderr.on('data', (b: Buffer) => { beLog += b.toString(); });
    try {
      await expect(page.locator('wash-app-term')).toBeVisible({ timeout: 10_000 });
      const first = page.locator('[data-testid="term-host"]').first();
      await waitForPrompt(first);
      await expect(page.locator('[data-testid="term-tabbar"]')).toHaveCount(1);

      // The split's pty cannot fork: exec of a mode-000 script fails.
      chmodSync(shell, 0o000);
      await first.click();
      await page.keyboard.press('Control+Shift+D');
      // BE half: the spawn failed and said so.
      await expect.poll(() => beLog, { timeout: 10_000 }).toMatch(/wash-term open: .*permission denied/);
      // FE half: the error landed in the pane, and nothing was split.
      await expect.poll(() => bufferOf(first), { timeout: 10_000 }).toMatch(/wash-term: .*permission denied/);
      await expect(page.locator('[data-testid="term-host"]')).toHaveCount(1);
      await expect(page.locator('[data-testid="term-tabbar"]')).toHaveCount(1);

      // Shell back; a plain New Tab must be a tab in the SAME strip, not
      // the split that failed a moment ago.
      chmodSync(shell, 0o755);
      await page.locator('[data-testid="term-new-tab"]').click();
      await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);
      await expect(page.locator('[data-testid="term-tabbar"]')).toHaveCount(1);
      await expect(page.locator('[data-testid="term-divider"]')).toHaveCount(0);
      await expect(page.locator('[data-testid="term-tabbar"] button[data-testid^="term-tab-"]')).toHaveCount(2);
      // …and the new tab is live: it reached a prompt in its pty.
      const second = page.locator('[data-testid="term-host"]:visible').first();
      await waitForPrompt(second);
    } finally {
      child.kill('SIGTERM');
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
