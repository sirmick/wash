// Agent approval policy, M3 (docs/AGENT_TERM.md §6, §8): the real
// wash-agent-hook binary, running inside a real wash terminal, asking the
// real per-tab socket, answered from a real policy file.
//
// Nothing here is mocked except the agent itself — instead of running
// Claude Code we pipe a PreToolUse payload into the helper by hand, which
// is exactly what Claude Code's hook does. What the helper prints is what
// the agent would act on.
//
// The cases are the three the design turns on: an allow that reaches the
// agent, a deny that also tells the human, and the fail-open silence that
// leaves the agent's own prompt alone.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

// A PreToolUse payload in the shape Claude Code 2.1 sends (common
// envelope: session_id/cwd/permission_mode; PreToolUse adds tool_name,
// tool_input, tool_use_id).
function payload(tool: string, input: Record<string, unknown>, cwd = '/home/mick/wash'): string {
  return JSON.stringify({
    hook_event_name: 'PreToolUse',
    session_id: 'e2e-policy-1',
    cwd,
    permission_mode: 'default',
    tool_name: tool,
    tool_input: input,
    tool_use_id: 'tu-1',
  });
}

// decide runs the real helper against the tab's own socket, the way the
// installed hook does: JSON on stdin, decision on stdout.
function decideCmd(tool: string, input: Record<string, unknown>, cwd?: string): string {
  return `printf '%s' '${payload(tool, input, cwd)}' | wash-agent-hook decide`;
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

async function run(page: Page, cmd: string) {
  await page.keyboard.type(cmd);
  await page.keyboard.press('Enter');
}

// writePolicy drops an agents.json into the router's isolated
// XDG_CONFIG_HOME — the same file the Agents settings pane writes.
function writePolicy(xdgConfigHome: string, policy: unknown) {
  const dir = join(xdgConfigHome, 'wash');
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, 'agents.json'), JSON.stringify(policy, null, 2));
}

test.use({ routerOpts: { xdgConfig: true } });

test.describe('agent approval policy (M3)', () => {
  test.setTimeout(45_000);

  test('an enabled policy answers allow and deny; a denial also tells the human', async ({ page, router }) => {
    writePolicy(router.xdgConfigHome, {
      enabled: true,
      default: 'ask',
      rules: [
        { match: 'Bash(git status:*)', decision: 'allow' },
        { match: 'Bash(rm *)', decision: 'deny' },
      ],
    });
    await openTerminal(page, router.url);

    // The socket is the contract between the terminal and the helper: it
    // must be in the shell's environment before anything else works.
    await run(page, 'echo "sock=${WASH_AGENT_SOCK:-none}"');
    await expect.poll(() => bufferText(page), { timeout: 10_000 }).toMatch(/sock=.*agent-\d+-\d+\.sock/);

    // Allowed by rule → the helper prints Claude Code's allow, naming the
    // rule so the transcript records why.
    await run(page, decideCmd('Bash', { command: 'git status --short' }));
    await expect.poll(() => bufferText(page), { timeout: 10_000 }).toContain('"permissionDecision":"allow"');
    await expect.poll(() => bufferText(page)).toContain('Bash(git status:*)');
    await router.waitForLog(/term: agent-decide ch=\d+ session=e2e-policy-1 tool=Bash decision=allow rule="Bash\(git status:\*\)"/, 10_000);

    // Denied by rule → the agent is told no, and so is the user (an agent
    // that just stops looks broken).
    await run(page, decideCmd('Bash', { command: 'rm -rf /' }));
    await expect.poll(() => bufferText(page), { timeout: 10_000 }).toContain('"permissionDecision":"deny"');
    await router.waitForLog(/term: agent-decide ch=\d+ .*tool=Bash decision=deny rule="Bash\(rm \*\)"/, 10_000);
    const toast = page.locator('[data-testid="notification"][data-level="warn"]');
    await expect(toast).toBeVisible({ timeout: 10_000 });
    await expect(toast.locator('[data-testid="notification-title"]')).toHaveText('Blocked Bash');
  });

  test('an unmatched tool is left to the human — the helper says nothing', async ({ page, router }) => {
    writePolicy(router.xdgConfigHome, {
      enabled: true,
      rules: [{ match: 'Read', decision: 'allow' }],
    });
    await openTerminal(page, router.url);
    // Marker after the pipeline: if the helper printed anything it lands
    // between the command and the marker.
    await run(page, `${decideCmd('Write', { file_path: '/etc/passwd' })}; echo AFTER-WRITE`);
    await expect.poll(() => bufferText(page), { timeout: 10_000 }).toContain('AFTER-WRITE');
    expect(await bufferText(page)).not.toContain('permissionDecision');
  });

  test('with no policy file at all, nothing is ever decided', async ({ page, router }) => {
    // The out-of-the-box state: hooks installed, policy never configured.
    // wash must be invisible to the agent.
    await openTerminal(page, router.url);
    await run(page, `${decideCmd('Bash', { command: 'rm -rf /' })}; echo AFTER-NOPOLICY`);
    await expect.poll(() => bufferText(page), { timeout: 10_000 }).toContain('AFTER-NOPOLICY');
    expect(await bufferText(page)).not.toContain('permissionDecision');
    expect(router.log()).not.toMatch(/term: agent-decide .*decision=(allow|deny)/);
  });

  test('the kill switch beats every rule', async ({ page, router }) => {
    writePolicy(router.xdgConfigHome, {
      enabled: false, // the switch the Agents pane ships in
      default: 'allow',
      rules: [{ match: 'Bash', decision: 'allow' }],
    });
    await openTerminal(page, router.url);
    await run(page, `${decideCmd('Bash', { command: 'rm -rf /' })}; echo AFTER-OFF`);
    await expect.poll(() => bufferText(page), { timeout: 10_000 }).toContain('AFTER-OFF');
    expect(await bufferText(page)).not.toContain('permissionDecision');
  });
});
