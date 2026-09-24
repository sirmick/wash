// The Agents manager's Running pane (docs/SIDEBAR.md M2a, docs/AGENT_APP.md §9).
//
// M1 gave the rail per-host counts. M2 moved the roster — and next the
// verbs — into an app, because that is where they can be correct: an app
// talking to its own host's agentd carries a router-attested sender, so
// `launchOn(B, …)` gets B's roster with no new addressing. This spec is the
// local half of that claim; agent-remote-roster.spec.ts is the two-router
// half.
//
// The roster used to be a pane inside every Agent window, beside that
// window's session, and a row click re-pointed the window (master-detail).
// That is gone: com.wash.agents is a singleton manager whose Running pane
// is the ONE roster, and each session has exactly one controller window
// (com.wash.ai) that agentd opens. So "switch this window's detail pane"
// became "take me to that session's controller", and "New session opens
// another window" became "a new session gets its own controller".
//
// Same fake adapter as agent-session.spec.ts: e2e/fixtures/acp-fake on a
// PATH this test controls, so the suite needs no API key and no network.

import type { Locator, Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, openAgents, startAgentSession, windowOf } from '../fixtures/agents';

test.use({
  routerOpts: {
    apps: [...AGENT_APPS],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

/**
 * shrinkWin makes an app's window `height` tall.
 *
 * Through the shell's own WM API rather than by dragging the resize grip:
 * the windows open about as tall as the viewport, so the bottom-right grip
 * can be under the taskbar or off screen, with nothing to grab.
 */
async function shrinkWin(page: Page, app: Locator, height: number) {
  const id = Number(await app.getAttribute('data-wash-window'));
  const box = (await app.boundingBox())!;
  await page.evaluate(
    (a) => window.wash.resizeWindow(a.id, a.w, a.h),
    { id, w: Math.round(box.width), h: height },
  );
  await expect.poll(async () => (await app.boundingBox())!.height).toBeLessThanOrEqual(height);
}

/** slotOverflow is how far an app's content outgrows its window frame's slot. */
async function slotOverflow(app: Locator): Promise<number> {
  return app.evaluate((el) => {
    const slot = el.parentElement!;
    return slot.scrollHeight - slot.clientHeight;
  });
}

/**
 * nextControllerKey reads the roster key agentd opened a controller for, from the log
 * written after `from`. Rows are addressed by that key, and nothing on
 * screen says which row is which session more reliably than it does.
 */
async function nextControllerKey(router: { waitForLog(re: RegExp, t?: number, from?: number): Promise<string> }, from: number) {
  const line = await router.waitForLog(/agentd: opening controller key=acp:\d+/, 20_000, from);
  return line.replace('agentd: opening controller key=', '');
}

const rowSel = '[data-testid^="agents-row-"]';

test('each manager pane scrolls its own content rather than growing the window', async ({ page, router }) => {
  // Several panes, several scrollbars: the launcher and the running list
  // each scroll their own content, and neither the window frame nor the
  // neighbouring pane moves when one of them does. What this guards
  // against is the layout quietly handing the scroll upwards — a split
  // body whose row grows with its tallest pane leaves the window frame
  // (whose app slot is `overflow: auto`) holding the only scrollbar, and
  // one scrollbar moves every pane at once.
  test.setTimeout(60_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  // Two sessions, not one: a single row can come out exactly as tall as
  // the shrunk pane (98 = 98 on CI's fonts), and then nothing overflows.
  await startAgentSession(page, 'a session to look at');
  await startAgentSession(page, 'and another, so the list outgrows its pane');
  const manager = await openAgents(page);
  const pane = manager.locator('[data-testid="ai-roster-pane"]');
  await expect(pane.locator(rowSel)).toHaveCount(2, { timeout: 20_000 });

  // Short enough that the list is taller than its pane — the situation the
  // clamp exists for. Asserted below, so a window that turns out to be
  // roomy enough fails loudly instead of passing on nothing.
  await shrinkWin(page, manager, 160);

  const m = await pane.evaluate((el) => {
    const body = el.parentElement!;
    return { content: el.scrollHeight, pane: el.clientHeight, body: body.clientHeight };
  });
  expect(m.content, 'the running list must overflow its pane for this test to mean anything')
    .toBeGreaterThan(m.body);
  // The pane is bounded by the window, not the other way round...
  expect(m.pane).toBeLessThanOrEqual(m.body + 1);
  // ...so the overflow is the pane's own to scroll.
  expect(m.content).toBeGreaterThan(m.pane);
  const scrolled = await pane.evaluate((el) => {
    el.scrollTop = 9999;
    return el.scrollTop;
  });
  expect(scrolled, 'the running pane scrolls itself').toBeGreaterThan(0);

  // The launcher is its own scroller too: at this height the form cannot
  // fit, and it must be the pane that scrolls, not the window.
  const launcher = manager.locator('[data-testid="agents-new-pane"]');
  const l = await launcher.evaluate((el) => ({ view: el.clientHeight, content: el.scrollHeight }));
  expect(l.content, 'the launcher must overflow for this to mean anything').toBeGreaterThan(l.view);

  // (History's own containment is not asserted here: with one running
  // session its list is empty, and an empty list fits any pane — the check
  // would pass on nothing.)

  // Nothing outside the panes scrolls. An app that lets its layout outgrow
  // the window hands the frame the scrollbar — and then one scrollbar moves
  // every pane at once, which is the coupling this is all about.
  expect(await slotOverflow(manager), 'the window frame must not be the scroller for the panes')
    .toBeLessThanOrEqual(1);

  // And a wheel that runs past the end of one pane stays in it: with
  // overscroll containment the gesture cannot chain outwards and drag the
  // neighbouring pane with it.
  await pane.hover();
  const before = await launcher.evaluate((el) => el.scrollTop);
  const slotBefore = await manager.evaluate((el) => el.parentElement!.scrollTop);
  await page.mouse.wheel(0, 400);
  expect(await launcher.evaluate((el) => el.scrollTop)).toBe(before);
  expect(await manager.evaluate((el) => el.parentElement!.scrollTop)).toBe(slotBefore);
});

test('the controller\'s transcript scrolls itself rather than growing the window', async ({ page, router }) => {
  // The other half of the old two-pane window: the transcript is its own
  // scroller, with its own viewport inside the window rather than a box as
  // tall as its content.
  test.setTimeout(60_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  const win = await startAgentSession(page, 'say something');
  // The fake's reply (heading, table, image) is what makes it tall.
  await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });
  await expect(win.locator('[data-testid="ai-roster-pane"]')).toHaveCount(0);
  await shrinkWin(page, win, 200);

  const body = await win.locator('[data-testid="ai-body"]').evaluate((el) => el.clientHeight);
  const transcript = win.locator('[data-testid="agent-transcript"]');
  const t = await transcript.evaluate((el) => ({ view: el.clientHeight, content: el.scrollHeight }));
  expect(t.view).toBeLessThanOrEqual(body);
  expect(t.content, 'the transcript must overflow for this to mean anything').toBeGreaterThan(t.view);
  expect(await slotOverflow(win), 'the window frame must not be the transcript\'s scroller')
    .toBeLessThanOrEqual(1);
});

test('the Running pane lists agentd\'s sessions and a row takes you to that session\'s controller', async ({ page, router }) => {
  test.setTimeout(90_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  // The pane is furniture: always there, never toggled. It used to appear
  // on rules (more than one session, a question elsewhere, an empty
  // window) and each rule was a guess about when a list earns its width —
  // which is what made the way back to your own agent depend on knowing a
  // button existed.
  let from = router.logCursor();
  const first = await startAgentSession(page, 'first session');
  const firstKey = await nextControllerKey(router, from);
  const manager = page.locator('wash-app-agents');
  const pane = manager.locator('[data-testid="ai-roster-pane"]');
  await expect(pane.locator(rowSel)).toHaveCount(1, { timeout: 20_000 });

  // A second session gets a second controller, and the one roster lists
  // BOTH — the roster is agentd's, not a window's, which is the whole reason
  // it can answer for a host rather than for a window.
  from = router.logCursor();
  const second = await startAgentSession(page, 'second session');
  const secondKey = await nextControllerKey(router, from);
  expect(secondKey).not.toBe(firstKey);
  await expect(pane.locator(rowSel)).toHaveCount(2, { timeout: 20_000 });
  await expect(page.locator('wash-app-ai')).toHaveCount(2);
  await expect(first.getByText('first session')).toBeVisible({ timeout: 20_000 });
  await expect(second.getByText('second session')).toBeVisible({ timeout: 20_000 });

  // The manager is a roster, not a session view: it has no row of its own
  // to mark as current.
  await expect(pane.locator(`${rowSel}[data-active="true"]`)).toHaveCount(0);

  // Bury the first controller so arriving at it is observable, then raise
  // the manager. Through the WM API: the manager and the newer controller
  // overlap its title bar, so there is no minimize button to click.
  const firstWin = Number(await first.getAttribute('data-wash-window'));
  await page.evaluate((id) => window.wash.minimizeWindow(id), firstWin);
  await expect(first).toBeHidden();
  await openAgents(page);

  const clickFrom = router.logCursor();
  await pane.locator(`[data-testid="agents-row-${firstKey}"]`).click();

  // BE half: agentd resolved the row's key to the one controller holding
  // it, rather than re-pointing the manager or spawning another window.
  await router.waitForLog(new RegExp(`agentd: focus key=${firstKey} raising controller=`), 15_000, clickFrom);

  // FE half: that controller is back on screen showing its own session, and
  // it is still the only window for it.
  await expect(first).toBeVisible({ timeout: 15_000 });
  await expect(first.getByText('first session')).toBeVisible();
  await expect(page.locator('wash-app-ai')).toHaveCount(2);
  expect(router.log().slice(clickFrom)).not.toMatch(/agentd: opening controller/);
});

test('a roster verb acts on the row it was picked from, and leaves other controllers alone', async ({ page, router }) => {
  // The claim M2b makes: verbs are key-addressed, so the manager can end
  // a session it is not showing — it shows none — including one with no
  // window at all. The rail could only ever do this for the LOCAL host, and
  // only by gatewaying through the session BE.
  test.setTimeout(90_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  let from = router.logCursor();
  await startAgentSession(page, 'session to be detached');
  const targetKey = await nextControllerKey(router, from);
  from = router.logCursor();
  await startAgentSession(page, 'the session that carries on');
  await nextControllerKey(router, from);

  // Bind the surviving controller by its CONTENT, not by index. Detaching
  // the other session closes its controller, and `nth()` is evaluated fresh
  // on every use — so the moment that window leaves the DOM, indices shift
  // and every locator derived from them points at something else.
  const survivor = page.locator('wash-app-ai').filter({ hasText: 'the session that carries on' });
  await expect(survivor).toHaveCount(1, { timeout: 20_000 });

  const manager = await openAgents(page);
  const pane = manager.locator('[data-testid="ai-roster-pane"]');
  await expect(pane.locator(rowSel)).toHaveCount(2, { timeout: 20_000 });
  const row = pane.locator(`[data-testid="agents-row-${targetKey}"]`);

  // The verbs live behind the row's menu (a rail row was too narrow for a
  // button strip, and a menu can render an inapplicable verb disabled
  // rather than vanishing). It portals to document.body, so it is addressed
  // from the page rather than from inside the row.
  const cursor = router.logCursor();
  await row.locator('[data-testid="agents-verbs-btn"]').click();
  const menu = page.locator('[data-testid="agents-row-actions"]');
  await expect(menu).toBeVisible();
  await menu.locator('[data-testid="agents-menu-detach"]').click();

  // agentd acted on the key it was given, which is the row's.
  const esc = targetKey.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  await router.waitForLog(new RegExp(`wash-ai: roster detach key=${esc}`), 10_000, cursor);
  await router.waitForLog(new RegExp(`agentd: acp detached key=${esc}`), 10_000, cursor);

  // And the row says so. Asserting the row is still VISIBLE would prove
  // nothing — it was visible before the detach too. What changed is what
  // the row now claims about itself.
  await expect(row).toHaveAttribute('title', /Detached/, { timeout: 10_000 });

  // Detach means "no window": that session's controller went, and ONLY
  // that one — the other controller still shows its own session.
  await expect(page.locator('wash-app-ai')).toHaveCount(1, { timeout: 15_000 });
  await expect(survivor.getByText('the session that carries on')).toBeVisible();
});

test('a reopened manager shows the sessions you already have, one click from each', async ({ page, router }) => {
  // Opening the app while a session of yours is detached used to give a
  // blank new-session form, with the way back hidden behind a toggle you
  // had to know about (docs/AGENT_UX.md N1). The manager always lists what
  // is running, so there is nothing to find.
  test.setTimeout(90_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  const from = router.logCursor();
  const ctrl = await startAgentSession(page, 'something to come back to');
  const key = await nextControllerKey(router, from);
  await expect(ctrl.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

  // Detach it through the controller's own close: the session keeps running
  // with no window on it.
  await windowOf(page, ctrl).locator('[data-testid="window-close"]').click();
  await page.locator('[data-testid="ai-close-confirm"]').getByRole('button', { name: 'Detach' }).click();
  await expect(page.locator('wash-app-ai')).toHaveCount(0, { timeout: 15_000 });

  // Close the manager too, so what comes back is a fresh instance with no
  // FE state left over from the start.
  const manager = page.locator('wash-app-agents');
  await windowOf(page, manager).locator('[data-testid="window-close"]').click();
  await expect(manager).toHaveCount(0, { timeout: 15_000 });

  // Now open the app fresh. The launcher is there, but so is the list...
  const fresh = await openAgents(page);
  const row = fresh.locator(`[data-testid="ai-roster-pane"] [data-testid="agents-row-${key}"]`);
  await expect(row).toBeVisible({ timeout: 20_000 });
  await expect(row).toHaveAttribute('title', /Detached/);

  // ...and one click on the row is the whole way back, transcript and all.
  const clickFrom = router.logCursor();
  await row.click();
  await router.waitForLog(new RegExp(`agentd: opening controller key=${key}`), 15_000, clickFrom);
  await expect(page.locator('wash-app-ai')).toHaveCount(1, { timeout: 20_000 });
  await expect(page.locator('wash-app-ai').getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });
});

test('a new session gets its own controller rather than hijacking an existing one', async ({ page, router }) => {
  // The old window cleared its own view to show a launcher, which left its
  // BE still streaming the old transcript into a "new" session. Now the
  // launcher lives in the manager and a start always lands in a controller
  // of its own.
  test.setTimeout(60_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  const first = await startAgentSession(page, 'keep me');
  await expect(first.getByText('keep me')).toBeVisible({ timeout: 20_000 });

  const second = await startAgentSession(page, 'a fresh one');
  await expect(page.locator('wash-app-ai')).toHaveCount(2, { timeout: 20_000 });
  await expect(page.locator('wash-app-agents')).toHaveCount(1);

  // The first controller is still on its own session, composer and all.
  await expect(first.getByText('keep me')).toBeVisible();
  await expect(first.getByText('a fresh one')).toHaveCount(0);
  await expect(first.locator('textarea')).toBeVisible();
  await expect(second.getByText('a fresh one')).toBeVisible({ timeout: 20_000 });
  await expect(second.getByText('keep me')).toHaveCount(0);
});

test('the manager split is dragged, not toggled', async ({ page, router }) => {
  // The running list is always there and resized by a divider — the same
  // split every other two-pane wash app uses. (The old per-window sessions
  // pane persisted its width through the window's BE; the manager's split
  // is deliberately local to the window — see managerSplit in
  // apps/ai/fe/src/main.tsx — so there is no reload half to test.)
  test.setTimeout(60_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  const manager = await openAgents(page);
  const pane = manager.locator('[data-testid="agents-running-pane"]');
  await expect(pane).toBeVisible();

  const before = (await pane.boundingBox())!.width;
  const grip = (await manager.locator('[data-testid="agents-manager-splitter"]').boundingBox())!;

  // Drag the divider LEFT, which gives the running pane the width. Mouse
  // move in two steps: one is sometimes coalesced away before the listener
  // attaches.
  await page.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2);
  await page.mouse.down();
  await page.mouse.move(grip.x - 120, grip.y + grip.height / 2, { steps: 2 });
  await page.mouse.up();

  await expect.poll(async () => (await pane.boundingBox())!.width).toBeGreaterThan(before + 40);
});
