// The Agent app's roster pane (docs/SIDEBAR.md M2a).
//
// M1 gave the rail per-host counts. M2 moves the roster — and next the
// verbs — into com.wash.ai, because that is where they can be correct: an
// app talking to its own host's agentd carries a router-attested sender,
// so `launchOn(B, com.wash.ai)` gets B's roster with no new addressing.
// This spec is the local half of that claim; the two-router half lands
// with M2c.
//
// Same fake adapter as agent-session.spec.ts: e2e/fixtures/acp-fake on a
// PATH this test controls, so the suite needs no API key and no network.

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

/** openAgentWindow launches a fresh Agent window and returns it. */
async function openAgentWindow(page: Page, nth: number) {
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agent', exact: true }).click();
  const win = page.locator('wash-app-ai').nth(nth);
  await expect(win).toBeVisible();
  return win;
}

/** startSession drives the launcher in an already-open Agent window. */
async function startSession(win: ReturnType<Page['locator']>, prompt: string) {
  await win.locator('select').selectOption('codex');
  await win.getByRole('button', { name: 'Start session' }).click();
  const composer = win.locator('textarea');
  await expect(composer).toBeVisible({ timeout: 20_000 });
  await composer.fill(prompt);
  await composer.press('Enter');
}

test('the roster pane lists agentd\'s sessions and switches the detail pane', async ({ page, router }) => {
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  // The pane is furniture: always there, resizable, never toggled. It
  // used to appear on rules (more than one session, a question elsewhere,
  // an empty window) and each rule was a guess about when a list earns
  // its width — which is what made the way back to your own agent depend
  // on knowing a button existed.
  const first = await openAgentWindow(page, 0);
  await startSession(first, 'first session');
  const pane = first.locator('[data-testid="ai-roster-pane"]');
  await expect(pane).toBeVisible();
  await expect(pane).toBeVisible();
  await expect(pane.locator('[data-testid^="agents-row-"]')).toHaveCount(1);

  // The row the window is showing reads as the current one.
  await expect(pane.locator('[data-testid^="agents-row-"][data-active="true"]')).toHaveCount(1);

  // A second session in a second window. Both windows' rosters now list
  // BOTH sessions — the roster is agentd's, not the window's, which is the
  // whole reason it can answer for a host rather than for a window.
  const second = await openAgentWindow(page, 1);
  await startSession(second, 'second session');
  await expect(pane.locator('[data-testid^="agents-row-"]')).toHaveCount(2, { timeout: 20_000 });

  // And with more than one session to choose between, the pane opens
  // itself in the new window — that is when a list earns its width.
  await expect(second.locator('[data-testid="ai-roster-pane"]')).toBeVisible({ timeout: 10_000 });

  // Master-detail: activating the OTHER row re-points this window's detail
  // pane at that session, and its transcript replays into it. Before M2a a
  // window could only ever show the session it started.
  //
  // Driven from the SECOND window deliberately: the windows overlap on the
  // desktop, and the newer one is on top — clicking the first window's
  // pane through it hits the wrong window's row (playwright catches this
  // as a pointer-event interception rather than silently misclicking).
  const topPane = second.locator('[data-testid="ai-roster-pane"]');
  await expect(topPane.locator('[data-testid^="agents-row-"]')).toHaveCount(2, { timeout: 20_000 });
  const otherRow = topPane.locator('[data-testid^="agents-row-"][data-active="false"]').first();
  await expect(otherRow).toBeVisible();
  // Remember WHICH row by its stable id: the locator above selects on
  // data-active="false", so once the click flips it the locator no longer
  // matches its own element.
  const otherKey = await otherRow.getAttribute('data-testid');
  await otherRow.click();

  // The first session's transcript is now in the second window.
  await expect(second.getByText('first session')).toBeVisible({ timeout: 20_000 });

  // Exactly one row is current, and it is the one just picked.
  await expect(topPane.locator('[data-testid^="agents-row-"][data-active="true"]')).toHaveCount(1);
  await expect(topPane.locator(`[data-testid="${otherKey}"]`)).toHaveAttribute('data-active', 'true');
});

