// wash-term keeps a tab open when its process ends in a way the user
// should read (apps/term/be/app.go shouldHold): a non-zero exit, a
// signal, or an exec'd command that finished within two seconds of
// starting. Before, every exit closed the tab at once and the last tab
// closed the window, so `wash-term --exec` (and wash-sudo --window) output
// that failed fast vanished with the window and the exit code was never
// shown.
//
// wash-term is run as a sibling process (the terminal-attach path) so the
// test can hand it --exec. Both halves: the BE's "tab held … code=N" line
// on the child's stderr (a sibling process is not tee'd into the router
// log), the banner in the xterm buffer, and the window staying up; then
// Enter dismisses it and the window goes.

import { test, expect } from '../fixtures/router';
import type { Locator, Page } from '@playwright/test';
import { spawn } from 'node:child_process';
import type { ChildProcess } from 'node:child_process';
import { existsSync } from 'node:fs';
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

// runTerm starts wash-term --exec ARGS as a sibling process joined to the
// router's session, capturing its stdout/stderr as the BE's log.
function runTerm(appsDir: string, controlSocket: string, argv: string[]): { child: ChildProcess; log: () => string } {
  const termBin = join(appsDir, 'wash-term');
  expect(existsSync(termBin)).toBe(true);
  const child = spawn(termBin, ['--exec', ...argv], {
    env: { ...process.env, WASH_DISPLAY: controlSocket },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let log = '';
  child.stdout!.on('data', (b: Buffer) => { log += b.toString(); });
  child.stderr!.on('data', (b: Buffer) => { log += b.toString(); });
  return { child, log: () => log };
}

async function openDesktop(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
}

test.describe('term exit hold', () => {
  test.setTimeout(60_000);

  test('--exec of a command that exits 3 stays open with the code; Enter closes it', async ({ page, router }) => {
    await openDesktop(page, router.url);
    const { child, log } = runTerm(router.appsDir, router.controlSocket, ['sh', '-c', 'echo boom >&2; exit 3']);
    try {
      const term = page.locator('wash-app-term');
      await expect(term).toBeVisible({ timeout: 10_000 });
      // BE half: the tab was held with the real exit status.
      await expect.poll(log, { timeout: 10_000 }).toMatch(/wash-term tab held ch=\d+ code=3 signal=""/);
      // FE half: the banner is in the terminal, after the command's output.
      const host = page.locator('[data-testid="term-host"]').first();
      await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/process exited with code 3 — press Enter to close/);
      await expect.poll(() => bufferOf(host)).toContain('boom');
      // …and it stays: well past the fast-exit window the window is still up.
      await page.waitForTimeout(2_500);
      await expect(term).toBeVisible();
      await expect(page.locator('[data-testid="term-host"]')).toHaveCount(1);

      // Enter dismisses the held tab; it was the last, so the window goes
      // with it — with no close confirmation (there is no shell to kill).
      await host.click();
      await page.keyboard.press('Enter');
      await expect(term).toHaveCount(0, { timeout: 10_000 });
      await expect(page.locator('[data-testid="term-close-confirm"]')).toHaveCount(0);
      await expect.poll(log, { timeout: 10_000 }).toMatch(/wash-term tab dismissed ch=\d+/);
    } finally {
      child.kill('SIGTERM');
    }
  });

  test('a clean exit within two seconds of spawn is held too; the tab × dismisses it', async ({ page, router }) => {
    await openDesktop(page, router.url);
    const { child, log } = runTerm(router.appsDir, router.controlSocket, ['true']);
    try {
      const term = page.locator('wash-app-term');
      await expect(term).toBeVisible({ timeout: 10_000 });
      await expect.poll(log, { timeout: 10_000 }).toMatch(/wash-term tab held ch=\d+ code=0/);
      const host = page.locator('[data-testid="term-host"]').first();
      await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/process exited with code 0 — press Enter to close/);
      // Closing a held tab does not ask — there is nothing left to end.
      await page.locator('span[data-testid^="term-tab-close-"]').first().click();
      await expect(page.locator('[data-testid="term-close-confirm"]')).toHaveCount(0);
      await expect(term).toHaveCount(0, { timeout: 10_000 });
    } finally {
      child.kill('SIGTERM');
    }
  });

  test('a clean exit after the window closes instantly, as before', async ({ page, router }) => {
    await openDesktop(page, router.url);
    const { child, log } = runTerm(router.appsDir, router.controlSocket, ['sh', '-c', 'sleep 2.5; exit 0']);
    try {
      const term = page.locator('wash-app-term');
      await expect(term).toBeVisible({ timeout: 10_000 });
      await expect(term).toHaveCount(0, { timeout: 15_000 });
      expect(log()).not.toMatch(/tab held/);
      expect(log()).toMatch(/exited cleanly/);
    } finally {
      child.kill('SIGTERM');
    }
  });
});
