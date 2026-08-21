// The per-session verbs, end to end (GH #21, relocated by SIDEBAR.md M2c).
//
// agentd has handled agent_detach / agent_cancel / agent_stop since the
// ACP tier landed. What #21 hit is that nothing could reach them; #21's
// fix put buttons in the desktop rail, and M2c moved them into
// com.wash.ai — because the rail's sends gateway through the session BE
// and resolve inside its own router, so they could never have worked on a
// remote host. This is the same chain with one hop removed: a click in
// the app → agentd → the ACP session actually ending.
//
// Both halves are still asserted, because either alone can pass while the
// feature is broken: the row could vanish from an FE that never reached
// the backend, and agentd could end a session the roster still shows.

import { fileURLToPath } from 'node:url';
import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test.use({
  routerOpts: {
    apps: ['session', 'agentd', 'ai', 'notify'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

async function startSession(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page
    .locator('[data-testid="start-menu"]')
    .getByRole('button', { name: 'Agent', exact: true })
    .click();
  const win = page.locator('wash-app-ai').first();
  await expect(win).toBeVisible();
  await win.locator('select').selectOption('codex');
  await win.getByRole('button', { name: 'Start session' }).click();
  const composer = win.locator('textarea');
  await expect(composer).toBeVisible({ timeout: 20_000 });
  await composer.fill('say something');
  await composer.press('Enter');
  await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });
  return win;
}

// openRoster opens the Agent window's roster pane. It stays shut with a
// single session (a list of one restates the window you're looking at),
// so the tests ask for it.
async function openRoster(page: Page) {
  const win = page.locator('wash-app-ai').first();
  const pane = win.locator('[data-testid="ai-roster-pane"]');
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
    expect(router.log().slice(cursor)).not.toMatch(/acp session ended/);

    // Confirming fires: FE → this app's BE → agentd → the adapter. One
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

    // Detaching your OWN session closes the window — that is what detach
    // means, and it is the same path the close dialog takes. So the
    // session now has no window at all, which is precisely the state the
    // rail has to be able to get you out of.
    await expect(page.locator('wash-app-ai')).toHaveCount(0, { timeout: 15_000 });

    // Reload first, so nothing below can be satisfied by FE state that
    // happened to survive.
    await page.reload();
    await expect(page.locator('wash-app-session')).toBeVisible();

    // The way back is the rail's door: it still knows an agent is running
    // (M1's awareness channel never depended on a window), and opening the
    // app is one click. This is the deep-link the §3.2(7) tripwire judges.
    const railHeader = page.locator('[data-testid="sidebar-section-header-agents"]');
    if ((await page.locator('[data-testid="sidebar-section-body-agents"]').count()) === 0) {
      await railHeader.click();
    }
    await page.locator('[data-testid="agents-open-local"]').click();
    await expect(page.locator('wash-app-ai')).toHaveCount(1, { timeout: 20_000 });

    // The fresh window's roster carries the detached session, described as
    // what it is rather than advertising a terminal it does not have (the
    // duplicate-`title` bug this shipped with).
    const restoredBody = await openRoster(page);
    const restoredRow = restoredBody.locator('[data-testid^="agents-row-"]').first();
    await expect(restoredRow).toHaveAttribute('title', /Detached/, { timeout: 15_000 });

    // Detach is disabled on it — there is no window left to let go of.
    await restoredRow.locator('[data-testid="agents-verbs-btn"]').click();
    await expect(page.locator('[data-testid="agents-menu-detach"]')).toBeDisabled({ timeout: 15_000 });
    await page.keyboard.press('Escape');
    await page.mouse.click(5, 5);

    // And one double-click brings it back, transcript and all.
    await restoredRow.dblclick();
    await expect(page.getByText('Hello from the fake agent.').first())
      .toBeVisible({ timeout: 20_000 });
  });
});
