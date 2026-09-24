// The payoff (docs/SIDEBAR.md M2c): focusOrLaunch(B, com.wash.agents) gets
// B's roster, with working verbs, and NO new addressing.
//
// This is what the whole plan is for. Before it, the rail's agent verbs
// gatewayed through the session BE, whose SendAppMsgTo resolves inside
// its own router — so an agent on B had no control surface at all, and
// the rail silently showed A's instead (§1.2). Expressing the roster as
// an app made cross-host addressing something wash already had. The
// roster now lives in the singleton Agents manager (com.wash.agents), so
// the door to B opens B's manager and its Running pane lists B's sessions.
//
// Two routers stand in for host A (the desktop) and host B (what an
// `ssh -L` tunnel would reach), wired with ?peer= as remote-apps.spec.ts
// does. B runs --no-session, which is exactly why a session-BE gateway
// could never have served it.

import type { Page } from '@playwright/test';
import { test, expect } from '@playwright/test';
import { startRouter, stopRouter, type RouterHandle } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR } from '../fixtures/agents';

const env = { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` };

/**
 * startOn starts a session on one host by launching a controller there and
 * handing it a start, returning the roster key agentd gave it. Deliberately
 * NOT through that host's manager: the point of the spec is the manager
 * the door opens, so no manager may exist on either host beforehand.
 */
async function startOn(r: RouterHandle, prompt: string): Promise<string> {
  const from = r.logCursor();
  const win = await r.controlRequest({ t: 'launch', app_id: 'com.wash.ai' });
  await r.controlRequest({
    t: 'msg', instance_id: String(win.instance_id),
    data: { kind: 'start', agent: 'codex', cwd: '', prompt },
  });
  const line = await r.waitForLog(/agentd: acp session started key=acp:\d+/, 20_000, from);
  return line.replace('agentd: acp session started key=', '');
}

/** openDoorToB opens the rail's Agents section and crosses B's door. */
async function openDoorToB(page: Page) {
  // The section carries the count while collapsed; open it for the door.
  // (It opens itself only when someone is WAITING on you; an agent merely
  // working is not an interruption, by design.)
  if ((await page.locator('[data-testid="sidebar-section-body-agents"]').count()) === 0) {
    await page.locator('[data-testid="sidebar-section-header-agents"]').click();
  }
  await expect(page.locator('[data-testid="sidebar-section-body-agents"]')).toBeVisible();
  const openB = page.locator('[data-testid="agents-open-remoteB"]');
  await expect(openB).toBeVisible({ timeout: 20_000 });
  await openB.click();
}

test('the Agents manager opened on a remote host shows THAT host\'s sessions', async ({ page }) => {
  let a: RouterHandle | undefined;
  let b: RouterHandle | undefined;
  try {
    a = await startRouter({ apps: [...AGENT_APPS], extraEnv: env });
    b = await startRouter({
      apps: AGENT_APPS.filter((app) => app !== 'session'),
      extraArgs: ['--no-session', '--allow-cross-origin'],
      extraEnv: env,
    });

    // A session on each host, with different prompts so a mix-up is
    // visible rather than merely suspected.
    await startOn(a, 'work happening on A');
    const bKey = await startOn(b, 'work happening on B');

    const bPort = new URL(b.url).port;
    await page.goto(`${a.url}?peer=${encodeURIComponent(`remoteB@ws://127.0.0.1:${bPort}/ws`)}`);
    await expect(page.locator('[data-testid="wash-cam"]')).toBeAttached({ timeout: 10_000 });

    // Cross B's door. focusOrLaunch carries the origin; nothing else had to
    // change. B has no manager yet, so this launches one THERE.
    const bLaunch = b.logCursor();
    await openDoorToB(page);
    await b.waitForLog(/wash-router app com\.wash\.agents up instance=/, 20_000, bLaunch);
    expect(a.log()).not.toMatch(/wash-router app com\.wash\.agents up/);

    // B's manager, in a window served by B's bundle (the per-origin mangled
    // tag is the proof it is B's code, not A's).
    const bManager = page.locator('wash-app-agents-remoteb');
    await expect(bManager).toBeAttached({ timeout: 20_000 });
    await expect(bManager).toHaveCount(1);

    // B's session is in B's Running pane — the row agentd on B keyed.
    const bRows = bManager.locator('[data-testid="ai-roster-pane"] [data-testid^="agents-row-"]');
    await expect(bRows).toHaveCount(1, { timeout: 20_000 });
    await expect(bRows.first()).toHaveAttribute('data-testid', `agents-row-${bKey}`);
    await expect(bManager.locator('[data-testid="ai-roster-pane"]'))
      .toContainText('work happening on B', { timeout: 20_000 });

    // And A's session is nowhere in B's manager. This is the regression the
    // plan was written around: the rail used to answer for A no matter
    // which host you meant.
    await expect(bManager).not.toContainText('work happening on A');

    // The door is navigation: crossing it again raises B's one manager.
    // Buried first, so its coming back is the barrier before counting —
    // a count taken straight after the click would precede any duplicate.
    const bId = Number(await bManager.getAttribute('data-wash-window'));
    await page.evaluate((w) => window.wash.minimizeWindow(w, 'remoteB'), bId);
    await expect(bManager).toBeHidden();
    await openDoorToB(page);
    await expect(bManager).toBeVisible({ timeout: 15_000 });
    await expect(bManager).toHaveCount(1);
    expect(b.log().slice(bLaunch).match(/wash-router app com\.wash\.agents up/g)).toHaveLength(1);
  } finally {
    if (a) await stopRouter(a);
    if (b) await stopRouter(b);
  }
});

test('a remote Agents manager survives a browser reload', async ({ page }) => {
  // The FE decides manager-vs-controller from its element's tag, and a
  // remote host's windows mount under the per-origin tag
  // (wash-app-agents-remoteb). Matching only the bare tag brought B's
  // manager back after a reload as a blank "not attached to a session"
  // window with no roster: the BE's one-shot `role` message is not replayed.
  let a: RouterHandle | undefined;
  let b: RouterHandle | undefined;
  try {
    a = await startRouter({ apps: [...AGENT_APPS], extraEnv: env });
    b = await startRouter({
      apps: AGENT_APPS.filter((app) => app !== 'session'),
      extraArgs: ['--no-session', '--allow-cross-origin'],
      extraEnv: env,
    });
    await startOn(a, 'work happening on A');
    await startOn(b, 'work happening on B');

    const bPort = new URL(b.url).port;
    await page.goto(`${a.url}?peer=${encodeURIComponent(`remoteB@ws://127.0.0.1:${bPort}/ws`)}`);
    await expect(page.locator('[data-testid="wash-cam"]')).toBeAttached({ timeout: 10_000 });
    await openDoorToB(page);
    const rows = page.locator('wash-app-agents-remoteb [data-testid="ai-roster-pane"] [data-testid^="agents-row-"]');
    await expect(rows).toHaveCount(1, { timeout: 20_000 });

    await page.reload();
    await expect(page.locator('wash-app-agents-remoteb')).toBeAttached({ timeout: 20_000 });
    await expect(rows).toHaveCount(1, { timeout: 15_000 });
  } finally {
    if (a) await stopRouter(a);
    if (b) await stopRouter(b);
  }
});

test('the door to B opens B\'s manager even when a local window shares an instance id with B\'s', async ({ page }) => {
  // focusOrLaunch asks pickWindow for B's windows of com.wash.agents, and
  // appIDForWindow (web/shell/src/main.tsx) used to resolve a window's app
  // from its BARE instance id ("i-1") in the LOCAL client's map. Instance
  // ids are small per-router counters and collide: with A's manager and
  // B's controller both i-1, the door raised B's controller instead of
  // launching B's manager. The window's own origin now picks the map.
  let a: RouterHandle | undefined;
  let b: RouterHandle | undefined;
  try {
    a = await startRouter({ apps: [...AGENT_APPS], extraEnv: env });
    b = await startRouter({
      apps: AGENT_APPS.filter((app) => app !== 'session'),
      extraArgs: ['--no-session', '--allow-cross-origin'],
      extraEnv: env,
    });
    // A's manager first, so it is i-1 on A — the normal state of a desktop
    // where someone has opened Agents.
    await a.controlRequest({ t: 'launch', app_id: 'com.wash.agents' });
    await a.waitForLog(/wash-ai ready instance=i-1 manager=true/, 20_000);
    await startOn(b, 'work happening on B');
    await b.waitForLog(/wash-ai ready instance=i-1 manager=false/, 20_000);

    const bPort = new URL(b.url).port;
    await page.goto(`${a.url}?peer=${encodeURIComponent(`remoteB@ws://127.0.0.1:${bPort}/ws`)}`);
    await expect(page.locator('[data-testid="wash-cam"]')).toBeAttached({ timeout: 10_000 });
    // Both windows must be known to the shell before the door is asked.
    await expect(page.locator('wash-app-agents')).toBeAttached({ timeout: 20_000 });
    await expect(page.locator('wash-app-ai-remoteb')).toBeAttached({ timeout: 20_000 });

    const bLaunch = b.logCursor();
    await openDoorToB(page);
    await b.waitForLog(/wash-router app com\.wash\.agents up instance=/, 15_000, bLaunch);
    await expect(page.locator('wash-app-agents-remoteb')).toBeAttached({ timeout: 15_000 });
  } finally {
    if (a) await stopRouter(a);
    if (b) await stopRouter(b);
  }
});
