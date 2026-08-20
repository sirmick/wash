// The payoff (docs/SIDEBAR.md M2c): launchOn(B, com.wash.ai) gets B's
// roster, with working verbs, and NO new addressing.
//
// This is what the whole plan is for. Before it, the rail's agent verbs
// gatewayed through the session BE, whose SendAppMsgTo resolves inside
// its own router — so an agent on B had no control surface at all, and
// the rail silently showed A's instead (§1.2). Expressing the roster as
// an app made cross-host addressing something wash already had.
//
// Two routers stand in for host A (the desktop) and host B (what an
// `ssh -L` tunnel would reach), wired with ?peer= as remote-apps.spec.ts
// does. B runs --no-session, which is exactly why a session-BE gateway
// could never have served it.

import { fileURLToPath } from 'node:url';
import { test, expect } from '@playwright/test';
import { startRouter, stopRouter, type RouterHandle } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test('the Agent app opened on a remote host shows THAT host\'s sessions', async ({ page }) => {
  let a: RouterHandle | undefined;
  let b: RouterHandle | undefined;
  try {
    const env = { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` };
    a = await startRouter({ apps: ['session', 'agentd', 'ai', 'notify'], extraEnv: env });
    b = await startRouter({
      apps: ['agentd', 'ai', 'notify'],
      extraArgs: ['--no-session', '--allow-cross-origin'],
      extraEnv: env,
    });

    // A session on each host, with different prompts so a mix-up is
    // visible rather than merely suspected.
    const aWin = await a.controlRequest({ t: 'launch', app_id: 'com.wash.ai' });
    const bWin = await b.controlRequest({ t: 'launch', app_id: 'com.wash.ai' });
    await a.controlRequest({
      t: 'msg', instance_id: String(aWin.instance_id),
      data: { kind: 'start', agent: 'codex', cwd: '', prompt: 'work happening on A' },
    });
    await b.controlRequest({
      t: 'msg', instance_id: String(bWin.instance_id),
      data: { kind: 'start', agent: 'codex', cwd: '', prompt: 'work happening on B' },
    });
    await a.waitForLog(/agentd: acp session started/, 20_000);
    await b.waitForLog(/agentd: acp session started/, 20_000);

    const bPort = new URL(b.url).port;
    await page.goto(`${a.url}?peer=${encodeURIComponent(`remoteB@ws://127.0.0.1:${bPort}/ws`)}`);
    await expect(page.locator('[data-testid="wash-cam"]')).toBeAttached({ timeout: 10_000 });

    // The rail knows B has an agent — that is M1's awareness channel. The
    // section carries the count while collapsed; open it for the door.
    // (It opens itself only when someone is WAITING on you; an agent
    // merely working is not an interruption, by design.)
    await page.locator('[data-testid="sidebar-section-header-agents"]').click();
    await expect(page.locator('[data-testid="sidebar-section-body-agents"]')).toBeVisible();

    // A door to B specifically.
    const openB = page.locator('[data-testid="agents-open-remoteB"]');
    await expect(openB).toBeVisible({ timeout: 20_000 });

    // Cross it. launchOn carries the origin; nothing else had to change.
    await openB.click();

    // The window that opens is B's app, and its roster is B's roster.
    const remoteWin = page.locator('wash-app-ai-remoteb');
    await expect(remoteWin).toBeAttached({ timeout: 20_000 });
    await expect(remoteWin.locator('[data-testid="ai-roster-pane"], [data-testid="ai-roster-toggle"]'))
      .toBeVisible({ timeout: 20_000 });
    await expect(remoteWin.getByText('work happening on B')).toBeVisible({ timeout: 20_000 });

    // And NOT A's. This is the regression the plan was written around:
    // the rail used to answer for A no matter which host you meant.
    await expect(remoteWin.getByText('work happening on A')).toHaveCount(0);
  } finally {
    if (a) await stopRouter(a);
    if (b) await stopRouter(b);
  }
});
