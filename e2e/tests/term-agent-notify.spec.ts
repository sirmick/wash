// Agent notifications, M2 (docs/AGENT_TERM.md §5): the two moments in an
// agent's turn that are allowed to interrupt a human, and what clicking
// the interruption does.
//
//   - needs-input → a warn toast, click-to-focus lands on the terminal
//     that raised it, and the window's taskbar pill wears an amber dot
//     until the user visits it (the toast fades; the badge doesn't).
//   - working → done → an info toast with the turn length.
//
// Driven by the same fake agent as term-agent.spec.ts — a shell script
// printf'ing OSC 7770 — so no real agent CLI runs in CI.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const FAKE_AGENT = `#!/bin/sh
# usage: fake-agent.sh <ev> [reason]
esc=$(printf '\\033')
bel=$(printf '\\007')
if [ -n "$2" ]; then
  printf '%s]7770;v=1;ev=%s;agent=claude;session=e2e-1;cwd=%%2Fhome%%2Fmick%%2Fwash;reason=%s%s' "$esc" "$1" "$2" "$bel"
else
  printf '%s]7770;v=1;ev=%s;agent=claude;session=e2e-1;cwd=%%2Fhome%%2Fmick%%2Fwash%s' "$esc" "$1" "$bel"
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

// focusedElement reports which app element currently owns focus, read
// from the shell's own window list — the same source the taskbar uses.
async function focusedElement(page: Page): Promise<string> {
  return await page.evaluate(() => {
    const w = (window as any).wash.windows().find((x: any) => x.focused);
    return w ? String(w.element) : '';
  });
}

async function run(page: Page, cmd: string) {
  await page.keyboard.type(cmd);
  await page.keyboard.press('Enter');
}

test.describe('agent notifications (M2)', () => {
  test.setTimeout(45_000);

  let dir = '';
  let agent = '';
  test.beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'wash-e2e-agent-'));
    agent = join(dir, 'fake-agent.sh');
    writeFileSync(agent, FAKE_AGENT, { mode: 0o755 });
  });
  test.afterEach(() => {
    if (dir) rmSync(dir, { recursive: true, force: true });
  });

  test('needs-input raises a warn toast naming the agent and its repo', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await run(page, `sh ${agent} working`);
    await run(page, `sh ${agent} needs-input permission`);

    const toast = page.locator('[data-testid="notification"][data-level="warn"]');
    await expect(toast).toBeVisible({ timeout: 10_000 });
    await expect(toast.locator('[data-testid="notification-title"]')).toHaveText('Claude needs your input');
    await expect(toast.locator('[data-testid="notification-body"]')).toHaveText('wash · permission request');
    await router.waitForLog(/term: agent-notify ch=\d+ level=warn title="Claude needs your input"/, 10_000);
  });

  test('a finished turn reports how long it took, and only after real work', async ({ page, router }) => {
    await openTerminal(page, router.url);
    // start→done is a session tidying up, not a result: no toast.
    await run(page, `sh ${agent} start`);
    await run(page, `sh ${agent} done`);
    await expect(page.locator('[data-testid="notification"]')).toHaveCount(0);

    // working→done is one the user could have been waiting on.
    await run(page, `sh ${agent} working`);
    await run(page, `sh ${agent} done`);
    const toast = page.locator('[data-testid="notification"][data-level="info"]');
    await expect(toast).toBeVisible({ timeout: 10_000 });
    await expect(toast.locator('[data-testid="notification-title"]')).toContainText(/^Claude finished after \d+s$/);
    await router.waitForLog(/term: agent-notify ch=\d+ level=info title="Claude finished after \d+s"/, 10_000);
  });

  test('clicking the toast focuses the terminal that raised it', async ({ page, router }) => {
    await openTerminal(page, router.url);
    // Arm the event, then move focus elsewhere — the toast has to be able
    // to bring the user BACK to a terminal they have navigated away from.
    await run(page, `sleep 2; sh ${agent} working; sh ${agent} needs-input permission`);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'About wash', exact: true }).click();
    await expect(page.locator('wash-app-about')).toBeVisible();
    await expect.poll(() => focusedElement(page), { timeout: 5_000 }).toBe('wash-app-about');

    const toast = page.locator('[data-testid="notification"][data-level="warn"]');
    await expect(toast).toBeVisible({ timeout: 10_000 });
    await toast.click();
    await expect.poll(() => focusedElement(page), { timeout: 5_000 }).toBe('wash-app-term');
    // The card goes away with the click.
    await expect(toast).toHaveCount(0);
  });

  test('the taskbar pill keeps an amber dot until the window is visited', async ({ page, router }) => {
    await openTerminal(page, router.url);
    const pill = page.locator('[data-testid="taskbar-pill"][data-attention="true"]');
    await expect(pill).toHaveCount(0);

    await run(page, `sh ${agent} working`);
    await run(page, `sh ${agent} needs-input permission`);

    // The toast fades after a few seconds; the badge is what survives it.
    await expect(pill).toHaveCount(1, { timeout: 10_000 });
    await expect(page.locator('[data-testid="taskbar-pill-attention"]')).toBeVisible();

    // Visiting the window is the acknowledgement.
    await pill.click();
    await expect(page.locator('[data-testid="taskbar-pill"][data-attention="true"]')).toHaveCount(0, { timeout: 10_000 });
  });
});
