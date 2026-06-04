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

// WireGuard exercises the dedicated VPN wizard: a wg interface (private key +
// tunnel address) plus a peer (which lives in the separate WGPeers kind) staged,
// applied, and committed through the real commit-confirm txn — proving the FE
// builds the model correctly and it round-trips through netd. The fake backend
// advertises CanWireGuard (caps.Full), so the +WireGuard button shows host-side.
test.describe('net app wireguard (kiosk, full FE→net→netd)', () => {
  test.use({ routerOpts: { kiosk: 'com.wash.net', apps: ['net', 'netd'] } });

  test('add a WireGuard tunnel → apply → committed', async ({ page, router }) => {
    await page.goto(router.url);
    const net = page.locator('wash-app-net');
    await expect(net).toBeVisible();

    // +WireGuard is gated on the CanWireGuard capability (the fake backend has it).
    const add = net.locator('[data-testid="add-wireguard"]');
    await expect(add).toBeEnabled();
    await add.click();
    await expect(net.locator('[data-testid="wireguard-wizard"]')).toBeVisible();

    // A private key is pre-generated (32 bytes base64 ≈ 44 chars); Generate rolls
    // a fresh one — proving the in-browser keygen runs, no backend round-trip.
    const key1 = await net.locator('[data-testid="wg-privkey"]').inputValue();
    expect(key1.length).toBeGreaterThan(40);
    await net.locator('[data-testid="wg-genkey"]').click();
    expect(await net.locator('[data-testid="wg-privkey"]').inputValue()).not.toBe(key1);

    // The public key is derived in-browser (Curve25519) from the private key — a
    // known key gives a deterministic pubkey (RFC-7748-verified derivation), so
    // the user can hand it to peers without applying first.
    await net.locator('[data-testid="wg-privkey"]').fill('yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=');
    await expect(net.locator('[data-testid="wg-pubkey"]')).toHaveValue('HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=');

    await net.locator('[data-testid="wg-addresses"]').fill('10.9.0.1/24');
    await net.locator('[data-testid="wg-peer-pubkey-0"]').fill('aGVsbG93aXJlZ3VhcmRwdWJrZXlnb2VzaGVyZT0=');
    await net.locator('[data-testid="wg-peer-allowed-0"]').fill('10.9.0.2/32');
    await net.locator('[data-testid="wg-peer-host-0"]').fill('vpn.example.com');
    await net.locator('[data-testid="wg-peer-port-0"]').fill('51820');
    await net.locator('[data-testid="wg-create"]').click();

    // Staged as a new WireGuard connection (kindOf → "WireGuard").
    const conn = net.locator('[data-testid="conn-wg0"]');
    await expect(conn).toHaveAttribute('data-status', 'new');
    await expect(conn).toHaveAttribute('data-kind', 'WireGuard');

    // Apply → keep → committed; conn reloads clean, proving the interface AND its
    // peer survived encode → netd apply/confirm → liveConfig → decode unchanged.
    await net.locator('[data-testid="apply-button"]').click();
    await expect(net.locator('[data-testid="apply-confirm"]')).toBeVisible();
    await net.locator('[data-testid="keep-button"]').click();
    await expect(net.locator('.wash-net-status')).toHaveText('committed');
    await expect(net.locator('[data-testid="conn-wg0"]')).toHaveAttribute('data-status', 'clean');
  });

  test('import a wg-quick config (QR contents) fills the whole form', async ({ page, router }) => {
    await page.goto(router.url);
    const net = page.locator('wash-app-net');
    await expect(net).toBeVisible();
    await net.locator('[data-testid="add-wireguard"]').click();
    await expect(net.locator('[data-testid="wireguard-wizard"]')).toBeVisible();

    // The exact text a WireGuard QR code encodes (and a .conf file holds).
    const conf = [
      '[Interface]',
      'PrivateKey = yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=',
      'Address = 10.9.0.2/24, fd00::2/64',
      'ListenPort = 51821',
      'DNS = 1.1.1.1',
      '',
      '[Peer]',
      'PublicKey = xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=',
      'PresharedKey = c29tZXByZXNoYXJlZGtleWZvcndndGVzdA==',
      'Endpoint = vpn.example.com:51820',
      'AllowedIPs = 0.0.0.0/0, ::/0',
      'PersistentKeepalive = 25',
    ].join('\n');
    await net.locator('[data-testid="wg-import-text"]').fill(conf);
    await net.locator('[data-testid="wg-import-apply"]').click();

    // [Interface] → the interface fields (and the derived public key).
    await expect(net.locator('[data-testid="wg-privkey"]')).toHaveValue('yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=');
    await expect(net.locator('[data-testid="wg-pubkey"]')).toHaveValue('HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=');
    await expect(net.locator('[data-testid="wg-addresses"]')).toHaveValue('10.9.0.2/24, fd00::2/64');
    await expect(net.locator('[data-testid="wg-listenport"]')).toHaveValue('51821');
    // [Peer] → the peer row (endpoint split into host/port).
    await expect(net.locator('[data-testid="wg-peer-pubkey-0"]')).toHaveValue('xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=');
    await expect(net.locator('[data-testid="wg-peer-psk-0"]')).toHaveValue('c29tZXByZXNoYXJlZGtleWZvcndndGVzdA==');
    await expect(net.locator('[data-testid="wg-peer-host-0"]')).toHaveValue('vpn.example.com');
    await expect(net.locator('[data-testid="wg-peer-port-0"]')).toHaveValue('51820');
    await expect(net.locator('[data-testid="wg-peer-allowed-0"]')).toHaveValue('0.0.0.0/0, ::/0');
    await expect(net.locator('[data-testid="wg-peer-keepalive-0"]')).toHaveValue('25');

    // …and it's immediately creatable from the imported config.
    await net.locator('[data-testid="wg-create"]').click();
    await expect(net.locator('[data-testid="conn-wg0"]')).toHaveAttribute('data-status', 'new');
  });
});

