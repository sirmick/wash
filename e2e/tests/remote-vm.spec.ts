// wash-remote (R2) capstone: two real VMs, real SSH (docs/REMOTE.md).
//
// VM-A serves the desktop (browser attached via the chrome proxy); VM-B is a
// separate host running sshd + wash. From A's desktop we open wash-connect and
// connect to B over a genuine `ssh -L` driven by com.wash.remote, asserting the
// host reaches "connected" — i.e. the supervisor SSHed in, started B's
// wash-router, and saw it bind. This is the half the host-process e2e
// (connect-launch.spec.ts) stubs out: the real cross-machine ssh bring-up.
//
// Why this stops at "connected" (not the composited B window): R2's *second*
// connection — the browser opening a WebSocket to the local end of the ssh -L
// forward — assumes the browser shares a localhost with A's router + tunnel.
// That holds in the product (one desktop machine) and is proven in-process by
// connect-launch.spec.ts. It does NOT hold under the wash-vm proxy, which puts
// the browser on the host while A's router + ssh -L live inside VM-A — so the
// browser can't reach VM-A's 127.0.0.1:<fwd>. The two specs together cover the
// whole path; a single-process real-ssh-AND-composite test would need a
// co-located browser (host desktop A + VM-B remote) — a separate harness.

import { test, expect, remoteVmSkipReason, vmLogin, REMOTE_HOST } from '../fixtures/remote-vm';

test.beforeEach(() => {
  const reason = remoteVmSkipReason();
  test.skip(reason !== null, reason ?? '');
});

test('connect to a second VM over real ssh — supervisor brings up B', async ({ remoteVm, page }) => {
  test.setTimeout(240_000); // two VM boots + ssh wiring + tunnelled router bring-up

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

  // The host card reaches "connected" once com.wash.remote SSHed into B,
  // started B's wash-router, and saw it bind ("listening on") — the real
  // cross-machine ssh -L bring-up. (FE compositing of B's window is proven
  // in-process by connect-launch.spec.ts; see the header for why the proxy
  // topology can't carry the browser's second connection.)
  const status = connect.locator('[data-testid="connect-host-status"]');
  await expect(status).toHaveAttribute('data-status', 'up', { timeout: 90_000 });
  // No error surfaced on the host card (a clean bring-up, not a flap).
  await expect(connect.locator(`[data-testid="connect-host-${REMOTE_HOST}"] [data-testid="connect-host-dot"]`)).toBeVisible();
});
