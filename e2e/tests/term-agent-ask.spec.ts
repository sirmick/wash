// Answering from the desktop, M6 (docs/AGENT_TERM.md §12).
//
// Full chain, real binaries: the real wash-agent-hook asks the real per-tab
// socket → the terminal's policy has no answer → agentd puts the question
// in the roster → the sidebar renders it → a click travels back and the
// helper prints the decision the human chose.
//
// The assertion that matters is the helper's stdout, because that is what
// the agent acts on.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

function payload(tool: string, input: Record<string, unknown>): string {
  return JSON.stringify({
    hook_event_name: 'PreToolUse',
    session_id: 'e2e-ask-1',
    cwd: '/home/mick/wash',
    permission_mode: 'default',
    tool_name: tool,
    tool_input: input,
    tool_use_id: 'tu-1',
  });
}

// The command a hook would run, with its answer captured to a file so the
// test can read exactly what the agent would have received.
//
// It goes in a SCRIPT rather than being typed: a 250-character line of
// JSON is a lot of keystrokes to inject into a pty, and a single dropped
// one turns into a mysterious timeout further down the test.
function decideScript(dir: string, name: string, tool: string, input: Record<string, unknown>, out: string): string {
  const path = join(dir, `${name}.sh`);
  writeFileSync(
    path,
    `#!/bin/sh\nprintf '%s' '${payload(tool, input)}' | wash-agent-hook decide > ${out}\necho DECIDED\n`,
    { mode: 0o755 },
  );
  return `sh ${path}`;
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

// Idempotent: sidebar section state is persisted, so after a reload the
// section may already be open and a blind click would CLOSE it.
async function openAgentsSection(page: Page) {
  const body = page.locator('[data-testid="sidebar-section-body-agents"]');
  if ((await body.count()) === 0) {
    await page.locator('[data-testid="sidebar-section-header-agents"]').click();
  }
  await expect(body).toBeVisible();
}

function writePolicy(xdgConfigHome: string, policy: unknown) {
  const dir = join(xdgConfigHome, 'wash');
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, 'agents.json'), JSON.stringify(policy, null, 2));
}

function readPolicy(xdgConfigHome: string): any {
  const p = join(xdgConfigHome, 'wash', 'agents.json');
  return existsSync(p) ? JSON.parse(readFileSync(p, 'utf8')) : null;
}

function decided(path: string): string {
  return existsSync(path) ? readFileSync(path, 'utf8') : '';
}

test.use({ routerOpts: { xdgConfig: true } });

