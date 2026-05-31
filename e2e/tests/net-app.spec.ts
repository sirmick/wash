// Host-side full-stack net e2e (docs/NET.md §8): the real Network app driven
// directly in kiosk mode against the real com.wash.netd service — no VM, no
// shell, no sidebar. netd runs its FAKE applier (real NM only under
// WASH_NETD_BACKEND=nm), so the whole stack is deterministic and runs in
// seconds on the host. The in-VM real-NM capstone stays in net-vm-gate.spec.ts.
//
// This is the regression net for FE logic + the FE↔netd contract — the tier
// where the "static doesn't expand" class of bug is caught instantly, instead
// of by hand-driving a browser through the VM.

import { test, expect } from '../fixtures/router';

test.describe('net app (kiosk, host-side full stack)', () => {
  test.use({ routerOpts: { kiosk: 'com.wash.net', apps: ['net', 'netd'] } });

  test('mounts and loads caps from netd', async ({ page, router }) => {
    await page.goto(router.url);
    const net = page.locator('wash-app-net');
    await expect(net).toBeVisible();
    // +Ethernet enables only once the mount-time `current` round-trip to netd
    // returned (caps + devices). Its being enabled proves the cross-app relay
    // net → netd → net completed.
    await expect(net.locator('[data-testid="add-ethernet"]')).toBeEnabled();
  });

  test('picking Manual (static) expands the addressing form with IP fields', async ({ page, router }) => {
    await page.goto(router.url);
    const net = page.locator('wash-app-net');
    await expect(net).toBeVisible();

    await net.locator('[data-testid="add-ethernet"]').click();
    const wizard = net.locator('[data-testid="eth-wizard"]');
    await expect(wizard).toBeVisible();

    const addressing = wizard.locator('[data-testid="addressing"]');
    // Static address fields render as <input data-widget="cidr"> (ObjectForm) —
    // they exist ONLY under the static variant (IPv4 + IPv6 address), so their
    // presence is an i18n-independent proof the section expanded.
    const ipFields = addressing.locator('input[data-widget="cidr"]');

    // Default method is DHCP: no static IP field yet (the reported bug was that
    // it never appeared after switching to static).
    await expect(ipFields).toHaveCount(0);

    // Switch IP method → Manual (static IP). The method select is the only one
    // inside the addressing block (the Interface picker is a sibling, outside it).
    await addressing.locator('select').selectOption({ label: 'Manual (static IP)' });

    // The section expands: both the IPv4 and IPv6 static address inputs appear.
    await expect(ipFields).toHaveCount(2);
    await expect(ipFields.first()).toBeVisible();

    // And it round-trips back: typing an address keeps the form on static (the
    // path-routing bug also dropped subsequent edits into a stray "" key).
    await ipFields.first().fill('192.168.50.10/24');
    await expect(ipFields.first()).toHaveValue('192.168.50.10/24');
    await expect(net.locator('[data-testid="eth-create"]')).toBeEnabled();
  });

  test('stage stays local (no auto-apply); explicit Apply → keep → committed', async ({ page, router }) => {
    await page.goto(router.url);
    const net = page.locator('wash-app-net');
    await expect(net).toBeVisible();

    await net.locator('[data-testid="add-ethernet"]').click();
    // Interface defaults to the first fake NIC (eth0) and Name follows it, so
    // the wizard is immediately submittable.
    await net.locator('[data-testid="eth-create"]').click();

    // Create only STAGES — nothing applies. The connection lists as "new" and a
    // pending bar appears; the commit-confirm gate must NOT be showing yet.
    await expect(net.locator('[data-testid="conn-eth0"]')).toHaveAttribute('data-status', 'new');
    await expect(net.locator('[data-testid="pending-bar"]')).toBeVisible();
    await expect(net.locator('[data-testid="apply-confirm"]')).toHaveCount(0);

    // Explicit Apply runs the real commit-confirm txn in netd → await-confirm.
    await net.locator('[data-testid="apply-button"]').click();
    await expect(net.locator('[data-testid="apply-confirm"]')).toBeVisible();

    // Keep → netd confirms → committed (the fake applier's Confirm path); the
    // reloaded connection is now clean (no badge).
    await net.locator('[data-testid="keep-button"]').click();
    await expect(net.locator('.wash-net-status')).toHaveText('committed');
    await expect(net.locator('[data-testid="conn-eth0"]')).toHaveAttribute('data-status', 'clean');
  });

  test('Discard drops staged edits without applying', async ({ page, router }) => {
    await page.goto(router.url);
    const net = page.locator('wash-app-net');
    await expect(net).toBeVisible();

    await net.locator('[data-testid="add-ethernet"]').click();
    await net.locator('[data-testid="eth-create"]').click();
    await expect(net.locator('[data-testid="conn-eth0"]')).toBeVisible();

    await net.locator('[data-testid="discard-changes"]').click();
    // Back to empty — nothing was sent to netd.
    await expect(net.locator('[data-testid="conn-eth0"]')).toHaveCount(0);
    await expect(net.locator('[data-testid="pending-bar"]')).toHaveCount(0);
  });
});

test.describe('net app addressing (kiosk)', () => {
  test.use({ routerOpts: { kiosk: 'com.wash.net', apps: ['net', 'netd'] } });

  test('IP method select toggles the variant fields every switch', async ({ page, router }) => {
    await page.goto(router.url);
    const net = page.locator('wash-app-net');
    await expect(net).toBeVisible();
    await net.locator('[data-testid="add-ethernet"]').click();
    const addr = net.locator('[data-testid="addressing"]');
    const cidr = addr.locator('input[data-widget="cidr"]');
    const method = addr.locator('select');
    // DHCP default: family checkboxes present, no static address fields.
    await expect(cidr).toHaveCount(0);
    await expect(addr.locator('input[type="checkbox"]')).toHaveCount(2); // IPv4 + IPv6 (DHCP)
    await method.selectOption({ label: 'Manual (static IP)' });
    await expect(cidr).toHaveCount(2);                                   // v4 + v6 address
    // Through Disabled (no fields) — the fieldless variant used to throw inside
    // the reactive form() and freeze every later switch.
    await method.selectOption({ label: 'Disabled (no IP)' });
    await expect(cidr).toHaveCount(0);
    await expect(addr.locator('input')).toHaveCount(0);                  // none: zero inputs
    await method.selectOption({ label: 'Automatic (DHCP)' });
    await expect(addr.locator('input[type="checkbox"]')).toHaveCount(2); // dhcp fields came back
    await expect(cidr).toHaveCount(0);
    await method.selectOption({ label: 'Manual (static IP)' });
    await expect(cidr).toHaveCount(2);                                   // static again
  });
});