test('a roster verb acts on the row, not on the window it was clicked in', async ({ page, router }) => {
  // The claim M2b makes: verbs are key-addressed, so a window can end a
  // session it is not showing — including one with no window at all. The
  // rail could only ever do this for the LOCAL host, and only by
  // gatewaying through the session BE.
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  const first = await openAgentWindow(page, 0);
  await startSession(first, 'session to be ended');
  const second = await openAgentWindow(page, 1);
  await startSession(second, 'the window doing the ending');

  // Work from the top window, which shows ITS OWN session. Its roster
  // lists both.
  const pane = second.locator('[data-testid="ai-roster-pane"]');
  await expect(pane).toBeVisible({ timeout: 20_000 });
  await expect(pane.locator('[data-testid^="agents-row-"]')).toHaveCount(2, { timeout: 20_000 });

  // The OTHER row — the session this window is not showing.
  const otherRow = pane.locator('[data-testid^="agents-row-"][data-active="false"]').first();
  await expect(otherRow).toBeVisible();
  const otherKey = (await otherRow.getAttribute('data-testid'))!.replace('agents-row-', '');

  // The verbs live behind the row's menu (a rail row was too narrow for a
  // button strip, and a menu can render an inapplicable verb disabled
  // rather than vanishing). It portals to document.body, so it is
  // addressed from the page rather than from inside the row — same idiom
  // as agent-rail-verbs.spec.ts, which drives the identical component.
  const from = router.logCursor();
  await otherRow.locator('[data-testid="agents-verbs-btn"]').click();
  const menu = page.locator('[data-testid="agents-row-actions"]');
  await expect(menu).toBeVisible();
  await menu.locator('[data-testid="agents-menu-detach"]').click();

  // agentd acted on the key it was given, which is the row's — not the
  // clicking window's session.
  await router.waitForLog(new RegExp(`wash-ai: roster detach key=${otherKey.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`), 10_000, from);

  // And the row says so: detached is "still running, no window on it".
  await expect(pane.locator(`[data-testid="agents-row-${otherKey}"]`))
    .toBeVisible({ timeout: 10_000 });

  // The window that issued it is untouched — its own session still shows.
  await expect(second.getByText('the window doing the ending')).toBeVisible();
});

test('an empty Agent window shows the sessions you already have', async ({ page, router }) => {
  // Opening the app while a session of yours is detached used to give a
  // blank new-session form, with the way back hidden behind a toggle you
  // had to know about (docs/AGENT_UX.md N1). A window with nothing to
  // show should show you what there is.
  test.setTimeout(60_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  const first = await openAgentWindow(page, 0);
  await startSession(first, 'something to come back to');

  // Detach it: the session keeps running with no window on it.
  const pane = first.locator('[data-testid="ai-roster-pane"]');
  const row = pane.locator('[data-testid^="agents-row-"]').first();
  await row.locator('[data-testid="agents-verbs-btn"]').click();
  await page.locator('[data-testid="agents-row-actions"] [data-testid="agents-menu-detach"]').click();
  await expect(page.locator('wash-app-ai')).toHaveCount(0, { timeout: 15_000 });

  // Now open the app fresh. The launcher is there, but so is the list —
  // and one click on the row is the whole way back.
  const fresh = await openAgentWindow(page, 0);
  const freshPane = fresh.locator('[data-testid="ai-roster-pane"]');
  await expect(freshPane).toBeVisible({ timeout: 20_000 });
  await expect(freshPane.locator('[data-testid^="agents-row-"]')).toHaveCount(1, { timeout: 20_000 });
});

test('New session opens another window rather than hijacking this one', async ({ page, router }) => {
  test.setTimeout(60_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  const first = await openAgentWindow(page, 0);
  await startSession(first, 'keep me');
  const pane = first.locator('[data-testid="ai-roster-pane"]');

  await pane.locator('[data-testid="ai-roster-new"]').click();

  // Two windows: the session survives in the first, the launcher is the
  // second. Clearing the view in place would have left this window's BE
  // still streaming the old transcript into a "new" session.
  await expect(page.locator('wash-app-ai')).toHaveCount(2, { timeout: 20_000 });
  await expect(first.locator('textarea')).toBeVisible();
});

test('the sessions pane is dragged, not toggled, and the width survives a reload', async ({
  page,
  router,
}) => {
  test.setTimeout(60_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  const win = await openAgentWindow(page, 0);
  await startSession(win, 'a session to look at');
  const pane = win.locator('[data-testid="ai-roster-pane"]');
  await expect(pane).toBeVisible();

  const before = (await pane.boundingBox())!.width;
  const bar = win.locator('[data-testid="ai-splitter"]');
  const grip = (await bar.boundingBox())!;

  // Drag the divider right. Mouse move in two steps: one is sometimes
  // coalesced away before the listener attaches.
  await page.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2);
  await page.mouse.down();
  await page.mouse.move(grip.x + 120, grip.y + grip.height / 2, { steps: 2 });
  await page.mouse.up();

  const after = (await pane.boundingBox())!.width;
  expect(after).toBeGreaterThan(before + 40);

  // The width is backend-owned view state (persistSessionView), so it
  // comes back with the window rather than dying with the tab.
  await page.reload();
  await expect(page.locator('wash-app-session')).toBeVisible();
  const restored = page.locator('wash-app-ai').first().locator('[data-testid="ai-roster-pane"]');
  await expect(restored).toBeVisible({ timeout: 20_000 });
  await expect
    .poll(async () => (await restored.boundingBox())!.width, { timeout: 15_000 })
    .toBeGreaterThan(before + 40);
});
