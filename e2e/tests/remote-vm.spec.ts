// wash-remote (R2) capstone: two real VMs, real SSH (docs/REMOTE.md).
//
// VM-A serves the desktop (browser attached via the chrome proxy); VM-B is a
// separate host running sshd + wash. From A's desktop we open wash-connect,
// connect to B over a genuine `ssh -L` driven by com.wash.remote, and launch an
// app ON B — asserting its window composites into A's desktop with the per-host
// stripe. End to end: the supervisor's real cross-machine ssh bring-up, the
// one-port relay (B's wire muxed over the browser's single connection to A and
// spliced to the ssh -L'd unix socket), and per-origin bundle delivery — all
// over the wash-vm proxy, which works precisely because the browser only ever
// talks to A (the "one port" rule), never to B directly.

import { test, expect, remoteVmSkipReason, vmLogin, REMOTE_HOST } from '../fixtures/remote-vm';

test.beforeEach(() => {
  const reason = remoteVmSkipReason();
  test.skip(reason !== null, reason ?? '');
});

test('connect to a second VM over real ssh and composite its app window', async ({ remoteVm, page }) => {
  test.setTimeout(240_000); // two VM boots + ssh wiring + tunnelled router + bundle

  await page.goto(remoteVm.url);
  await vmLogin(page);
  await expect(page.locator('wash-app-session')).toBeVisible({ timeout: 60_000 });
  await expect(page.locator('#spec')).toContainText('served from VM', { timeout: 60_000 });

  // Open wash-connect via the sidebar Remote section → Manage…
  await page.locator('[data-testid="sidebar-section-header-remote"]').click();
  await page.locator('[data-testid="remote-manage"]').click();
  const connect = page.locator('wash-app-connect');
  await expect(connect).toBeVisible({ timeout: 30_000 });

  // Connect to VM-B over real ssh (BatchMode; the harness pre-loaded a
  // passphraseless key, so no auth widget needed here).
  await connect.locator('[data-testid="connect-host-input"]').fill(REMOTE_HOST);
  await connect.locator('[data-testid="connect-submit"]').click();

  // The host reaches "connected" once com.wash.remote SSHed in, started B's
  // wash-router (--listen-raw), and registered the relay socket; the FE then
  // attaches via the muxed peer channel (one port).
  const status = connect.locator('[data-testid="connect-host-status"]');
  await expect(status).toHaveAttribute('data-status', 'up', { timeout: 90_000 });

  // B's catalog arrives over the relay channel; launch About on B.
  await expect(connect.locator('[data-testid="connect-launch-com.wash.about"]')).toBeVisible({ timeout: 30_000 });
  await connect.locator('[data-testid="connect-launch-com.wash.about"]').click();

  // B's app window composites into A's desktop, host-striped with B's origin.
  // The stripe renders only after B's bundle imports over the relay — so this
  // proves the whole path: real ssh, the one-port mux, the A-side splice, and
  // cross-machine per-origin bundle delivery.
  const stripe = page.locator(`[data-testid="wash-host-stripe"][data-origin="${REMOTE_HOST}"]`);
  await expect(stripe).toBeVisible({ timeout: 60_000 });
});
