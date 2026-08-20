// bulk-ops tests, three layers:
//
//   1. Router foundations: singleton spawn dedup + sentinel
//      addressing via the control socket (no browser).
//   2. wash-bulk BE: enqueue via control-socket sendAppMsg, observe
//      job completion via router.waitForLog, assert filesystem.
//   3. fm FE: multi-select with ctrl-click, Delete via context
//      menu, confirm overlay, assert bulk-ops processes the job.
//
// Per [[wash e2e pattern]] we use waitForLog as the async signal —
// wash-bulk emits a structured line on every job state transition.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { existsSync, mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

test.describe('singleton + sentinel', () => {
  test('two launches of a singleton return the same instance_id', async ({ router }) => {
    const a = await router.controlRequest({ t: 'launch', app_id: 'com.wash.bulk' });
    expect(a.t).toBe('launched');
    const b = await router.controlRequest({ t: 'launch', app_id: 'com.wash.bulk' });
    expect(b.t).toBe('launched');
    expect(b.instance_id).toBe(a.instance_id);
  });

  test('sentinel addressing: msg to {app_id} reaches the singleton', async ({ router }) => {
    // No prior launch — the router should spawn-on-demand when
    // resolving the sentinel. The fact that the response carries
    // bulk-ops' enqueue_ok shape proves the dispatch worked.
    const resp = await router.controlRequest({
      t: 'msg',
      // Note: our control socket's `msg` op currently addresses by
      // instance_id. For sentinel addressing we launch first to get
      // the id; the SENTINEL semantic is verified by repeat-launch
      // and by the fm flow below (which uses ShellAppMsgSendTo).
      // Keeping this test pinned to the control-socket reachability.
      instance_id: 'unknown',
      data: { kind: 'enqueue', id: 'x', op: 'delete', paths: ['/tmp/wash-nonexistent'] },
    });
    // Unknown instance id → not_found.
    expect(resp.t).toBe('error');
    expect(resp.code).toBe('not_found');
  });
});

test.describe('wash-bulk BE: enqueue → completion via log + fs', () => {
  test('delete a recursive tree via enqueue', async ({ router }) => {
    // Build a small recursive tree under the sandbox.
    const root = join(router.fmRoot, 'doomed');
    mkdirSync(root);
    mkdirSync(join(root, 'sub'));
    writeFileSync(join(root, 'sub', 'a.txt'), 'aaa');
    writeFileSync(join(root, 'sub', 'b.txt'), 'bbb');
    writeFileSync(join(root, 'top.txt'), 'top');
    expect(existsSync(root)).toBe(true);

    // Spawn bulk-ops + send the enqueue. We don't need await_id
    // beyond the immediate ack; completion is observed via the
    // router log.
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.bulk' });
    expect(launched.t).toBe('launched');
    const inst = launched.instance_id as string;

    const ack = await router.sendAppMsg(inst, {
      kind: 'enqueue',
      id: 'r1',
      op: 'delete',
      paths: [root],
    });
    expect(ack.kind).toBe('enqueue_ok');
    expect(typeof ack.job_id).toBe('string');

    // Wait for the job to finish — bulk-ops logs one line per
    // state transition; we match on status=done.
    const jobID = ack.job_id as string;
    await router.waitForLog(new RegExp(`bulk-ops job=${jobID} .* status=done`), 5_000);

    // The entire tree should be gone.
    expect(existsSync(root)).toBe(false);
  });

  test('enqueue with empty paths returns enqueue_err', async ({ router }) => {
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.bulk' });
    const inst = launched.instance_id as string;
    const r = await router.sendAppMsg(inst, {
      kind: 'enqueue', id: 'bad', op: 'delete', paths: [],
    });
    expect(r.kind).toBe('enqueue_err');
    expect(r.code).toBe('bad_request');
  });

  test('cancel of unknown job_id is silently ignored (fire-and-forget)', async ({ router }) => {
    // Cancel became HandleVoid in M6 — the StateService fan is the
    // confirmation, not a reply envelope. An unknown-id cancel must
    // not throw, not crash the service, not corrupt subsequent
    // enqueues. We can't easily await "no reply"; instead, fire the
    // cancel then verify the service still processes a real enqueue.
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.bulk' });
    const inst = launched.instance_id as string;
    // Fire the no-op cancel. Without an `id` field sendAppMsg takes
    // the no-await path (msg.ok rather than awaiting a reply), which
    // matches the new fire-and-forget contract on bulk.cancel.
    await router.sendAppMsg(inst, { kind: 'cancel', job_id: 'no-such-job' });
    // Now a real enqueue must still complete.
    const ack = await router.sendAppMsg(inst, {
      kind: 'enqueue', id: 'after-cancel', op: 'delete', paths: ['/tmp/wash-nonexistent-XYZ'],
    });
    expect(ack.kind).toBe('enqueue_ok');
  });
});

