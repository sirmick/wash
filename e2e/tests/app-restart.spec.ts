// Full-stack app.restart contract (docs/SETTINGS.md §5).
//
// The router unit tests (internal/router/restart_test.go) cover the
// capability check, surface guard, and not_found at the handler
// boundary. This spec drives the SAME paths end-to-end through a real
// wire round-trip: the wash-test app declares the `restart` capability
// and calls Conn.RestartApp; we assert the router's reply over the
// control socket.
//
// The caller-WITHOUT-capability → forbidden case stays unit-only
// (TestAppRestartRequiresCapability): wash-test must HAVE the cap to
// exercise everything else here, so it can't also play the capless
// caller.

import { test, expect } from '../fixtures/router';

test.use({ routerOpts: { apps: ['session', 'test', 'notify'] } });

test.describe('app.restart contract (full-stack)', () => {
  test('restarting a background singleton returns a fresh instance id', async ({ router }) => {
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    expect(launched.t).toBe('launched');
    const inst = launched.instance_id as string;

    // Resolve the current notify-service instance (control launch
    // consults the singleton table, spawning on demand — there's no
    // shell here to autoboot background apps).
    const before = await router.controlRequest({ t: 'launch', app_id: 'com.wash.notify' });
    const instA = before.instance_id as string;
    expect(instA).toBeTruthy();

    // wash-test → RestartApp(com.wash.notify): the router terminates the
    // singleton, waits for teardown, and respawns. app.restart.ok rides
    // back with the NEW instance id.
    const reply = await router.sendAppMsg(
      inst,
      { kind: 'restart', id: 'r1', app_id: 'com.wash.notify' },
      15_000,
    );
    expect(reply.kind).toBe('restart_ok');
    expect(reply.instance_id).toBeTruthy();
    expect(reply.instance_id).not.toBe(instA);

    // The singleton table now resolves to that same fresh instance —
    // the old one is gone, not shadowed.
    const after = await router.controlRequest({ t: 'launch', app_id: 'com.wash.notify' });
    expect(after.instance_id).toBe(reply.instance_id);
  });

  test('restarting a window-surface app is forbidden', async ({ router }) => {
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    const inst = launched.instance_id as string;

    // com.wash.test is surface=window — restartBackgroundApp refuses a
    // non-background target before any terminate/respawn.
    const reply = await router.sendAppMsg(inst, { kind: 'restart', id: 'r2', app_id: 'com.wash.test' });
    expect(reply.kind).toBe('restart_err');
    expect(reply.error as string).toContain('forbidden');
  });

  test('restarting an unknown app id is not_found', async ({ router }) => {
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    const inst = launched.instance_id as string;

    const reply = await router.sendAppMsg(inst, { kind: 'restart', id: 'r3', app_id: 'com.wash.nope' });
    expect(reply.kind).toBe('restart_err');
    expect(reply.error as string).toContain('not_found');
  });
});
