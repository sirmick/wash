// The payoff (docs/SIDEBAR.md M3c): the rail points at a job on B, and the
// door opens the file manager THERE.
//
// Before the relocation the rail carried the job list and a cancel button,
// and both were local-only: a shell-originated send has no router-attested
// sender, so it gatewayed through the session BE, whose SendAppMsgTo
// resolves inside its own router. A copy running on B could be counted but
// never cancelled — the rail would happily show you the row and then do
// nothing you meant. fm subscribes to its own host's bulk singleton, so
// launchOn(origin, com.wash.fm) gets working job control with no new
// addressing.
//
// Two routers stand in for host A (the desktop) and host B (what an
// `ssh -L` tunnel would reach), wired with ?peer= as remote-apps.spec.ts
// does. B runs --no-session, which is exactly why a session-BE gateway
// could never have served it.

import { test, expect } from '@playwright/test';
import { startRouter, stopRouter, type RouterHandle } from '../fixtures/router';

/**
 * Park an in-flight job in a host's bulk service. Reported rather than
 * enqueued, and deliberately: an enqueued op finishes before a window can
 * open, and the door only exists while work is in flight. job_report is
 * the same door fm's own uploads use for externally-driven work, so the
 * row is exactly the shape a real long copy produces — it simply never
 * advances.
 */
async function reportJob(r: RouterHandle, jobID: string, name: string): Promise<void> {
  const bulk = await r.controlRequest({ t: 'launch', app_id: 'com.wash.bulk' });
  const inst = String(bulk.instance_id ?? '');
  expect(inst).not.toBe('');
  await r.controlRequest({
    t: 'msg', instance_id: inst,
    data: {
      kind: 'job_report', job_id: jobID, op: 'copy',
      paths: [`${r.fmRoot}/${name}`], dest: `${r.fmRoot}/dest`,
      status: 'running', done: 1, total: 10,
    },
  });
}

test('the bulk door opens fm on the host that owns the job', async ({ page }) => {
  test.setTimeout(90_000);
  let a: RouterHandle | undefined;
  let b: RouterHandle | undefined;
  try {
    a = await startRouter({ apps: ['session', 'fm', 'bulk', 'notify'], fmRoot: true });
    b = await startRouter({
      apps: ['fm', 'bulk', 'notify'],
      extraArgs: ['--no-session', '--allow-cross-origin'],
      fmRoot: true,
    });

    // A job on each host, named differently so a mix-up is visible rather
    // than merely suspected.
    await reportJob(a, 'e2e-on-a', 'copying-on-A');
    await reportJob(b, 'e2e-on-b', 'copying-on-B');

    const bPort = new URL(b.url).port;
    await page.goto(`${a.url}?peer=${encodeURIComponent(`remoteB@ws://127.0.0.1:${bPort}/ws`)}`);
    await expect(page.locator('[data-testid="wash-cam"]')).toBeAttached({ timeout: 10_000 });

    // The rail knows B has work in flight — that is M1's awareness channel
    // reaching across, with no subscribe of the rail's own.
    //
    // No click: A's own running job pops the section open by itself. Since
    // M6 that rise-detection comes from hostgw for LOCAL as well as remote
    // hosts, so this doubles as the regression test for the fold.
    //
    // The earlier version clicked when the body was missing, which raced —
    // between the count() and the click the section could auto-open, and
    // the click then TOGGLED IT SHUT. Waiting is both simpler and a
    // stronger assertion.
    const body = page.locator('[data-testid="sidebar-section-body-bulk"]');
    await expect(body).toBeVisible({ timeout: 20_000 });

    // A door to B specifically, and one to A — the rail names both because
    // both have work. A quiet host would have neither.
    const openB = page.locator('[data-testid="bulk-open-remoteB"]');
    await expect(openB).toBeVisible({ timeout: 20_000 });
    await expect(openB).toContainText('remoteB');
    await expect(page.locator('[data-testid="bulk-open-local"]')).toBeVisible();

    // Cross it. launchOn carries the origin; nothing else had to change.
    await openB.click();

    // A window opens on B, served by B's bundle — the per-origin mangled
    // tag is the proof it is B's code, not A's.
    const fmB = page.locator('wash-app-fm-remoteb').first();
    await expect(fmB).toBeAttached({ timeout: 30_000 });

    // B's job is listed there, ready to be cancelled on the host that is
    // actually running it.
    const strip = fmB.locator('[data-testid="fm-jobs"]');
    await expect(strip).toBeVisible({ timeout: 30_000 });
    await expect(strip.locator('[data-testid="bulk-job-e2e-on-b"]')).toBeVisible({ timeout: 30_000 });

    // And A's job is nowhere in B's window. This is the regression the
    // whole plan was written around: the rail used to answer for A no
    // matter which host you meant.
    await expect(strip.locator('[data-testid="bulk-job-e2e-on-a"]')).toHaveCount(0);
  } finally {
    if (a) await stopRouter(a);
    if (b) await stopRouter(b);
  }
});
