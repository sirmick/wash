// Agent roster, M4 (docs/AGENT_TERM.md §7): the sidebar answers "what are
// my agents doing?" for the whole desktop, not one tab chip at a time.
//
// Full stack, no mocks but the agent itself: a fake agent script printf's
// OSC 7770 in a real terminal → the term BE publishes to com.wash.agentd
// (spawned on first reference) → the session BE gateway forwards its
// StateService snapshot → the sidebar widget renders a row → clicking the
// row focuses the terminal that owns it.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const FAKE_AGENT = `#!/bin/sh
# usage: fake-agent.sh <ev> [reason]
esc=$(printf '\\033')
bel=$(printf '\\007')
cwd=$(pwd | sed 's|/|%2F|g')
if [ -n "$2" ]; then
  printf '%s]7770;v=1;ev=%s;agent=claude;session=e2e-roster;cwd=%s;reason=%s%s' "$esc" "$1" "$cwd" "$2" "$bel"
else
  printf '%s]7770;v=1;ev=%s;agent=claude;session=e2e-roster;cwd=%s%s' "$esc" "$1" "$cwd" "$bel"
fi
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

// run types into the TERMINAL. The click matters: expanding a sidebar
// section moves keyboard focus out of the pty, so without re-focusing the
// keystrokes land in the chrome instead. The echo assertion turns a
// dropped keystroke into an immediate, legible failure rather than a 15s
// timeout waiting for the effect of a command that never ran.
async function run(page: Page, cmd: string) {
  await page.locator('[data-testid="term-host"]').first().click();
  await page.keyboard.type(cmd);
  await expect.poll(() => bufferText(page), { timeout: 10_000 }).toContain(cmd.slice(0, 20));
  await page.keyboard.press('Enter');
}

// The Agents section starts collapsed; open it to render rows.
// Idempotent: sidebar section state is persisted, so after a reload the
// section may already be open and a blind click would CLOSE it.
async function openAgentsSection(page: Page) {
  const body = page.locator('[data-testid="sidebar-section-body-agents"]');
  if ((await body.count()) === 0) {
    await page.locator('[data-testid="sidebar-section-header-agents"]').click();
  }
  await expect(body).toBeVisible();
}

async function focusedElement(page: Page): Promise<string> {
  return await page.evaluate(() => {
    const w = (window as any).wash.windows().find((x: any) => x.focused);
    return w ? String(w.element) : '';
  });
}

// wash-agentd is staged in every router by the fixture (the router spawns
// it on first reference), so it isn't in the apps list.
test.use({ routerOpts: { apps: ['session', 'term', 'about', 'notify'] } });

test.describe('agent roster (M4)', () => {
  test.setTimeout(45_000);

  let dir = '';
  let agent = '';
  test.beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'wash-e2e-roster-'));
    agent = join(dir, 'fake-agent.sh');
    writeFileSync(agent, FAKE_AGENT, { mode: 0o755 });
  });
  test.afterEach(() => {
    if (dir) rmSync(dir, { recursive: true, force: true });
  });

  test('an agent in a terminal appears in the sidebar, states its dir, and clears when it ends', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await openAgentsSection(page);
    await expect(page.locator('[data-testid="agents-empty"]')).toBeVisible();

    // Report from a known directory so the row's dir label is assertable.
    await run(page, `cd ${dir} && sh ${agent} working`);

    const row = page.locator('[data-testid^="agents-row-"]');
    await expect(row).toHaveCount(1, { timeout: 15_000 });
    await expect(row).toHaveAttribute('data-agent', 'claude');
    await expect(row).toHaveAttribute('data-agent-state', 'working');
    await expect(row).toContainText(dir.split('/').pop()!);
    await router.waitForLog(/wash-agentd ready/, 15_000);

    // needs-input floats to the top of the roster and badges the header.
    await run(page, `sh ${agent} needs-input permission`);
    await expect(row).toHaveAttribute('data-agent-state', 'needs-input', { timeout: 15_000 });
    await expect(row.locator('[data-testid="agents-state"]')).toContainText('needs input · permission');

    // ev=end retracts the row — the roster is live, not a log.
    await run(page, `sh ${agent} end`);
    await expect(page.locator('[data-testid^="agents-row-"]')).toHaveCount(0, { timeout: 15_000 });
    await expect(page.locator('[data-testid="agents-empty"]')).toBeVisible();
  });

  test('clicking a row goes to the terminal that owns the agent', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await openAgentsSection(page);
    await run(page, `cd ${dir} && sh ${agent} working`);
    await expect(page.locator('[data-testid^="agents-row-"]')).toHaveCount(1, { timeout: 15_000 });

    // Move focus away, then use the roster to get back.
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'About wash', exact: true }).click();
    await expect(page.locator('wash-app-about')).toBeVisible();
    await expect.poll(() => focusedElement(page), { timeout: 5_000 }).toBe('wash-app-about');

    await page.locator('[data-testid^="agents-row-"]').first().click();
    await expect.poll(() => focusedElement(page), { timeout: 5_000 }).toBe('wash-app-term');
  });

  test('closing the tab retracts its row immediately', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await openAgentsSection(page);
    await run(page, `cd ${dir} && sh ${agent} working`);
    await expect(page.locator('[data-testid^="agents-row-"]')).toHaveCount(1, { timeout: 15_000 });

    // A second tab so closing the first doesn't take the window with it.
    await page.locator('[data-testid="term-new-tab"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);
    const closeButtons = page.locator('span[data-testid^="term-tab-close-"]');
    await closeButtons.first().click();

    // Not waiting for the 60s stale sweep: the terminal says goodbye.
    await expect(page.locator('[data-testid^="agents-row-"]')).toHaveCount(0, { timeout: 15_000 });
  });
});
