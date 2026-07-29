// Agent-aware terminals, M1 (docs/AGENT_TERM.md §9): a coding agent
// running in a wash-term tab paints a state dot on the tab chip and a
// clause in the status line.
//
// No real agent CLI runs in CI. Both detection tiers are driven by
// stand-ins that produce exactly what the real thing produces:
//
//   T1 — a fake agent shell script that printf's the OSC 7770 sequences
//        an installed hook would write to /dev/tty.
//   T0 — an executable literally named `claude` (the foreground poll
//        matches on comm), plus the negative case that decides whether
//        the tier is useful at all: an ordinary program must never be
//        mistaken for an agent.
//
// Assertions are full-stack per the repo pattern: Playwright for the FE
// dot/status line, the router log for the BE's agent_status decisions.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

// The fake agent: one OSC 7770 event per invocation, so the test can step
// through states deterministically instead of racing a script's sleeps.
const FAKE_AGENT = `#!/bin/sh
# Stand-in for a coding agent's hook helper (wash-agent-hook status).
# usage: fake-agent.sh <ev> [reason]
esc=$(printf '\\033')
bel=$(printf '\\007')
if [ -n "$2" ]; then
  printf '%s]7770;v=1;ev=%s;agent=claude;session=e2e-1;reason=%s%s' "$esc" "$1" "$2" "$bel"
else
  printf '%s]7770;v=1;ev=%s;agent=claude;session=e2e-1%s' "$esc" "$1" "$bel"
fi
`;

// A T0 stand-in: /proc/<pid>/comm for a shebang script is the script's
// own name, so an executable called `claude` is indistinguishable from
// the real one to the foreground poll.
const FAKE_CLAUDE_BIN = `#!/bin/sh
sleep 30
`;

async function openTerminal(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
  await expect(page.locator('wash-app-term')).toBeVisible();
  const host = page.locator('[data-testid="term-host"]').first();
  await expect(host).toBeVisible();
  // Wait for a shell prompt before typing at it.
  await expect
    .poll(() => bufferText(page), { timeout: 8_000 })
    .toMatch(/[$#%>][ ]?/);
  await host.click();
  return host;
}

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

async function run(page: Page, cmd: string) {
  await page.keyboard.type(cmd);
  await page.keyboard.press('Enter');
}

test.describe('agent-aware terminal (M1)', () => {
  test.setTimeout(45_000);

  let dir = '';
  test.beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'wash-e2e-agent-'));
    writeFileSync(join(dir, 'fake-agent.sh'), FAKE_AGENT, { mode: 0o755 });
    writeFileSync(join(dir, 'claude'), FAKE_CLAUDE_BIN, { mode: 0o755 });
  });
  test.afterEach(() => {
    if (dir) rmSync(dir, { recursive: true, force: true });
  });

  test('OSC 7770 events drive the tab dot, the status line and the BE log', async ({ page, router }) => {
    await openTerminal(page, router.url);
    const agent = join(dir, 'fake-agent.sh');
    const dot = page.locator('[data-testid^="term-tab-agent-"]');
    const statusAgent = page.locator('[data-testid="term-status-agent"]');

    // Nothing has claimed the tab yet.
    await expect(dot).toHaveCount(0);

    // ev=working → blue dot + "claude working" in the status line.
    await run(page, `sh ${agent} working`);
    await expect(dot).toHaveAttribute('data-agent-state', 'working', { timeout: 10_000 });
    await expect(dot).toHaveAttribute('data-agent', 'claude');
    await expect(statusAgent).toContainText('claude working');
    await router.waitForLog(/term: agent-status ch=\d+ agent=claude state=working session=e2e-1/, 10_000);

    // ev=needs-input → amber, and the reason rides along.
    await run(page, `sh ${agent} needs-input permission`);
    await expect(dot).toHaveAttribute('data-agent-state', 'needs-input', { timeout: 10_000 });
    await expect(statusAgent).toContainText('claude needs input');
    await router.waitForLog(/term: agent-status ch=\d+ agent=claude state=needs-input session=e2e-1 reason=permission/, 10_000);

    // ev=done → green.
    await run(page, `sh ${agent} done`);
    await expect(dot).toHaveAttribute('data-agent-state', 'done', { timeout: 10_000 });
    await expect(statusAgent).toContainText('claude done');

    // ev=end → the tab is a plain terminal again (nothing agent-shaped is
    // in the foreground, so T0 has nothing to fall back to either).
    await run(page, `sh ${agent} end`);
    await expect(dot).toHaveCount(0, { timeout: 10_000 });
    await expect(statusAgent).toHaveCount(0);
  });

  test('the sequence is not stripped from the stream', async ({ page, router }) => {
    // §3: wash parses the OSC but never rewrites the byte stream, so the
    // surrounding output is delivered intact and xterm.js just ignores
    // the unknown id. Bracket the sequence with visible text and check
    // both halves rendered.
    await openTerminal(page, router.url);
    const agent = join(dir, 'fake-agent.sh');
    await run(page, `printf before; sh ${agent} working; printf after`);
    await expect.poll(() => bufferText(page), { timeout: 10_000 }).toContain('beforeafter');
    await expect(page.locator('[data-testid^="term-tab-agent-"]'))
      .toHaveAttribute('data-agent-state', 'working');
  });

  test('T0 spots an agent by name, and does not mistake an ordinary program for one', async ({ page, router }) => {
    await openTerminal(page, router.url);
    const dot = page.locator('[data-testid^="term-tab-agent-"]');

    // The negative case first — this is where a loose agent table would
    // show up as a false positive. `sleep` runs for longer than several
    // poll ticks and must never raise a dot.
    await run(page, 'sleep 4');
    await page.waitForTimeout(2_500);
    await expect(dot).toHaveCount(0);
    await page.keyboard.press('Control+c');

    // The positive: an executable named `claude`, detected with no hooks
    // installed at all. State is "running" — T0 knows presence, not what
    // the agent is doing.
    await run(page, `${join(dir, 'claude')}`);
    await expect(dot).toHaveAttribute('data-agent-state', 'running', { timeout: 10_000 });
    await expect(dot).toHaveAttribute('data-agent', 'claude');
    await router.waitForLog(/term: agent-status ch=\d+ agent=claude state=running/, 10_000);

    // …and it clears when the agent exits.
    await page.keyboard.press('Control+c');
    await expect(dot).toHaveCount(0, { timeout: 10_000 });
    await router.waitForLog(/term: agent-status ch=\d+ agent=none/, 10_000);
  });

  test('per-tab: an agent in one tab leaves the other tab alone', async ({ page, router }) => {
    await openTerminal(page, router.url);
    const agent = join(dir, 'fake-agent.sh');
    await run(page, `sh ${agent} working`);
    await expect(page.locator('[data-testid^="term-tab-agent-"]'))
      .toHaveAttribute('data-agent-state', 'working', { timeout: 10_000 });

    // A second tab has its own pty, its own channel, and no agent.
    await page.locator('[data-testid="term-new-tab"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);
    // Still exactly one dot, and the status line (which follows the
    // active tab) no longer mentions an agent.
    await expect(page.locator('[data-testid^="term-tab-agent-"]')).toHaveCount(1);
    await expect(page.locator('[data-testid="term-status-agent"]')).toHaveCount(0);
  });
});
