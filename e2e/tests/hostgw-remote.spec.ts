// Host-awareness channel, two routers (docs/SIDEBAR.md M1a).
//
// The defect this closes: the right rail reads A's services no matter
// which host the work is on, so an agent (or a job, or an escalation) on
// B is invisible. M1's answer is com.wash.hostgw — a background app on
// every router that subscribes to its own host's services and republishes
// their state to its own FE, which the router fans to every attached
// shell. A's shell IS an attached shell on B, so B's state arrives over
// the connection that already exists.
//
// Two routers stand in for host A (the desktop) and host B (what an
// `ssh -L` tunnel would reach), wired with ?peer= exactly as
// remote-apps.spec.ts does. B runs --no-session, which is the whole
// reason a session-BE gateway could never have served it.
//
// What's asserted, in order of how much it would hurt to lose:
//   1. B's live state reaches A, keyed under B's origin.
//   2. A's own state does NOT appear under B — the regression that
//      motivated the plan was precisely a host mix-up.
//   3. The startup subscribe works, not just the live fan-out: a service
//      nobody touched still shows up, from its subscribe-time snapshot.

import { test, expect } from '@playwright/test';
import { startRouter, stopRouter, type RouterHandle } from '../fixtures/router';

/** The merged (origin → service → state) map, flattened for assertions. */
type Snapshot = Record<string, Record<string, unknown>>;

async function hostgwSnapshot(page: import('@playwright/test').Page): Promise<Snapshot> {
  return page.evaluate(() => {
    const w = window as unknown as {
      wash: { hostgwState(): Map<string, Map<string, unknown>> };
    };
    const out: Snapshot = {};
    for (const [origin, services] of w.wash.hostgwState()) {
      out[origin] = Object.fromEntries(services) as Record<string, unknown>;
    }
    return out;
  }) as Promise<Snapshot>;
}

/** Titles of the notifications in one host's notify cell. */
function notifyTitles(snap: Snapshot, origin: string): string[] {
  const state = snap[origin]?.notify as { notifications?: Array<{ title?: string }> } | undefined;
  return (state?.notifications ?? []).map((n) => n.title ?? '');
}

// raiseNotify drives the test app's notify action, which emits a real
// EvtNotify — so the router's notify service ingests it exactly as any
// app's would. The title carries a per-app sequence, which is what lets
// us tell A's notifications from B's below.
//
// Waits for the service to log the ingest before returning. Firing two of
// these back to back without waiting loses one: the first EvtNotify
// spawns the notify singleton on demand (forwardNotifyToService is
// best-effort and returns silently if the recipient isn't resolvable
// yet), so the second can arrive mid-spawn. Sequencing here keeps the
// race out of the assertion — the router-side behaviour under burst is
// notify's business, not this spec's.
async function raiseNotify(r: RouterHandle, instanceID: string, seq: number): Promise<void> {
  const from = r.logCursor();
  await r.controlRequest({ t: 'msg', instance_id: instanceID, data: { kind: 'notify' } });
  await r.waitForLog(new RegExp(`wash-notify: .*"wash-test #${seq}"`), 10_000, from);
}

