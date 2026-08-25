// The managed agent path, end to end (docs/AGENT_APP.md §13).
//
// M5 deleted six specs that covered the intercept tier and shipped
// nothing in their place; these are the replacement.
//
// The agent is `e2e/fixtures/acp-fake`, built as `codex-acp` and put on a
// PATH this test controls — so nothing in production knows it is a
// fixture, and the suite needs no API key, no network and no money. Its
// frames were captured from real adapters with the conformance tracer,
// and it passes the same conformance test they do; a shape that drifts
// upstream fails there first.

import { fileURLToPath } from 'node:url';
import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';

// Resolved from this file, not from process.cwd(): playwright can be
// invoked from the repo root or from e2e/, and a cwd-relative path
// silently found NO fake — so the suite quietly ran the real agent, cost
// real tokens, and failed on text a model had actually written.
const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test.use({
  routerOpts: {
    apps: ['session', 'agentd', 'ai', 'notify'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

// openAgent opens the desktop and starts a session in a fresh Agent
// window. The fake is the only adapter on this PATH, so "Start" starts it.
async function openAgent(page: Page, url: string, prompt: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agent', exact: true }).click();
  const win = page.locator('wash-app-ai').first();
  await expect(win).toBeVisible();

  await win.locator('select').selectOption('codex');
  await win.getByRole('button', { name: 'Start session' }).click();

  const composer = win.locator('textarea');
  await expect(composer).toBeVisible({ timeout: 20_000 });
  await composer.fill(prompt);
  await composer.press('Enter');
  return win;
}

test.describe('managed agent sessions', () => {
  test('a session streams its reply into the transcript', async ({ page, router }) => {
    const win = await openAgent(page, router.url, 'say something');

    // What the human typed comes back as a transcript line — an agent
    // does not echo the prompt its own client just sent it, so wash
    // records its side.
    await expect(win.getByText('say something')).toBeVisible({ timeout: 20_000 });

    // Markdown, not a literal ## and asterisks.
    await expect(win.getByText('Heading', { exact: true })).toBeVisible({ timeout: 20_000 });

    // Streamed across three chunks that split mid-sentence. This is the
    // assertion that catches reordering and lost whitespace — the two
    // bugs that actually happened.
    await expect(win.getByText('Hello from the fake agent.')).toBeVisible();

    // A tool call is one line, not a wall of output.
    await expect(win.getByText('README.md')).toBeVisible();

    // A GFM table becomes a real table, not a row of pipes.
    await expect(win.locator('table')).toBeVisible();
    await expect(win.locator('th', { hasText: 'Adapter' })).toBeVisible();
    await expect(win.locator('td', { hasText: 'claude' })).toBeVisible();

    // An inline image is DECODED, not merely present: a broken <img>
    // would still satisfy toBeVisible, which is the trap here.
    const img = win.locator('img[alt="image from the agent"]');
    await expect(img).toBeVisible();
    await expect.poll(() => img.evaluate((el: HTMLImageElement) => el.naturalWidth)).toBeGreaterThan(0);
  });

  test('a permission request can be answered, and the answer reaches the agent', async ({ page, router }) => {
    const win = await openAgent(page, router.url, 'please ask before running');

    // The command the agent wants to run is what the human reads.
    await expect(win.getByText('echo hello > /tmp/wash-e2e-fake')).toBeVisible({ timeout: 20_000 });

    await win.getByRole('button', { name: /^Allow(\s|$)/ }).first().click();

    // The fake reports back what outcome it received, so this asserts the
    // answer reached the AGENT — not merely that a button disappeared.
    await expect(win.getByText('Permission outcome: allow')).toBeVisible({ timeout: 20_000 });
  });

  // Host-side yolo: wash stops asking and answers "allow" itself. The
  // hazard of the feature is forgetting it is on, so the test asserts the
  // visible half as hard as the functional one.
  test('yolo auto-approves what would have been asked, and says so', async ({ page, router }) => {
    const win = await openAgent(page, router.url, 'say something');
    await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });
    // Off by default — no badge.
    await expect(win.locator('[data-testid="agent-yolo-badge"]')).toHaveCount(0);

    // The menubar by testid, not by accessible name: the roster pane's
    // "New session" button is also a button whose name contains
    // "Session", and a loose name match picks up both.
    await win.locator('[data-testid="ai-menubar-session"]').click();
    await page.locator('[data-testid="ai-menu-yolo"]').click();

    // The badge is permanent while it is on, and the transcript records
    // the moment the guard came off.
    await expect(win.locator('[data-testid="agent-yolo-badge"]')).toBeVisible({ timeout: 10_000 });
    await expect(win.getByText(/Auto-approval \(yolo\) is ON/)).toBeVisible({ timeout: 10_000 });

    // The same prompt that produced a permission question above now runs
    // without one: no Allow button, and the agent hears the outcome.
    const cursor = router.logCursor();
    const composer = win.locator('textarea');
    await composer.fill('please ask before running');
    await composer.press('Enter');

    // BE half first, as a barrier. agentd deciding is the hop that makes
    // the rest of this test meaningful, and asserting it separately is
    // what tells "agentd never auto-approved" from "the FE never showed
    // it" — a distinction the FE-only assertion below could not make when
    // this failed on CI.
    await router.waitForLog(/agentd: acp decide .*decision=allow reason=yolo/, 20_000, cursor);
    // ...and the agent really heard the answer, rather than agentd just
    // logging one.
    await expect(win.getByText('Permission outcome: allow')).toBeVisible({ timeout: 20_000 });
    await expect(win.getByRole('button', { name: /^Allow(\s|$)/ })).toHaveCount(0);
    // Every auto-approval is announced, not silent.
    await expect(win.getByText(/Auto-approved \(yolo\)/)).toBeVisible({ timeout: 10_000 });
  });

  test('the approval mode is on the window, and changing it reaches the agent', async ({ page, router }) => {
    const win = await openAgent(page, router.url, 'say something');
    await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

    // The preset lives on the session it governs, not in Settings.
    const mode = win.locator('[data-testid="agent-mode"]');
    await expect(mode).toHaveValue('agent');

    await mode.selectOption('agent-full-access');

    // The agent confirms with current_mode_update, so this asserts the
    // change reached it — not that a <select> changed locally.
    await expect(mode).toHaveValue('agent-full-access', { timeout: 15_000 });
  });

  test("the agent's own settings render as controls and changing one reaches it", async ({ page, router }) => {
    const win = await openAgent(page, router.url, 'say something');
    await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

    // One generic control per option the agent exposes — model here, but
    // the same renderer covers reasoning effort and plan mode.
    const model = win.locator('[data-testid="agent-config-model"]');
    await expect(model).toHaveValue('fast');

    await model.selectOption('smart');

    // The agent returns its full config state, which is authoritative;
    // this asserts the change round-tripped rather than stuck locally.
    await expect(model).toHaveValue('smart', { timeout: 15_000 });
  });

  test('slash commands stay out of the way until you type one', async ({ page, router }) => {
    const win = await openAgent(page, router.url, 'say something');
    await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

    const list = win.locator('[data-testid="agent-commands"]');
    const composer = win.locator('textarea');

    // The agent offers commands, but an idle composer shows none of them.
    await expect(list).toBeHidden();

    await composer.fill('/');
    await expect(list).toBeVisible();
    await expect(list.getByRole('button', { name: '/review' })).toBeVisible();

    // Typing narrows it.
    await composer.fill('/re');
    await expect(list.getByRole('button', { name: '/review' })).toBeVisible();
    await expect(list.getByRole('button', { name: '/compact' })).toBeHidden();

    // A completed command is no longer a search.
    await composer.fill('/review the diff');
    await expect(list).toBeHidden();
  });

  test('the menus carry the settings, the transcript and the history', async ({ page, router }) => {
    const win = await openAgent(page, router.url, 'say something');
    await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

    // Session: one group per setting the agent exposes, current marked.
    await win.locator('[data-testid="ai-menubar-session"]').click();
    const sessionMenu = page.locator('[data-testid="ai-menu-session"]');
    await expect(sessionMenu).toBeVisible();
    await sessionMenu.locator('[data-testid="ai-menu-config-model-smart"]').click();
    await expect(win.locator('[data-testid="agent-config-model"]')).toHaveValue('smart', { timeout: 15_000 });

    // Edit: copying the transcript is a real action, not a stub.
    await win.locator('[data-testid="ai-menubar-edit"]').click();
    await expect(page.locator('[data-testid="ai-menu-copy-all"]')).toBeVisible();
    await page.keyboard.press('Escape');

    // History: earlier sessions live here now, not in the sidebar.
    await win.locator('[data-testid="ai-menubar-history"]').click();
    await expect(page.locator('[data-testid="ai-menu-history"]')).toBeVisible();
    await page.keyboard.press('Escape');

    // The agent names its own session on session_info_update, and that
    // name becomes the WINDOW title — no extra model call, it arrives.
    // Asserted on the chrome rather than the sidebar, whose Agents
    // section may be collapsed.
    await expect(page.getByText('Fake conversation').first()).toBeVisible({ timeout: 15_000 });
  });

  test('closing a session window asks what to do with the agent', async ({ page, router }) => {
    const win = await openAgent(page, router.url, 'say something');
    await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

    // The window's own close button, i.e. the path a user takes.
    await page.locator('[data-testid="window-close"]').first().click();

    // Three outcomes, because dismissing must not silently pick a
    // destructive one.
    const dialog = page.locator('[data-testid="ai-close-confirm"]');
    await expect(dialog).toBeVisible({ timeout: 10_000 });
    await expect(dialog.getByRole('button', { name: 'Detach' })).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Terminate' })).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Keep open' })).toBeVisible();
  });

  test('Detach from the close dialog actually closes the window', async ({ page, router }) => {
    // The path a user takes — the window's X, then Detach — had no
    // coverage past "the dialog appeared", and a detach that leaves the
    // window on screen reads as a dead app: the session IS detached, so
    // every button in the window now talks about something that is no
    // longer there.
    test.setTimeout(60_000);
    const win = await openAgent(page, router.url, 'say something');
    await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

    const cursor = router.logCursor();
    await page.locator('[data-testid="window-close"]').first().click();
    const dialog = page.locator('[data-testid="ai-close-confirm"]');
    await expect(dialog).toBeVisible({ timeout: 10_000 });
    await dialog.getByRole('button', { name: 'Detach' }).click();

    // BE half: the session detached rather than ending.
    await router.waitForLog(/agentd: acp detached key=/, 15_000, cursor);
    expect(router.log().slice(cursor)).not.toMatch(/acp session ended/);

    // FE half: the window is gone from the desktop.
    await expect(page.locator('wash-app-ai')).toHaveCount(0, { timeout: 15_000 });
  });

  test('Terminate from the close dialog closes the window and ends the session', async ({
    page,
    router,
  }) => {
    test.setTimeout(60_000);
    const win = await openAgent(page, router.url, 'say something');
    await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

    const cursor = router.logCursor();
    await page.locator('[data-testid="window-close"]').first().click();
    const dialog = page.locator('[data-testid="ai-close-confirm"]');
    await expect(dialog).toBeVisible({ timeout: 10_000 });
    await dialog.getByRole('button', { name: 'Terminate' }).click();

    await router.waitForLog(/agentd: acp session ended key=/, 15_000, cursor);
    await expect(page.locator('wash-app-ai')).toHaveCount(0, { timeout: 15_000 });
  });
});