test.describe('answering from the desktop (M6)', () => {
  test.setTimeout(60_000);

  let scriptDir = '';
  test.beforeEach(() => {
    scriptDir = mkdtempSync(join(tmpdir(), 'wash-e2e-ask-'));
  });

  test('an unmatched request appears in the sidebar and Allow reaches the agent', async ({ page, router }) => {
    // Policy on, but with nothing that covers Bash.
    writePolicy(router.xdgConfigHome, { enabled: true, rules: [{ match: 'Read', decision: 'allow' }] });
    await openTerminal(page, router.url);
    await openAgentsSection(page);

    const out = join(router.appsDir, 'decision.json');
    await run(page, decideScript(scriptDir, 'allow', 'Bash', { command: 'git push origin main' }, out));

    const ask = page.locator('[data-testid="agents-ask"]');
    await expect(ask).toBeVisible({ timeout: 15_000 });
    await expect(ask).toContainText('wants to run');
    await expect(ask.locator('[data-testid="agents-ask-what"]')).toContainText('git push origin main');
    // The rule it would write is named on the button, not hidden.
    await expect(ask.locator('[data-testid="agents-ask-always"]')).toContainText('Bash(git push*)');
    await router.waitForLog(/agentd: ask row=\S+ agent=\S+ tool=Bash/, 15_000);

    await ask.locator('[data-testid="agents-ask-allow"]').click();
    await expect(ask).toHaveCount(0, { timeout: 10_000 });

    // What the agent actually receives.
    await expect.poll(() => bufferText(page), { timeout: 15_000 }).toContain('DECIDED');
    expect(decided(out)).toContain('"permissionDecision":"allow"');
    await router.waitForLog(/agentd: answer id=\S+ tool=Bash decision=allow remember=false/, 10_000);
    // A one-off Allow teaches nothing.
    expect(readPolicy(router.xdgConfigHome).rules).toHaveLength(1);
  });

  test('“Always allow” writes the rule it named, and the next request needs no human', async ({ page, router }) => {
    writePolicy(router.xdgConfigHome, { enabled: true, rules: [] });
    await openTerminal(page, router.url);
    await openAgentsSection(page);

    const first = join(router.appsDir, 'first.json');
    await run(page, decideScript(scriptDir, 'first', 'Bash', { command: 'git status --short' }, first));
    const ask = page.locator('[data-testid="agents-ask"]');
    await expect(ask).toBeVisible({ timeout: 15_000 });
    await ask.locator('[data-testid="agents-ask-always"]').click();
    await expect(ask).toHaveCount(0, { timeout: 10_000 });
    await expect.poll(() => decided(first), { timeout: 15_000 }).toContain('"permissionDecision":"allow"');

    // The rule landed in the policy file…
    await expect
      .poll(() => (readPolicy(router.xdgConfigHome)?.rules ?? []).map((r: any) => r.match), { timeout: 10_000 })
      .toContain('Bash(git status*)');
    await router.waitForLog(/agentd: remembered rule="Bash\(git status\*\)" decision=allow/, 10_000);

    // …and the SAME request is now answered with no sidebar round-trip.
    const second = join(router.appsDir, 'second.json');
    await run(page, decideScript(scriptDir, 'second', 'Bash', { command: 'git status --porcelain' }, second));
    await expect.poll(() => decided(second), { timeout: 15_000 }).toContain('"permissionDecision":"allow"');
    await expect(page.locator('[data-testid="agents-ask"]')).toHaveCount(0);
    await router.waitForLog(/term: agent-decide .*tool=Bash decision=allow rule="Bash\(git status\*\)"/, 10_000);
  });

  test('Deny reaches the agent as a deny', async ({ page, router }) => {
    writePolicy(router.xdgConfigHome, { enabled: true, rules: [] });
    await openTerminal(page, router.url);
    await openAgentsSection(page);

    const out = join(router.appsDir, 'deny.json');
    await run(page, decideScript(scriptDir, 'deny', 'Bash', { command: 'rm -rf /' }, out));
    const ask = page.locator('[data-testid="agents-ask"]');
    await expect(ask).toBeVisible({ timeout: 15_000 });
    await ask.locator('[data-testid="agents-ask-deny"]').click();
    await expect.poll(() => decided(out), { timeout: 15_000 }).toContain('"permissionDecision":"deny"');
  });

  test('with the policy off, nothing is ever asked', async ({ page, router }) => {
    // The out-of-the-box state: M6 must be invisible until the policy is on.
    await openTerminal(page, router.url);
    await openAgentsSection(page);
    const out = join(router.appsDir, 'off.json');
    await run(page, decideScript(scriptDir, 'off', 'Bash', { command: 'rm -rf /' }, out));
    await expect.poll(() => bufferText(page), { timeout: 15_000 }).toContain('DECIDED');
    expect(decided(out)).toBe('');
    await expect(page.locator('[data-testid="agents-ask"]')).toHaveCount(0);
  });

  test('ask_desktop:false keeps M3 behaviour with the policy on', async ({ page, router }) => {
    writePolicy(router.xdgConfigHome, { enabled: true, ask_desktop: false, rules: [] });
    await openTerminal(page, router.url);
    await openAgentsSection(page);
    const out = join(router.appsDir, 'noask.json');
    await run(page, decideScript(scriptDir, 'noask', 'Bash', { command: 'git push' }, out));
    await expect.poll(() => bufferText(page), { timeout: 15_000 }).toContain('DECIDED');
    // Deferred silently — the agent's own prompt would appear.
    expect(decided(out)).toBe('');
    await expect(page.locator('[data-testid="agents-ask"]')).toHaveCount(0);
  });
});
