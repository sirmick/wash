// ACP's filesystem capability, end to end (apps/agentd/be/acpfs.go).
//
// wash now advertises fs.readTextFile / fs.writeTextFile, so an agent asks
// US for files instead of opening them itself. That is worth an e2e rather
// than only unit tests, because the interesting part is the whole chain: the
// adapter's JSON-RPC request → agentd's confined handler → the reply the
// agent then reports in its transcript.
//
// The fake adapter (e2e/fixtures/acp-fake) grew two keywords for this —
// `readfile <abs path>` and `writefile <abs path> <text>` — and reports what
// came back as READ<<…>> / WROTE<<…>>, so a spec can tell "wash served it"
// from "wash refused" without reading the wire.
//
// The refusal case matters most: an agent is not trusted to stay inside the
// folder it was given, it is HELD there, and the only way to know that still
// holds is to have an agent genuinely try to leave.

import { fileURLToPath } from 'node:url';
import { mkdtempSync, writeFileSync, readFileSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test.use({
  routerOpts: {
    apps: ['session', 'agentd', 'ai', 'notify'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

// startAgentIn opens an Agent window, points it at `dir` through the
// launcher's folder picker, and starts the fake adapter there. The cwd is
// the sandbox boundary, so a spec about that boundary has to set it
// explicitly rather than inherit $HOME.
async function startAgentIn(page: Page, url: string, dir: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agent', exact: true }).click();
  const win = page.locator('wash-app-ai').first();
  await expect(win).toBeVisible();

  await win.getByRole('button', { name: 'Choose…' }).click();
  const picker = page.locator('[data-testid="ai-folder-picker"]');
  await expect(picker).toBeVisible();
  const bar = picker.locator('[data-testid="fp-path"]');
  await bar.click();
  await bar.fill(dir);
  await bar.press('Enter');
  await picker.locator('[data-testid="fp-confirm"]').click();
  await expect(picker).toBeHidden();

  await win.locator('select').first().selectOption('codex');
  await win.getByRole('button', { name: 'Start session' }).click();
  const composer = win.locator('textarea');
  await expect(composer).toBeVisible({ timeout: 20_000 });
  return win;
}

async function ask(win: ReturnType<Page['locator']>, page: Page, text: string) {
  const composer = win.locator('textarea');
  await composer.fill(text);
  await composer.press('Enter');
}

test.describe('agent terminal capability', () => {
  test.setTimeout(60_000);

  // The agent hands wash the command instead of forking it, then blocks on
  // wait_for_exit and reads the output AFTER exit — the sequence that only
  // works if a terminal outlives its process.
  test('the agent runs a command through wash and reads its result', async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-agentterm-'));
    writeFileSync(join(dir, 'marker.txt'), 'x');
    const win = await startAgentIn(page, router.url, dir);

    await ask(win, page, 'runcmd echo hello-from-wash-terminal; exit 7');
    await expect(win).toContainText('RAN<<id=', { timeout: 30_000 });
    // The output came back after the process was gone, with its status.
    await expect(win).toContainText('exit=7', { timeout: 10_000 });
    await expect(win).toContainText('hello-from-wash-terminal', { timeout: 10_000 });
  });

  // The transcript mounts a LIVE terminal on the pty's channel — the point
  // of the capability is watching the command, not reading about it.
  test('the transcript shows the command running, live', async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-agentterm-live-'));
    const win = await startAgentIn(page, router.url, dir);

    // Long enough that the assertions below land WHILE it is still running.
    await ask(win, page, 'runcmd echo watch-me-live; sleep 8');

    const term = win.locator('[data-testid="agent-terminal"]');
    await expect(term).toBeVisible({ timeout: 30_000 });
    // The command line is named, and the pty's own output is on screen
    // before the process has exited.
    await expect(term).toContainText('watch-me-live', { timeout: 20_000 });
    // It is a real terminal on the pty's channel, not a text blob.
    await expect(term.locator('.xterm-rows')).toBeVisible();
  });

  // The command runs in the session's folder, not agentd's — the cd happens
  // inside the child, so this is the assertion that the wrapper works.
  test('the command runs in the session folder', async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-agentterm-cwd-'));
    writeFileSync(join(dir, 'only-here.txt'), 'x');
    const win = await startAgentIn(page, router.url, dir);

    await ask(win, page, 'runcmd ls');
    await expect(win).toContainText('only-here.txt', { timeout: 30_000 });
  });
});

test.describe('agent filesystem capability', () => {
  test.setTimeout(60_000);

  test('the agent reads a file through wash, and is refused outside its folder', async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-agentfs-'));
    writeFileSync(join(dir, 'notes.md'), 'the-secret-inside\n');
    // A file the agent must NOT reach: a sibling of its folder, not a child.
    const outside = mkdtempSync(join(tmpdir(), 'wash-agentfs-out-'));
    writeFileSync(join(outside, 'private.txt'), 'do-not-read-me\n');

    const win = await startAgentIn(page, router.url, dir);

    await ask(win, page, `readfile ${join(dir, 'notes.md')}`);
    await expect(win).toContainText('READ<<the-secret-inside', { timeout: 30_000 });

    // Same session, a path outside the folder it was started in.
    await ask(win, page, `readfile ${join(outside, 'private.txt')}`);
    await expect(win).toContainText('READ<<REFUSED', { timeout: 30_000 });
    // And the content never appeared, in any form.
    await expect(win).not.toContainText('do-not-read-me');
  });

  test('the agent writes through wash, and cannot write outside its folder', async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-agentfs-w-'));
    const outside = mkdtempSync(join(tmpdir(), 'wash-agentfs-wout-'));

    const win = await startAgentIn(page, router.url, dir);

    await ask(win, page, `writefile ${join(dir, 'made.txt')} hello-from-the-agent`);
    await expect(win).toContainText('WROTE<<OK>>', { timeout: 30_000 });
    // The proof is on disk, not in the transcript.
    expect(readFileSync(join(dir, 'made.txt'), 'utf8')).toContain('hello-from-the-agent');

    await ask(win, page, `writefile ${join(outside, 'escaped.txt')} should-not-exist`);
    await expect(win).toContainText('WROTE<<REFUSED', { timeout: 30_000 });
    expect(existsSync(join(outside, 'escaped.txt'))).toBe(false);
  });
});
