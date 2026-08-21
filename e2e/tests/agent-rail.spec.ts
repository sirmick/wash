// The Agents right rail, across the events that were blamed for losing it
// (GH #21).
//
// #21 reported that after a browser reconnect/supersede the rail stopped
// offering resume/clone/terminate for a session wash and the adapter both
// still held, and that NO backend request was logged when the user tried.
// Two separable claims live in there, and this spec pins the first so the
// second can be built on solid ground:
//
//   1. "reconnect loses the roster" — tested here. The roster is a
//      StateService whose subscribe handler replies with the current
//      snapshot (pkg/sdk/stateservice.go), so a rail that re-subscribes
//      recovers even a quiescent session that will never push again. A
//      page reload and a supersede both remount the FE, so both must
//      rehydrate. These are the regression tests.
//
//   2. "the rail offers no resume/clone/terminate" — NOT a reconnect
//      symptom. Those affordances are absent from the rail entirely
//      (AgentsWidget renders asks + live rows; RecentRow is dead code),
//      which is why no backend request was logged: there is no button to
//      send one. Covered by agent-rail-verbs.spec.ts as those land.
//
// The agent is e2e/fixtures/acp-fake on a PATH this test controls, so the
// suite needs no API key, no network and no money.

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

// startSession opens an Agent window and gets a real ACP session running,
// then returns once the agent has answered — so the roster row it produces
// is in the quiescent (done) state that #21 was reported against. A row
// that only survives because something keeps pushing would prove nothing.
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
  // The reply landing is what makes the turn done.
  await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });
  return win;
}

// The Agents section is collapsed by default; the rail only renders its
// body once expanded.
async function openRail(page: Page) {
  const header = page.locator('[data-testid="sidebar-section-header-agents"]');
  await expect(header).toBeVisible();
  const body = page.locator('[data-testid="sidebar-section-body-agents"]');
  if ((await body.count()) === 0) await header.click();
  await expect(body).toBeVisible();
  return body;
}

// A live agent shows up in the rail as a count and a door, and in the
// Agent window as a roster row. Both are asserted: since SIDEBAR.md M2c
// the rail keeps awareness and the app keeps the roster, so a reconnect
// that restored only one of them would be half-broken in a way a single
// assertion could not see.
async function expectRailHasAgent(page: Page) {
  const body = await openRail(page);
  // Awareness half: the rail knows an agent is running HERE.
  const door = body.locator('[data-testid="agents-open-local"]');
  await expect(door).toBeVisible({ timeout: 15_000 });
  await expect(door).toContainText(/running|waiting/, { timeout: 15_000 });

  // Roster half: the app lists the session itself.
  const win = page.locator('wash-app-ai').first();
  const pane = win.locator('[data-testid="ai-roster-pane"]');
  await expect(pane.locator('[data-testid^="agents-row-"]').first()).toBeVisible({
    timeout: 15_000,
  });
  await expect(pane.locator('[data-testid="agents-empty"]')).toHaveCount(0);
}

test.describe('agent awareness + roster survive the reconnect paths', () => {
  test('a reload rehydrates the window and roster for a session that is done', async ({ page, router }) => {
    test.setTimeout(60_000);
    await startSession(page, router.url);
    await expectRailHasAgent(page);

    // The reporter's "refresh". The FE remounts and re-subscribes; agentd
    // is a separate process that never saw the browser go, so the row it
    // returns is the SAME session, not a new one.
    await page.reload();
    await expect(page.locator('wash-app-session')).toBeVisible();
    await expectRailHasAgent(page);

    // The window instance survives the browser. Its FE must restore which
    // agentd session it was showing and request the transcript again,
    // rather than falling back to a misleading new-session launcher.
    const win = page.locator('wash-app-ai').first();
    await expect(win.locator('textarea')).toBeVisible({ timeout: 15_000 });
    await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 15_000 });
    await expect(win.getByRole('button', { name: 'Start session' })).toHaveCount(0);

    // And the backend really was asked again — the snapshot came from a
    // fresh subscribe, not from FE state that happened to survive.
    expect(router.log()).toMatch(/agentd: acp (row|session started)/);
  });

  test('a superseding window gets the roster the old one had', async ({ page, router, context }) => {
    test.setTimeout(60_000);
    await startSession(page, router.url);
    await expectRailHasAgent(page);

    // Second window on the same session: the router hands the shell head
    // over and tells the predecessor it was superseded (router.go,
    // superseded_test.go). The new window must come up with the roster.
    const second = await context.newPage();
    await second.goto(router.url);
    await expect(second.locator('wash-app-session')).toBeVisible();
    await expectRailHasAgent(second);
    await second.close();
  });
});
