// Session resume, M7 (docs/AGENT_TERM.md §13): a roster row disappears
// when its agent ends, but the session it belonged to is remembered — so a
// closed window, a crashed terminal or a reboot costs a click, not the
// context.
//
// The fake agent reports a session id over OSC 7770 exactly as a real
// hook would; then it "ends". The Recent row that survives is what this
// spec is about, and Resume is asserted on what actually runs in the new
// terminal.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';
import { mkdtempSync, writeFileSync, rmSync, existsSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

// The fake agent: reports a session, then ends it. `claude` in this test
// is a stand-in script on PATH, so Resume can be observed running it.
const FAKE_AGENT = `#!/bin/sh
esc=$(printf '\\033')
bel=$(printf '\\007')
cwd=$(pwd | sed 's|/|%2F|g')
printf '%s]7770;v=1;ev=%s;agent=claude;session=%s;cwd=%s%s' "$esc" "$1" "$2" "$cwd" "$bel"
`;

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

async function run(page: Page, cmd: string) {
  await page.locator('[data-testid="term-host"]').first().click();
  await page.keyboard.type(cmd);
  await page.keyboard.press('Enter');
}

// Idempotent: sidebar section state is persisted, so after a reload the
// section may already be open and a blind click would CLOSE it.
async function openAgentsSection(page: Page) {
  const body = page.locator('[data-testid="sidebar-section-body-agents"]');
  if ((await body.count()) === 0) {
    await page.locator('[data-testid="sidebar-section-header-agents"]').click();
  }
  await expect(body).toBeVisible();
}

// agentd persists history to $XDG_STATE_HOME — point it at a throwaway
// dir, or the suite would write into the developer's real
// ~/.local/state/wash and read it back on the next run.
const STATE_DIR = mkdtempSync(join(tmpdir(), 'wash-e2e-agentstate-'));

// Resume runs whatever `claude` the user's PATH resolves — which on a dev
// box is the REAL Claude Code (it ran, and sat waiting at its trust
// prompt, the first time this spec was written). Shadow it with a stand-in
// by putting a directory FIRST on the router's PATH; WASH_BIN_DIR is
// out/, not the per-test apps dir, so dropping a file there is not enough.
const STANDIN_DIR = mkdtempSync(join(tmpdir(), 'wash-e2e-standin-'));

test.use({
  routerOpts: {
    apps: ['session', 'term', 'notify'],
    extraEnv: {
      XDG_STATE_HOME: STATE_DIR,
      PATH: `${STANDIN_DIR}:${process.env.PATH ?? ''}`,
    },
  },
});

test.describe('session resume (M7)', () => {
  test.setTimeout(60_000);

  let dir = '';
  let agent = '';
  let resumeLog = '';

  test.beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'wash-e2e-resume-'));
    agent = join(dir, 'fake-agent.sh');
    writeFileSync(agent, FAKE_AGENT, { mode: 0o755 });
    // A stand-in `claude` that records how it was invoked, so Resume is
    // asserted on the actual argv. It lives in STANDIN_DIR, which is first
    // on the router's PATH (see extraEnv above).
    resumeLog = join(dir, 'resume.log');
    writeFileSync(
      join(STANDIN_DIR, 'claude'),
      `#!/bin/sh\nprintf '%s\\n' "$*" >> ${resumeLog}\npwd >> ${resumeLog}\nsleep 30\n`,
      { mode: 0o755 },
    );
  });
  test.afterEach(() => {
    if (dir) rmSync(dir, { recursive: true, force: true });
  });
  test.afterAll(() => {
    for (const d of [STATE_DIR, STANDIN_DIR]) rmSync(d, { recursive: true, force: true });
  });

  test('a session outlives its agent and can be resumed with one click', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await openAgentsSection(page);

    // An agent runs and reports a session, then ends.
    await run(page, `cd ${dir} && sh ${agent} working sess-42`);
    await expect(page.locator('[data-testid^="agents-row-"]')).toHaveCount(1, { timeout: 15_000 });
    // While it's live, Recent doesn't offer to resume what's already here.
    await expect(page.locator('[data-testid="agents-recent-row"][data-session-id="sess-42"]')).toHaveCount(0);

    await run(page, `sh ${agent} end sess-42`);
    await expect(page.locator('[data-testid^="agents-row-"]')).toHaveCount(0, { timeout: 15_000 });

    // …and the session is now in Recent.
    const recent = page.locator('[data-testid="agents-recent-row"][data-session-id="sess-42"]');
    await expect(recent).toHaveCount(1, { timeout: 15_000 });
    await expect(recent).toContainText('claude');
    await expect(recent).toContainText(dir.split('/').pop()!);

    // Resume opens a new terminal running the agent, in the right tree.
    await recent.locator('[data-testid="agents-resume"]').click();
    await expect(page.locator('wash-app-term')).toHaveCount(2, { timeout: 15_000 });
    await router.waitForLog(/agentd: resume session=sess-42 agent=claude fork=false/, 15_000);

    await expect
      .poll(() => (existsSync(resumeLog) ? readFileSync(resumeLog, 'utf8') : ''), { timeout: 20_000 })
      .toContain('--resume sess-42');

    // …and it ran where the session was working.
    expect(readFileSync(resumeLog, 'utf8')).toContain(dir);
  });

  test('Fork branches off the session instead of continuing it', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await openAgentsSection(page);
    await run(page, `cd ${dir} && sh ${agent} working sess-77`);
    await expect(page.locator('[data-testid^="agents-row-"]')).toHaveCount(1, { timeout: 15_000 });
    await run(page, `sh ${agent} end sess-77`);

    const recent = page.locator('[data-testid="agents-recent-row"][data-session-id="sess-77"]');
    await expect(recent).toHaveCount(1, { timeout: 15_000 });
    await recent.locator('[data-testid="agents-fork"]').click();
    await router.waitForLog(/agentd: resume session=sess-77 agent=claude fork=true/, 15_000);
    await expect
      .poll(() => (existsSync(resumeLog) ? readFileSync(resumeLog, 'utf8') : ''), { timeout: 20_000 })
      .toContain('--fork-session');
  });

  test('history survives the service dying — that is the point of it', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await openAgentsSection(page);
    await run(page, `cd ${dir} && sh ${agent} working sess-99`);
    await expect(page.locator('[data-testid^="agents-row-"]')).toHaveCount(1, { timeout: 15_000 });
    await run(page, `sh ${agent} end sess-99`);
    await expect(page.locator('[data-testid="agents-recent-row"][data-session-id="sess-99"]'))
      .toHaveCount(1, { timeout: 15_000 });

    // Kill agentd; the router restarts it on the next reference. The
    // reloaded service must still know the session.
    await run(page, 'pkill -f wash-agentd || true');
    await page.waitForTimeout(1_000);
    await page.reload();
    await expect(page.locator('wash-app-session')).toBeVisible();
    await openAgentsSection(page);
    await expect(page.locator('[data-testid="agents-recent-row"][data-session-id="sess-99"]'))
      .toHaveCount(1, { timeout: 20_000 });
  });
});