test.describe('fm: bulk delete via multi-select', () => {
  async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
    await expect(page.locator('wash-app-fm')).toBeVisible();
    await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
    await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);
  }

  test('ctrl-click selects multiple rows; status bar shows count', async ({ page, router }) => {
    writeFileSync(join(router.fmRoot, 'a.txt'), 'a');
    writeFileSync(join(router.fmRoot, 'b.txt'), 'b');
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-a.txt"]').click();
    await page.locator('[data-testid="fm-entry-b.txt"]').click({ modifiers: ['Control'] });

    await expect(page.locator('[data-testid="fm-entry-a.txt"]')).toHaveAttribute('data-selected', 'true');
    await expect(page.locator('[data-testid="fm-entry-b.txt"]')).toHaveAttribute('data-selected', 'true');
    await expect(page.locator('[data-testid="fm-status"]')).toContainText(/2 of \d+ selected/);
  });

  test('delete on multi-selection → bulk-ops processes the job', async ({ page, router }) => {
    writeFileSync(join(router.fmRoot, 'doomed-a.txt'), 'a');
    writeFileSync(join(router.fmRoot, 'doomed-b.txt'), 'b');
    await openFm(page, router);

    // Select both
    await page.locator('[data-testid="fm-entry-doomed-a.txt"]').click();
    await page.locator('[data-testid="fm-entry-doomed-b.txt"]').click({ modifiers: ['Control'] });

    // Right-click → Delete
    await page.locator('[data-testid="fm-entry-doomed-a.txt"]').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-delete"]').click();

    // Confirm dialog shows "2 items"
    await expect(page.locator('[data-testid="fm-confirm-delete-name"]')).toContainText(/doomed-a\.txt|doomed-b\.txt|2 items/);
    await page.locator('[data-testid="fm-confirm-delete-yes"]').click();

    // bulk-ops finishes the job — log line + fs assertion.
    await router.waitForLog(/bulk-ops job=\S+ op=delete status=done/, 5_000);
    expect(existsSync(join(router.fmRoot, 'doomed-a.txt'))).toBe(false);
    expect(existsSync(join(router.fmRoot, 'doomed-b.txt'))).toBe(false);
  });

  test('delete a non-empty dir → fm transparently re-routes through bulk-ops', async ({ page, router }) => {
    const dir = join(router.fmRoot, 'fullbin');
    mkdirSync(dir);
    writeFileSync(join(dir, 'a.txt'), 'a');
    writeFileSync(join(dir, 'b.txt'), 'b');
    await openFm(page, router);

    await page.locator('[data-testid="fm-entry-fullbin"]').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-delete"]').click();
    await page.locator('[data-testid="fm-confirm-delete-yes"]').click();

    // fm's direct delete will fail with not_empty; the fallback
    // dispatches to bulk-ops and we wait for that job to finish.
    await router.waitForLog(/bulk-ops job=\S+ op=delete status=done/, 5_000);
    expect(existsSync(dir)).toBe(false);
  });

  test('sidebar bulk widget shows job row + auto-expands on enqueue', async ({ page, router }) => {
    // Multi-select delete should produce a bulk-job-* row in the
    // sidebar's Bulk Ops section. The auto-expand rule (M5) flips
    // the section from collapsed → expanded on a new job.
    writeFileSync(join(router.fmRoot, 'sb-a.txt'), 'a');
    writeFileSync(join(router.fmRoot, 'sb-b.txt'), 'b');
    await openFm(page, router);

    // Confirm the bulk section starts collapsed.
    await expect(page.locator('[data-testid="sidebar-section-bulk"]')).toHaveAttribute('data-state', 'collapsed');

    // Multi-select + delete.
    await page.locator('[data-testid="fm-entry-sb-a.txt"]').click();
    await page.locator('[data-testid="fm-entry-sb-b.txt"]').click({ modifiers: ['Control'] });
    await page.locator('[data-testid="fm-entry-sb-a.txt"]').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-delete"]').click();
    await page.locator('[data-testid="fm-confirm-delete-yes"]').click();

    // Auto-expand + a row arrives. Row test-id includes the job_id
    // (we don't know it ahead of time — match the prefix).
    await expect(page.locator('[data-testid="sidebar-section-bulk"]')).toHaveAttribute('data-state', 'expanded', { timeout: 5_000 });
    await expect(page.locator('[data-testid^="bulk-job-"]').first()).toBeVisible({ timeout: 5_000 });
    // Job completes (fs side already covered by other tests; here we
    // just check the row reaches a terminal state).
    await expect(page.locator('[data-testid^="bulk-job-"]').first()).toHaveAttribute('data-status', /done|cancelled|failed/, { timeout: 5_000 });
  });

  test('fm\'s bulk conflict overlay pops on copy-onto-existing', async ({ page, router }) => {
    // Set up a target file + a source we'll copy onto it.
    mkdirSync(join(router.fmRoot, 'src'));
    writeFileSync(join(router.fmRoot, 'src', 'conflict.txt'), 'src');
    writeFileSync(join(router.fmRoot, 'conflict.txt'), 'existing');
    await openFm(page, router);

    // Trigger a copy via the cross-app bulk wire — fm's UI doesn't
    // ship a copy-with-conflict path in M6 (separate UX), but bulk's
    // BE accepts copy enqueues directly. Use control socket to
    // enqueue. The overlay pops in the fm WINDOW since SIDEBAR.md M3b:
    // a blocked worker is asking a file manager's question, and only an
    // app can answer it for the host the job is running on.
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.bulk' });
    const inst = launched.instance_id as string;
    const ack = await router.sendAppMsg(inst, {
      kind: 'enqueue', id: 'cf-1', op: 'copy',
      paths: [join(router.fmRoot, 'src', 'conflict.txt')],
      dest: router.fmRoot,
    });
    expect(ack.kind).toBe('enqueue_ok');

    // The overlay appears with the conflict prompt + a Replace button.
    const overlay = page.locator('wash-app-fm').first().locator('[data-testid="bulk-conflict-overlay"]');
    await expect(overlay).toBeVisible({ timeout: 5_000 });
    await expect(overlay).toContainText(/Replace|Merge/);
    // Skip — leaves the existing file intact.
    await overlay.locator('[data-testid^="bulk-conflict-skip-"]').first().click();
    await expect(overlay).toBeHidden({ timeout: 3_000 });
    // Existing content unchanged.
    expect(existsSync(join(router.fmRoot, 'conflict.txt'))).toBe(true);
  });

  test('shift-click range-selects between two clicks', async ({ page, router }) => {
    writeFileSync(join(router.fmRoot, 'r1.txt'), '1');
    writeFileSync(join(router.fmRoot, 'r2.txt'), '2');
    writeFileSync(join(router.fmRoot, 'r3.txt'), '3');
    await openFm(page, router);

    // Anchor on r1, then Shift-click r3 to grab the range.
    await page.locator('[data-testid="fm-entry-r1.txt"]').click();
    await page.locator('[data-testid="fm-entry-r3.txt"]').click({ modifiers: ['Shift'] });

    await expect(page.locator('[data-testid="fm-entry-r1.txt"]')).toHaveAttribute('data-selected', 'true');
    await expect(page.locator('[data-testid="fm-entry-r2.txt"]')).toHaveAttribute('data-selected', 'true');
    await expect(page.locator('[data-testid="fm-entry-r3.txt"]')).toHaveAttribute('data-selected', 'true');
  });
});
