// A transcript watcher survives agentd's TTL (docs/Review-findings.md,
// 2026-09-08 sweep, "Transcript freezes after 60 s").
//
// agentd drops a transcript watcher it has not heard from within
// WatcherTTL. wash-ai used to re-affirm only on some of the paths that set
// a window's session key, so a window that reached its session another
// way went quiet after a minute: the status line kept moving (the roster
// push has its own keepalive) while the transcript stopped at the first
// event after the TTL. The keepalive now starts once per controller from
// onReady and re-affirms whatever key the window holds.
//
// The window that exposed it — one that picked its session from a roster
// inside itself — no longer exists: a session has exactly one controller,
// opened by agentd and handed its key with `attach`. The property is the
// same one, though, and it is the one a user feels: the controller keeps
// receiving its own transcript long after the TTL, without re-subscribing.
//
// The TTL is shrunk through WASH_AGENT_WATCHER_TTL (one seam, read by
// agentd and every host from internal/agentclient), so the test waits past
// several TTLs in seconds rather than minutes.

import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, startAgentSession } from '../fixtures/agents';

const TTL_MS = 3_000;

test.use({
  routerOpts: {
    apps: [...AGENT_APPS],
    extraEnv: {
      PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}`,
      WASH_AGENT_WATCHER_TTL: `${TTL_MS}ms`,
    },
  },
});

test('a session controller keeps receiving its transcript past the TTL', async ({ page, router }) => {
  test.setTimeout(90_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  const cursor = router.logCursor();
  const win = await startAgentSession(page, 'first prompt before the ttl');
  await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

  // The controller's instance, from its ready line: the manager is a
  // wash-ai process too, and the BE assertions below are about the
  // controller's watcher, not anything the manager does.
  const ready = [...router.log().slice(cursor).matchAll(/wash-ai ready instance=(\S+) manager=false/g)].map((m) => m[1]);
  expect(ready, 'exactly one controller announced itself').toHaveLength(1);
  const inst = ready[0];
  const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  // agentd handed it the session and it subscribed, exactly once.
  await router.waitForLog(new RegExp(`transcript subscribed instance=${esc(inst)} key=\\S+ replay=`), 10_000, cursor);

  // Idle past several TTLs. Nothing happens on screen; the question is
  // whether agentd still counts the controller as a watcher afterwards.
  await page.waitForTimeout(TTL_MS * 3);

  // BE half: the watcher was never expired, and its keepalives were
  // keepalives — a known instance re-affirming logs nothing and gets no
  // second snapshot. A re-subscribe AFTER an expiry would show as another
  // "subscribed" line; a dropped one as "expired".
  const since = router.log().slice(cursor);
  expect(since).not.toMatch(new RegExp(`transcript watcher expired instance=${esc(inst)}`));
  expect(since.match(new RegExp(`transcript subscribed instance=${esc(inst)} `, 'g'))?.length ?? 0).toBe(1);

  // FE half: a new turn still lands. The reply is the decisive part — it
  // exists only as agentd's transcript events, so it cannot be a local
  // echo of what was typed.
  const composer = win.locator('textarea');
  await composer.fill('second prompt after the ttl');
  await composer.press('Enter');
  await expect(win.getByText('second prompt after the ttl')).toBeVisible({ timeout: 20_000 });
  await expect(win.getByText('Hello from the fake agent.')).toHaveCount(2, { timeout: 20_000 });

  // The TTL is only enforced when agentd next pushes an event, so the
  // decisive BE check is after the push: the controller was not expired on
  // its way out. Without the keepalive this is where the log says
  // "transcript watcher expired" and the FE half above goes red.
  const after = router.log().slice(cursor);
  expect(after).not.toMatch(new RegExp(`transcript watcher expired instance=${esc(inst)}`));
  expect(after.match(new RegExp(`transcript subscribed instance=${esc(inst)} `, 'g'))?.length ?? 0).toBe(1);
});
