// Phase-B real-NM capstone (docs/NET.md §8.3): drive the VM-served Network app
// through THREE different interface shapes against four virtual eths — eth0 as
// DHCP, a bridge over eth1+eth2, and a VLAN on eth3 — and assert real
// NetworkManager in the guest accepts each via the commit-confirm transaction.
//
// "The VM accepts it" is proven two ways per shape: (1) the apply terminal
// reaches the await-confirm gate, which only happens after the in-guest txn's
// VERIFY ran against live NM without tripping lock-out protection, and (2) after
// Keep, the connection list reloads from netd.Live() — which parses the live NM
// keyfiles — so the row's presence means NM has the profile on disk + active.
//
// This is the slow capstone; the fast host-side equivalent (fake backend, no VM)
// is net-app.spec.ts.

import { test, expect, vmSkipReason, vmLogin } from '../fixtures/vm';

test.beforeEach(() => {
  const reason = vmSkipReason();
  test.skip(reason !== null, reason ?? '');
});

test('eth(DHCP) + bridge(eth1,eth2) + vlan(eth3.100): real NM accepts all three', async ({ vm, page }) => {
  test.setTimeout(180_000); // VM boot + chromium + three commit-confirm verifies

  await page.goto(vm.url);

  // Log in through the in-guest front's gate (wash-vm/UNIFY.md) before the desktop loads.
  await vmLogin(page);
  await expect(page.locator('wash-app-session')).toBeVisible({ timeout: 40_000 });
  await expect(page.locator('#spec')).toContainText('served from VM', { timeout: 40_000 });

  // Launch Network via the sidebar's Network section → Configure…, which
  // spawn.requests com.wash.net (it's also in the start-menu catalog now).
  await page.locator('[data-testid="sidebar-section-header-net"]').click();
  await page.locator('[data-testid="net-configure"]').click();
  const net = page.locator('wash-app-net');
  await expect(net).toBeVisible({ timeout: 20_000 });
  // +Ethernet enables once the mount-time `current` round-trip to in-guest netd
  // returned caps + the four NM-discovered eths.
  await expect(net.locator('[data-testid="add-ethernet"]')).toBeEnabled({ timeout: 20_000 });

  const ethRow = net.locator('[data-kind="Ethernet"][data-device="eth0"]');
  const brRow = net.locator('[data-kind="Bridge"][data-device="br0"]');
  const vlanRow = net.locator('[data-device="eth3.100"]');

  // STAGE all three shapes (no apply yet) — Create only edits the local draft.
  // 1) eth0 — plain DHCP (device defaults to the first free NIC, proto DHCP).
  await net.locator('[data-testid="add-ethernet"]').click();
  await net.locator('[data-testid="eth-create"]').click();
  // 2) br0 — bridge eth1 + eth2 (they become ports, losing standalone configs).
  await net.locator('[data-testid="add-bridge"]').click();
  await net.locator('[data-testid="member-eth1"]').check();
  await net.locator('[data-testid="member-eth2"]').check();
  await net.locator('[data-testid="bridge-create"]').click();
  // 3) eth3.100 — VLAN id 100 on the last free NIC.
  await net.locator('[data-testid="add-vlan"]').click();
  await net.locator('[data-testid="vlan-parent"]').selectOption('eth3');
  await net.locator('[data-testid="vlan-vid"]').fill('100');
  await net.locator('[data-testid="vlan-create"]').click();

  // All three are staged as "new" and nothing has hit NM yet.
  await expect(ethRow).toHaveAttribute('data-status', 'new');
  await expect(brRow).toHaveAttribute('data-status', 'new');
  await expect(vlanRow).toHaveAttribute('data-status', 'new');
  await expect(net.locator('[data-testid="apply-confirm"]')).toHaveCount(0);

  // ONE explicit Apply commits the whole batch through commit-confirm in netd.
  await net.locator('[data-testid="apply-button"]').click();
  await expect(net.locator('[data-testid="apply-confirm"]')).toBeVisible({ timeout: 60_000 });
  await net.locator('[data-testid="keep-button"]').click();
  await expect(net.locator('.wash-net-status')).toHaveText('committed', { timeout: 30_000 });

  // All three coexist in the reloaded list — read back from live NM keyfiles, so
  // this is the proof real NM in the VM accepted every shape together.
  await expect(ethRow).toBeVisible({ timeout: 20_000 });
  await expect(brRow).toContainText('eth1');
  await expect(brRow).toContainText('eth2');
  await expect(vlanRow).toHaveAttribute('data-kind', /^VLAN/);
});
