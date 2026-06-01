// Phase-B B1 exit gate (docs/NET.md §8.3, §11): the wash UI served BY the VM
// loads through the proxy and round-trips a model edit to the in-guest
// com.wash.netd. Driven entirely from outside the VM — Playwright against the
// proxy's HTTP/WS, the full stack (shell + router + net app + netd) running in a
// real Alpine microvm.
//
// The round-trip is the proof: the net window's Apply runs the commit-confirm
// transaction in the in-guest netd service (a separate process the net app
// relays to cross-app), and the resulting await-confirm → committed states come
// back over the serial tunnel onto the widget. Nothing here is host-faked.

import { test, expect, vmSkipReason, vmLogin } from '../fixtures/vm';

test.beforeEach(() => {
  const reason = vmSkipReason();
  test.skip(reason !== null, reason ?? '');
});

test('VM-served wash UI round-trips a model edit to in-guest netd', async ({ vm, page }) => {
  test.setTimeout(120_000); // VM boot + chromium + login + the full wire bring-up

  await page.goto(vm.url);

  // Log in through the in-guest front's gate (wash-vm/UNIFY.md) before the desktop loads.
  await vmLogin(page);

  // The desktop rendered — shell.js was fetched over the wire (asset.read) and
  // the bundle streamed from the in-guest router over the serial tunnel, not
  // from a host file. The chrome's spec label flips to "served from VM" once
  // the over-the-wire shell boot completes.
  await expect(page.locator('wash-app-session')).toBeVisible({ timeout: 40_000 });
  await expect(page.locator('#spec')).toContainText('served from VM', { timeout: 40_000 });

  // The Console tab streams the guest console (LOG plane) over /console SSE.
  await page.locator('#tab-console').click();
  await expect(page.locator('#pane-console')).toContainText('Linux', { timeout: 20_000 });
  await page.locator('#tab-wash').click();

  // Launch the Network app from the start menu (catalog served by the VM).
  await page.locator('[title="Apps"]').click();
  await page.locator('[data-testid="start-menu-com.wash.net"]').click();

  const net = page.locator('wash-app-net');
  await expect(net).toBeVisible({ timeout: 20_000 });

  // +Ethernet enables once the mount-time `current` round-trip to in-guest netd
  // returned — proving the cross-app relay net → netd → net works over the wire.
  await expect(net.locator('[data-testid="add-ethernet"]')).toBeEnabled({ timeout: 20_000 });

  // Stage eth0 (DHCP), then EXPLICITLY Apply — the wizard only edits the draft;
  // Apply runs the real commit-confirm transaction in netd and the apply terminal
  // (B3) renders its phase stream.
  await net.locator('[data-testid="add-ethernet"]').click();
  await net.locator('[data-testid="eth-create"]').click();
  await net.locator('[data-testid="apply-button"]').click();

  // The apply terminal shows the in-guest txn phase stream (verify happened)
  // and the live commit-confirm countdown to the autonomous auto-revert.
  await expect(net.locator('[data-testid="apply-confirm"]')).toBeVisible({ timeout: 20_000 });
  await expect(net.locator('[data-testid="apply-log"]')).toContainText('verify', { timeout: 10_000 });
  await expect(net.locator('[data-testid="apply-countdown"]')).toContainText('Auto-reverting');

  // Keep the change → netd confirms → committed, and eth0 lists.
  await net.locator('[data-testid="keep-button"]').click();
  await expect(net.locator('.wash-net-status')).toHaveText('committed', { timeout: 20_000 });
  await expect(net.locator('[data-kind="Ethernet"][data-device="eth0"]')).toBeVisible({ timeout: 20_000 });

  // The other commit-confirm branch in-VM: stage eth1, Apply, then Discard →
  // reverted, and eth1 must NOT persist (the autonomous timeout-revert is
  // covered by the netd unit test).
  await net.locator('[data-testid="add-ethernet"]').click();
  await net.locator('[data-testid="eth-create"]').click();
  await net.locator('[data-testid="apply-button"]').click();
  await expect(net.locator('[data-testid="apply-confirm"]')).toBeVisible({ timeout: 20_000 });
  await net.locator('[data-testid="discard-button"]').click();
  await expect(net.locator('.wash-net-status')).toHaveText('reverted', { timeout: 20_000 });
  // Reverted → the list reloads from netd.Live() (live NM keyfiles) and eth1 is
  // gone: the rollback actually took on real NM, not just in the UI.
  await expect(net.locator('[data-kind="Ethernet"][data-device="eth1"]')).toHaveCount(0);
});
