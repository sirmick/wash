// wash-discovery — the "On your network" candidate list (docs/DISCOVERY.md).
//
// Full-stack proof of the discovery feature without real multicast (mDNS
// needs two hosts on a shared link — that's the deferred two-VM gate). We
// drive the BE's static provider seam (WASH_DISCOVERY_STATIC), inherited by
// the auto-spawned com.wash.remote background app, so a deterministic
// candidate flows: discoverer → StateService → connect BE relay → connect FE.
//
// WASH_DISCOVERY_NO_MDNS disables the live mDNS provider entirely (no browse,
// no advertise) so ONLY the static seed appears. NO_ADVERTISE alone is not
// enough — it silences our announcement but still browses the link, so the
// runner's real LAN peers would bleed into "On your network" and make the
// assertions non-deterministic on a developer's network.
//
// Covered: the candidate renders under "On your network" (name + addr +
// non-default port), Connect moves it into the connected list (proving the
// connect op — carrying remote_port — relays all the way to the supervisor),
// and Save turns it into a bookmark (and the candidate then disappears).
// The exact `ssh -p <port>` dial is unit-tested in supervisor_test.go.

import { test, expect } from '@playwright/test';
import { startRouter, stopRouter, type RouterHandle } from '../fixtures/router';

const STATIC = 'labbox=10.42.0.9:2222';

async function openConnect(page: import('@playwright/test').Page, a: RouterHandle) {
  await page.goto(a.url);
  await expect(page.locator('[data-testid="wash-cam"]')).toBeAttached({ timeout: 10_000 });
  const launched = await a.controlRequest({ t: 'launch', app_id: 'com.wash.connect' });
  expect(launched.t).toBe('launched');
  await expect(page.locator('[data-testid="connect-host-input"]')).toBeVisible({ timeout: 15_000 });
}

test('discovered host renders under "On your network" and Connect dials it', async ({ page }) => {
  let a: RouterHandle | undefined;
  try {
    a = await startRouter({
      apps: ['session', 'connect', 'remote'],
      extraEnv: { WASH_DISCOVERY_STATIC: STATIC, WASH_DISCOVERY_NO_MDNS: '1' },
      xdgConfig: true,
    });
    await openConnect(page, a);

    // The static candidate flows BE → FE into the "On your network" list.
    const row = page.locator('[data-testid="connect-candidate-10.42.0.9"]');
    await expect(row).toBeVisible({ timeout: 15_000 });
    await expect(row).toContainText('labbox');
    await expect(row).toContainText('10.42.0.9:2222'); // friendly name + dim addr:port
    // A static pin did not announce itself as wash → no "wash" chip.
    await expect(row.locator('[data-testid="connect-candidate-wash"]')).toHaveCount(0);

    // Connect dials by friendly name (so the remote ssh reads ~/.ssh/config),
    // carrying the addr as a fallback + remote_port under the hood. It can't
    // succeed against a bogus IP, but the supervisor takes ownership, so the
    // host appears in the connected list (keyed by its name) and leaves the
    // candidate list.
    await row.locator('[data-testid="connect-candidate-connect"]').click();
    await expect(page.locator('[data-testid="connect-host-labbox"]')).toBeAttached({ timeout: 10_000 });
    await expect(row).toHaveCount(0, { timeout: 10_000 });
  } finally {
    if (a) await stopRouter(a);
  }
});

test('Save turns a discovered host into a bookmark', async ({ page }) => {
  let a: RouterHandle | undefined;
  try {
    a = await startRouter({
      apps: ['session', 'connect', 'remote'],
      extraEnv: { WASH_DISCOVERY_STATIC: STATIC, WASH_DISCOVERY_NO_MDNS: '1' },
      xdgConfig: true,
    });
    await openConnect(page, a);

    const row = page.locator('[data-testid="connect-candidate-10.42.0.9"]');
    await expect(row).toBeVisible({ timeout: 15_000 });

    // ☆ saves the addr as the bookmark target with the friendly name as label;
    // the candidate then drops out of "On your network" (it's now saved).
    await row.locator('[data-testid="connect-candidate-save"]').click();
    const bookmark = page.locator('[data-testid="connect-bookmark"]', { hasText: 'labbox' });
    await expect(bookmark).toBeVisible({ timeout: 10_000 });
    await expect(row).toHaveCount(0, { timeout: 10_000 });
  } finally {
    if (a) await stopRouter(a);
  }
});
