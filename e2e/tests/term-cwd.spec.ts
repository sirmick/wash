// Working directory in wash-term (docs/Review-findings.md P2 → term "cwd"):
//
//   - `wash-term --open <dir>` starts its first shell in <dir> — the argv
//     the router's open-routing injects (sdk LaunchOpenPath), here exercised
//     the terminal-attach way (an app run outside the router dials
//     WASH_DISPLAY), which is the same `--open` parse;
//   - a New Tab (and a split) inherits the FOCUSED tab's cwd: what the shell
//     reported through OSC 7 when it did, else /proc/<shell pid>/cwd.
//
// Both halves each time: `pwd` inside the pane (FE) and the BE's
// "tab opened … cwd=…" line (or, for the externally-run binary, its own
// stderr) naming the directory the pty was CREATED in.

import { test, expect } from '../fixtures/router';
import type { Locator, Page } from '@playwright/test';
import { spawn } from 'node:child_process';
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

async function waitPrompt(host: Locator) {
  await expect(host).toBeVisible();
  await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/[$#%>][ ]?/);
}

// pwdOf runs a tagged `pwd` in the pane so the poll cannot match an older
// probe (docs/FLAKE_LOG.md stale-match failure mode).
let seq = 0;
async function pwdOf(page: Page, host: Locator): Promise<string> {
  const tag = `PWD${++seq}`;
  await host.click();
  await page.keyboard.type(`echo ${tag}=$(pwd)`);
  await page.keyboard.press('Enter');
  let got = '';
  await expect.poll(async () => {
    const m = (await bufferOf(host)).match(new RegExp(`^${tag}=(\\S+)$`, 'm'));
    got = m ? m[1] : '';
    return got;
  }, { timeout: 10_000 }).not.toBe('');
  return got;
}

async function openTerminal(page: Page, url: string): Promise<Locator> {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
  await expect(page.locator('wash-app-term')).toBeVisible();
  const host = page.locator('[data-testid="term-host"]').first();
  await waitPrompt(host);
  return host;
}

test.describe('term cwd', () => {
  test.setTimeout(60_000);

  test('`wash-term --open <dir>` starts its first shell in that directory', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    const dir = realpathSync(mkdtempSync(join(tmpdir(), 'wash-term-open-')));
    const termBin = join(router.appsDir, 'wash-term');
    let stderr = '';
    const child = spawn(termBin, ['--open', dir], {
      env: { ...process.env, WASH_DISPLAY: router.controlSocket },
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    child.stderr.on('data', (b: Buffer) => { stderr += b.toString('utf8'); });
    try {
      await expect(page.locator('wash-app-term')).toBeVisible({ timeout: 15_000 });
      const host = page.locator('[data-testid="term-host"]').first();
      await waitPrompt(host);
      expect(await pwdOf(page, host)).toBe(dir);
      // BE half: the pty was created there, not cd'd into afterwards.
      await expect.poll(() => stderr, { timeout: 5_000 }).toMatch(/wash-term: open dir=/);
      expect(stderr).toContain(`cwd="${dir}"`);
    } finally {
      child.kill('SIGTERM');
    }
  });

  test('a bad --open path is ignored, not fatal', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const termBin = join(router.appsDir, 'wash-term');
    let stderr = '';
    const child = spawn(termBin, ['--open', '/definitely/not/here'], {
      env: { ...process.env, WASH_DISPLAY: router.controlSocket },
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    child.stderr.on('data', (b: Buffer) => { stderr += b.toString('utf8'); });
    try {
      await expect(page.locator('wash-app-term')).toBeVisible({ timeout: 15_000 });
      const host = page.locator('[data-testid="term-host"]').first();
      await waitPrompt(host);
      expect(await pwdOf(page, host)).not.toBe('/definitely/not/here');
      await expect.poll(() => stderr, { timeout: 5_000 }).toMatch(/--open "\/definitely\/not\/here" is not a directory \(ignored\)/);
    } finally {
      child.kill('SIGTERM');
    }
  });

  test('New Tab inherits the focused tab\'s cwd (via /proc when the shell is silent)', async ({ page, router }) => {
    const first = await openTerminal(page, router.url);
    await first.click();
    await page.keyboard.type('cd /tmp');
    await page.keyboard.press('Enter');
    expect(await pwdOf(page, first)).toBe('/tmp');

    const cursor = router.logCursor();
    await page.locator('[data-testid="term-new-tab"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);
    // BE half: the second pty was CREATED in /tmp.
    await router.waitForLog(/wash-term tab opened ch=\d+ .*cwd="\/tmp"/, 10_000, cursor);
    const second = page.locator('[data-testid="term-host"]:visible').first();
    await waitPrompt(second);
    expect(await pwdOf(page, second)).toBe('/tmp');
  });

  test('an OSC 7 report beats /proc for the inherited cwd', async ({ page, router }) => {
    const first = await openTerminal(page, router.url);
    await first.click();
    // The shell stays in its own cwd but REPORTS /usr — what a prompt hook
    // does on every prompt. The FE forwards it (tab_cwd) and the next tab
    // starts there.
    const reported = router.logCursor();
    await page.keyboard.type("printf '\\033]7;file://localhost/usr\\a'");
    await page.keyboard.press('Enter');
    // The report is asynchronous (FE → BE); wait for the BE to have it
    // before asking for a tab that should inherit it.
    await router.waitForLog(/wash-term tab cwd ch=\d+ cwd="\/usr"/, 10_000, reported);

    const cursor = router.logCursor();
    // Split — the same inheritance path as New Tab, through the split
    // intent, which is why it is the second gesture tested here.
    await first.click();
    await page.keyboard.press('Control+Shift+D');
    await expect(page.locator('[data-testid="term-host"]:visible')).toHaveCount(2);
    await router.waitForLog(/wash-term tab opened ch=\d+ .*cwd="\/usr"/, 10_000, cursor);
    const right = page.locator('[data-testid="term-host"]:visible').last();
    await waitPrompt(right);
    expect(await pwdOf(page, right)).toBe('/usr');
  });
});
