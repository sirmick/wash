// The per-session verbs, end to end (GH #21, relocated by SIDEBAR.md M2c).
//
// agentd has handled agent_detach / agent_cancel / agent_stop since the
// ACP tier landed. What #21 hit is that nothing could reach them; #21's
// fix put buttons in the desktop rail, and M2c moved them into an app —
// because the rail's sends gateway through the session BE and resolve
// inside its own router, so they could never have worked on a remote host.
// They now live on the rows of the Agents manager's Running pane
// (com.wash.agents): a click in the manager → agentd → the ACP session
// actually ending.
//
// Both halves are still asserted, because either alone can pass while the
// feature is broken: the row could vanish from an FE that never reached
// the backend, and agentd could end a session the roster still shows.

import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, openAgents, startAgentSession, windowOf } from '../fixtures/agents';

test.use({
  routerOpts: {
    apps: [...AGENT_APPS],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

async function startSession(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  const win = await startAgentSession(page, 'say something');
  await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });
  return win;
}

// openRoster raises the Agents manager and returns its Running pane. The
// controller agentd just opened sits on top of the manager, so the manager
// is raised first or every row click lands on the wrong window.
async function openRoster(page: Page) {
  const manager = await openAgents(page);
  const pane = manager.locator('[data-testid="ai-roster-pane"]');
  await expect(pane).toBeVisible();
  return pane;
}

test.describe('agent roster per-session verbs', () => {
  test('End ends the session — the menu asks first, then reaches agentd', async ({
    page,
    router,
  }) => {
    test.setTimeout(60_000);
    await startSession(page, router.url);
    const body = await openRoster(page);
    const row = body.locator('[data-testid^="agents-row-"]').first();
    await expect(row).toBeVisible({ timeout: 15_000 });

    // Everything after this point must be caused by our clicks, so read
    // the log from here rather than from 0.
    const cursor = router.logCursor();

    // The verbs live in a per-row menu. It portals to document.body, so
    // it is addressed from the page, not from inside the row.
    await row.locator('[data-testid="agents-verbs-btn"]').click();
    const menu = page.locator('[data-testid="agents-row-actions"]');
    await expect(menu).toBeVisible();

    // Picking End asks first. Asserted rather than assumed: End sits one
    // row from Detach, and an End that fired on the pick would be a
    // session lost to a slip.
    await menu.locator('[data-testid="agents-menu-end"]').click();
    // Wait for the confirm row before sampling the log for absence.
    // Reading it in the same tick as the click proves nothing: an End
    // that DID fire would not have reached agentd yet either, so the
    // assertion would pass on a missing gate. The confirm row appearing
    // is the barrier that makes "and still nothing ended" mean something.
    await expect(menu.locator('[data-testid="agents-menu-end-confirm"]')).toBeVisible();
    expect(router.log().slice(cursor)).not.toMatch(/acp session ended/);

    // Confirming fires: FE → the manager's BE → agentd → the adapter. One
    // hop shorter than the rail's, and correct on any host.
    await menu.locator('[data-testid="agents-menu-end-confirm"]').click();

    // BE half: agentd really retired the session.
    await router.waitForLog(/agentd: acp session ended key=/, 15_000, cursor);

    // FE half: the roster reflects it. A pane that only *looked* right
    // would pass the BE assertion on its own.
    await expect(body.locator('[data-testid^="agents-row-"]')).toHaveCount(0, { timeout: 15_000 });
    await expect(body.locator('[data-testid="agents-empty"]')).toBeVisible();
  });

  test('Detach keeps the session running and offers it back', async ({ page, router }) => {
    test.setTimeout(60_000);
    await startSession(page, router.url);
    const body = await openRoster(page);
    const row = body.locator('[data-testid^="agents-row-"]').first();
    await expect(row).toBeVisible({ timeout: 15_000 });

    const cursor = router.logCursor();
    await row.locator('[data-testid="agents-verbs-btn"]').click();
    const menu = page.locator('[data-testid="agents-row-actions"]');
    await expect(menu).toBeVisible();
    await menu.locator('[data-testid="agents-menu-detach"]').click();

    // agentd marks it detached and republishes — it does NOT end.
    await router.waitForLog(/agentd: acp detached key=/, 15_000, cursor);
    expect(router.log().slice(cursor)).not.toMatch(/acp session ended/);

    // Detach means "no window": agentd tells the session's controller to
    // go — the same path the close dialog takes. So the session now has no
    // window at all, which is precisely the state the rail has to be able
    // to get you out of.
    await expect(page.locator('wash-app-ai')).toHaveCount(0, { timeout: 15_000 });

    // Close the manager as well, then reload, so nothing below can be
    // satisfied by a window or FE state that happened to survive.
    const manager = page.locator('wash-app-agents');
    await windowOf(page, manager).locator('[data-testid="window-close"]').click();
    await expect(manager).toHaveCount(0, { timeout: 15_000 });
    await page.reload();
    await expect(page.locator('wash-app-session')).toBeVisible();

    // The way back is the rail's door: it still knows an agent is running
    // (M1's awareness channel never depended on a window), and opening the
    // Agents manager is one click. This is the deep-link the §3.2(7)
    // tripwire judges. It opens the manager, NOT a controller: which
    // session you want is the manager's question to ask.
    const railHeader = page.locator('[data-testid="sidebar-section-header-agents"]');
    if ((await page.locator('[data-testid="sidebar-section-body-agents"]').count()) === 0) {
      await railHeader.click();
    }
    await page.locator('[data-testid="agents-open-local"]').click();
    await expect(page.locator('wash-app-agents')).toHaveCount(1, { timeout: 20_000 });
    await expect(page.locator('wash-app-ai')).toHaveCount(0);

    // The fresh manager's roster carries the detached session, described as
    // what it is rather than advertising a terminal it does not have (the
    // duplicate-`title` bug this shipped with).
    const restoredBody = page.locator('wash-app-agents [data-testid="ai-roster-pane"]');
    const restoredRow = restoredBody.locator('[data-testid^="agents-row-"]').first();
    await expect(restoredRow).toHaveAttribute('title', /Detached/, { timeout: 15_000 });

    // Detach is disabled on it — there is no window left to let go of.
    await restoredRow.locator('[data-testid="agents-verbs-btn"]').click();
    await expect(page.locator('[data-testid="agents-menu-detach"]')).toBeDisabled({ timeout: 15_000 });
    await page.keyboard.press('Escape');
    await page.mouse.click(5, 5);

    // And one click brings it back in a controller of its own, transcript
    // and all.
    const reattach = router.logCursor();
    await restoredRow.click();
    await router.waitForLog(/agentd: opening controller key=acp:\d+/, 15_000, reattach);
    const ctrl = page.locator('wash-app-ai');
    await expect(ctrl).toHaveCount(1, { timeout: 20_000 });
    await expect(ctrl.getByText('Hello from the fake agent.').first()).toBeVisible({ timeout: 20_000 });
    expect(router.log().slice(cursor)).not.toMatch(/acp session (ended|started)/);
  });
});
