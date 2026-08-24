// Getting to the agent that wants you (docs/AGENT_UX.md N1 + N2), end to
// end: a click in the chrome → the shell → agentd → the right window.
//
// Both halves are asserted for the same reason the verbs spec gives:
// either alone passes while the feature is broken. A door could focus a
// window the service knows nothing about, and agentd could log a focus
// that never moved anything on screen.
//
// The two defects under test are the ones a user actually hits:
//   - every click on "Open Agent" used to spawn another Agent window, so
//     the way back to the agent you were watching was the way to lose it.
//   - a question raised no toast at all, so an agent blocked behind a
//     buried window waited in silence.

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

// openAgentDoor clicks the rail's per-host Agent door, expanding the
// section first if it is collapsed.
async function openAgentDoor(page: Page) {
  if ((await page.locator('[data-testid="sidebar-section-body-agents"]').count()) === 0) {
    await page.locator('[data-testid="sidebar-section-header-agents"]').click();
  }
  await page.locator('[data-testid="agents-open-local"]').click();
}

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
  return win;
}

test.describe('agent focus-or-launch', () => {
  test('the rail door goes to the Agent window instead of making another', async ({
    page,
    router,
  }) => {
    test.setTimeout(60_000);
    await startSession(page, router.url);
    await expect(page.locator('wash-app-ai')).toHaveCount(1);

    // Minimize, so "the door worked" means something visible rather than
    // "the window was already there".
    await page.getByRole('button', { name: 'Minimize window' }).click();
    await expect(page.locator('wash-app-ai').first()).toBeHidden();

    const cursor = router.logCursor();
    await openAgentDoor(page);

    // FE half: the window came back, and there is still exactly one.
    await expect(page.locator('wash-app-ai').first()).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('wash-app-ai')).toHaveCount(1);

    // BE half: no second Agent instance was spawned. The old behaviour
    // logs another app start here.
    expect(router.log().slice(cursor)).not.toMatch(/wash-ai ready instance=/);

    // And it keeps holding: a door is navigation, so pressing it twice
    // more must not accumulate windows either.
    await openAgentDoor(page);
    await openAgentDoor(page);
    await expect(page.locator('wash-app-ai')).toHaveCount(1);
  });

  test('a question toasts, and clicking the toast lands on that session', async ({
    page,
    router,
  }) => {
    test.setTimeout(90_000);
    const win = await startSession(page, router.url);

    const cursor = router.logCursor();
    // "ask" makes the fake adapter request permission, exactly as a real
    // one does — this is a genuine blocked turn, not a synthesised toast.
    const composer = win.locator('textarea');
    await composer.fill('please ask');
    await composer.press('Enter');

    // Bury the window NOW, while the turn is still in flight, so landing
    // on it later is observable — and so nothing sits between the toast
    // appearing and the click that has to happen inside its 4.5s life.
    await page.getByRole('button', { name: 'Minimize window' }).click();
    await expect(page.locator('wash-app-ai').first()).toBeHidden();

    // BE half 1: the question reached the queue.
    await router.waitForLog(/agentd: ask row=acp:/, 20_000, cursor);

    // FE half 1: a toast said so. This is N2's whole point — before it,
    // agentd raised no notifications at all.
    const toast = page.locator('[data-testid="notification"][data-level="warn"]').last();
    await expect(toast.locator('[data-testid="notification-title"]')).toContainText('needs you', {
      timeout: 15_000,
    });
    // The question itself, not just "something happened".
    await expect(toast.locator('[data-testid="notification-body"]')).toContainText('echo hello');
    // And it names the session it is about, which is what makes the click
    // land somewhere rather than merely opening the app.
    await expect(toast).toHaveAttribute('data-key', /^acp:\d+$/);

    const clickCursor = router.logCursor();
    await toast.click();

    // BE half 2: agentd resolved the key to the window showing it. The
    // shell handed the key back to agentd rather than guessing.
    await router.waitForLog(/agentd: focus key=acp:\d+ raising 1 window/, 15_000, clickCursor);

    // FE half 2: that window is what the user is now looking at, with the
    // question in it — and no second window was opened to show it.
    await expect(page.locator('wash-app-ai').first()).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('wash-app-ai')).toHaveCount(1);
  });
});

test('a blocked agent marks its taskbar pill, and looking at it clears the mark', async ({
  page,
  router,
}) => {
  // docs/AGENT_UX.md N6, end to end: agentd → the Agent window's BE →
  // EvtWindowAttention → the router → the shell → the pill. The Go tests
  // cover the router's half (wmstate_attention_test.go); nothing proved a
  // browser ever renders it, which is the half the user actually sees.
  //
  // The clear is the interesting assertion. The app raises the flag but
  // only the ROUTER lowers it, on focus — so an app cannot leave a pill
  // pulsing at a window you have already read.
  test.setTimeout(90_000);
  const win = await startSession(page, router.url);

  const pill = page.locator('[data-testid="taskbar-pill"]', { hasText: /codex|Agent/ }).first();
  await expect(pill).toBeVisible({ timeout: 20_000 });
  // Nothing is waiting yet: the mark must be absent BEFORE it is present,
  // or its later presence proves nothing.
  await expect(pill).not.toHaveAttribute('data-attention', 'true');

  const cursor = router.logCursor();
  const composer = win.locator('textarea');
  await composer.fill('please ask');
  await composer.press('Enter');
  await router.waitForLog(/agentd: ask row=acp:/, 20_000, cursor);

  // Minimize so the window cannot be "focused" — the router clears the
  // flag on focus, and a focused window is never marked in the first
  // place. This is the state the pill exists for.
  await page.getByRole('button', { name: 'Minimize window' }).click();
  await expect(pill).toHaveAttribute('data-attention', 'true', { timeout: 20_000 });
  await expect(pill).toHaveAttribute('title', /wants your attention/);

  // Look at it. The router drops the flag as part of the focus it already
  // performs, so the pill goes quiet without the app being told anything.
  await pill.click();
  await expect(page.locator('wash-app-ai').first()).toBeVisible({ timeout: 15_000 });
  await expect(pill).not.toHaveAttribute('data-attention', 'true', { timeout: 15_000 });

  // And the question is still there — clearing the MARK must not look
  // like answering the question.
  await expect(win.getByText(/echo hello/).first()).toBeVisible({ timeout: 15_000 });
});