// Wi-Fi exercises the full windowed path FE → com.wash.net → com.wash.netd for
// the wifi_* kinds, which the config-flow tests above never touch. This is the
// regression net for the relay bug where com.wash.net's proxy only forwarded the
// config kinds, so every wifi_scan/wifi_radio silently died in the windowed app
// (15s timeout, dead scan) — green here only if the transparent relay carries
// them. netd uses the deterministic fake wifi runtime (WASH_NETD_WIFI); the real
// nmcli path is covered in the VM gate.
test.describe('net app wifi (kiosk, full FE→net→netd relay)', () => {
  test.describe('radio switched off', () => {
    test.use({ routerOpts: { kiosk: 'com.wash.net', apps: ['net', 'netd'], extraEnv: { WASH_NETD_WIFI: 'off' } } });

    test('scan reports disabled → "Turn on Wi-Fi" toggle appears', async ({ page, router }) => {
      await page.goto(router.url);
      const net = page.locator('wash-app-net');
      await expect(net).toBeVisible();

      // +Wi-Fi shows once caps (wireless/wifi-iface) + radio arrive from netd's
      // `current`; clicking it opens the dialog, whose scan poll fires wifi_scan.
      const addWifi = net.locator('[data-testid="add-wifi"]');
      await expect(addWifi).toBeEnabled();
      await addWifi.click();
      await expect(net.locator('[data-testid="wifi-dialog"]')).toBeVisible();

      // The toggle is shown ONLY when wifi_scan round-tripped and returned
      // enabled:false — wifiEnabled defaults true, so its flip is proof the relay
      // delivered the scan reply. A dropped relay leaves the scan UI up forever.
      await expect(net.locator('[data-testid="wifi-radio-on"]')).toBeVisible();
    });
  });

  test.describe('radio enabled with an AP', () => {
    test.use({ routerOpts: { kiosk: 'com.wash.net', apps: ['net', 'netd'], extraEnv: { WASH_NETD_WIFI: 'on' } } });

    test('scan returns the AP → picker lists it', async ({ page, router }) => {
      await page.goto(router.url);
      const net = page.locator('wash-app-net');
      await expect(net).toBeVisible();

      await net.locator('[data-testid="add-wifi"]').click();
      await expect(net.locator('[data-testid="wifi-dialog"]')).toBeVisible();

      // The fake's single AP can only appear here if wifi_scan relayed and its
      // reply carried the AP list back through com.wash.net to the FE.
      await expect(net.locator('[data-testid="ap-wash-e2e"]')).toBeVisible();
      await expect(net.locator('[data-testid="wifi-radio-on"]')).toHaveCount(0);
    });
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