test('a remote host\'s service state reaches the seat, tagged to that host', async ({ page }) => {
  let a: RouterHandle | undefined;
  let b: RouterHandle | undefined;
  try {
    a = await startRouter({ apps: ['session', 'test', 'notify', 'hostgw'] });
    b = await startRouter({
      apps: ['about', 'test', 'notify', 'hostgw'],
      extraArgs: ['--no-session', '--allow-cross-origin'],
    });

    // Raise TWO notifications on A and ONE on B, before the browser is
    // even open. Different counts per host is what makes the isolation
    // assertion sharp: the test app's title carries its own sequence, so
    // "wash-test #2" exists only on A and can never legitimately appear
    // in B's cell.
    const aTest = await a.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    const bTest = await b.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    const aInst = String(aTest.instance_id ?? '');
    const bInst = String(bTest.instance_id ?? '');
    expect(aInst).not.toBe('');
    expect(bInst).not.toBe('');
    await raiseNotify(a, aInst, 1);
    await raiseNotify(a, aInst, 2);
    await raiseNotify(b, bInst, 1);

    const bPort = new URL(b.url).port;
    const peer = `remoteB@ws://127.0.0.1:${bPort}/ws`;
    await page.goto(`${a.url}?peer=${encodeURIComponent(peer)}`);
    await expect(page.locator('[data-testid="wash-cam"]')).toBeAttached({ timeout: 10_000 });

    // (1) B's notify state arrives, under B's origin. This is the whole
    // mechanism: shell subscribe → B spawns hostgw → hostgw replays its
    // cache → relayAppMsgToShell fans it to this shell.
    await expect
      .poll(async () => notifyTitles(await hostgwSnapshot(page), 'remoteB'), { timeout: 20_000 })
      .toEqual(['wash-test #1']);

    const snap = await hostgwSnapshot(page);

    // (2) Isolation, both directions, and this is the assertion the whole
    // plan exists for. A raised two notifications and B one; each cell
    // holds exactly its own host's. If the origin tagging were wrong (or
    // the map keyed by service alone) A's "#2" would surface under B, or
    // B's single entry would be overwritten by A's pair.
    //
    // LOCAL is populated even though M1a installs no local subscriber:
    // EnsureBackgroundAppsRunning spawns EVERY background app on each
    // shell connect (internal/router/autoboot.go), so A's own hostgw boots
    // and republishes unprompted. M1a is "remote-only" in what the RAIL
    // READS, not in what arrives — see the M1 note in docs/SIDEBAR.md.
    expect(notifyTitles(snap, 'remoteB')).toEqual(['wash-test #1']);
    expect(notifyTitles(snap, 'local')).toEqual(['wash-test #2', 'wash-test #1']);

    // (3) The startup subscribe, not just the live push: wash-agentd is
    // staged in every test router, so B's hostgw subscribed to it at
    // startup and got a snapshot back with nobody having touched it.
    expect(Object.keys(snap.remoteB).sort()).toContain('agent');

    // (4) And the rail actually renders the merged number (M1b). Three
    // unread: A's two plus B's one. This is also the double-count guard —
    // local state reaches the FE twice now (via hostgw AND via the legacy
    // notify.state kind the widget body still reads), so a badge sourced
    // from both would read five here.
    await expect(page.locator('[data-testid="sidebar-section-badge-notify"]'))
      .toHaveText('3', { timeout: 10_000 });

    // (5) And B has its own group in the rail (M1c): named, badged with
    // ITS count alone, and collapsed — the remote host is visible without
    // pushing this seat's own list off screen. The local host gets no
    // group: it is the section body, and saying "this machine" twice is
    // how the rail stopped being glanceable.
    const group = page.locator('[data-testid="host-group-notify-remoteB"]');
    await expect(group).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('[data-testid="host-group-badge-notify-remoteB"]')).toHaveText('1');
    await expect(page.locator('[data-testid="host-group-notify-local"]')).toHaveCount(0);

    // It arrived OPEN, and that is the auto-expand working (§3.2(1)): B's
    // unread count rose from nothing to one, which is exactly the event
    // worth pulling a collapsed group open for. Collapsed is the resting
    // default — asserted in HostGroups.ctest.tsx, where a group can be
    // rendered without an event having just fired.
    await expect(group).toHaveAttribute('data-state', 'expanded');
    await expect(page.locator('[data-testid="host-group-body-notify-remoteB"]'))
      .toHaveText('1 unread');

    // And the user still outranks the auto-expand.
    await page.locator('[data-testid="host-group-header-notify-remoteB"]').click();
    await expect(group).toHaveAttribute('data-state', 'collapsed');
  } finally {
    if (a) await stopRouter(a);
    if (b) await stopRouter(b);
  }
});
