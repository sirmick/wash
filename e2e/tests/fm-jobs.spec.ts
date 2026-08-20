// fm's Jobs strip (docs/SIDEBAR.md M3a).
//
// The queue belongs to com.wash.bulk, not to the window that started a
// job: closing fm has never killed a copy, and the job keeps running with
// nothing pointing at it. That is why the queue could not simply live in
// the fm window — and why the desktop rail grew a cancel button, which
// could only ever reach the LOCAL host.
//
// So the claim under test is not "fm shows its own uploads". It is that
// ANY fm window on this host lists the SERVICE's jobs, including one
// started somewhere else entirely, and can cancel it.

import { test, expect } from '../fixtures/router';

test.use({ routerOpts: { apps: ['session', 'fm', 'bulk'], fmRoot: true } });

test('fm lists the host\'s bulk jobs, not just its own, and cancels them', async ({ page, router }) => {
  test.setTimeout(60_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  // A job nobody's fm window started: enqueued straight into the service
  // over the control socket, exactly as a job whose originating window has
  // since closed would look.
  const bulk = await router.controlRequest({ t: 'launch', app_id: 'com.wash.bulk' });
  const bulkInst = String(bulk.instance_id ?? '');
  expect(bulkInst).not.toBe('');

  // Reported rather than enqueued, and deliberately: an enqueued delete
  // finishes before a window can open, and this spec is about a job that
  // is still in flight. job_report is the same door fm's own uploads use
  // for externally-driven work, so the row is exactly the shape a real
  // long copy produces — it simply never advances.
  await router.controlRequest({
    t: 'msg', instance_id: bulkInst,
    data: {
      kind: 'job_report', job_id: 'e2e-inflight', op: 'copy',
      paths: [`${router.fmRoot}/big`], dest: `${router.fmRoot}/dest`,
      status: 'running', done: 1, total: 10,
    },
  });

  // Now open fm. It subscribes to the service on ready, so the job is
  // already there to be found — no window had to be open when it started.
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Files', exact: true }).click();
  const fm = page.locator('wash-app-fm').first();
  await expect(fm).toBeVisible();

  const strip = fm.locator('[data-testid="fm-jobs"]');
  await expect(strip).toBeVisible({ timeout: 20_000 });
  await expect(strip.locator('[data-testid^="bulk-job-"]').first()).toBeVisible({ timeout: 20_000 });
});

test('cancelling from fm reaches the service, with fm as the attested sender', async ({ page, router }) => {
  test.setTimeout(60_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  const bulk = await router.controlRequest({ t: 'launch', app_id: 'com.wash.bulk' });
  const bulkInst = String(bulk.instance_id ?? '');
  await router.controlRequest({
    t: 'msg', instance_id: bulkInst,
    data: {
      kind: 'job_report', job_id: 'e2e-cancel-me', op: 'copy',
      paths: [`${router.fmRoot}/big`], dest: `${router.fmRoot}/dest`,
      status: 'running', done: 1, total: 10,
    },
  });

  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Files', exact: true }).click();
  const fm = page.locator('wash-app-fm').first();
  await expect(fm.locator('[data-testid="fm-jobs"]')).toBeVisible({ timeout: 20_000 });

  // This is the verb that moved. Before M3 it lived in the desktop rail
  // and routed through the session BE gateway, so it resolved inside the
  // local router and could never have cancelled a job on another host.
  const from = router.logCursor();
  await fm.locator('[data-testid="bulk-cancel-e2e-cancel-me"]').click();

  // FE → fm → bulk, with fm's own instance as the router-attested sender.
  await router.waitForLog(/wash-fm: cancel job=e2e-cancel-me/, 10_000, from);

  // The row does NOT vanish on the click, and that is the service being
  // right rather than the UI being slow: an externally-driven job (an fm
  // upload, or this stand-in for one) is not in bulk's worker queue, so
  // bulk relays the cancel to the job's OWNER and the job ends when the
  // owner says it ended. Asserting the row disappears on click would be
  // asserting a lie about who owns the work.
  //
  // So: play the owner, and confirm the strip follows the service.
  await router.controlRequest({
    t: 'msg', instance_id: bulkInst,
    data: {
      kind: 'job_report', job_id: 'e2e-cancel-me', op: 'copy',
      paths: [`${router.fmRoot}/big`], dest: `${router.fmRoot}/dest`,
      status: 'cancelled', done: 1, total: 10,
    },
  });
  await expect(fm.locator('[data-testid="bulk-job-e2e-cancel-me"]'))
    .toHaveCount(0, { timeout: 20_000 });
});
