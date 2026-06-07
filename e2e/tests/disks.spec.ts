// wash-disks — storage info app. Tier-3 full-stack UI gate.
//
// Launched with WASH_DISKS_SOURCE=fake (injected via routerOpts.extraEnv)
// so the BE returns a fixed snapshot covering every disk + manager kind.
// That makes the render deterministic and root-free: we assert the topology
// tree (Disks + Volumes sections, md/lvm/btrfs/zfs rows), the detail pane,
// and a fullness bar — without depending on the runner's real disks.
//
// Real md/lvm/btrfs/zfs against an actual kernel is the separate Tier-4 VM
// gate (see docs/STORAGE.md); this spec covers the FE/wire path only.

import { test, expect } from '../fixtures/router';

test.use({
  routerOpts: {
    apps: ['session', 'about', 'disks'],
    extraEnv: { WASH_DISKS_SOURCE: 'fake' },
  },
});

// The BE snapshot shape (disks + all manager kinds from the fake source) is
// covered by the Go unit test TestFakeSnapshotShape; the snapshot is an
// unsolicited push, which the request/reply control API can't capture, so
// the full push path is asserted through the browser below.

test.describe('wash-disks browser', () => {
  test.setTimeout(20_000);

  test('palette → renders topology tree + detail + fullness', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.keyboard.press('Control+Space');
    const input = page.locator('[data-testid="palette-input"]');
    await expect(input).toBeFocused();
    await input.fill('disks');
    await expect(page.locator('[data-testid="palette-item-com.wash.disks"]')).toBeVisible();
    await page.keyboard.press('Enter');

    const el = page.locator('wash-app-disks');
    await expect(el).toBeVisible();

    // Sections + a physical disk row from the fake snapshot.
    await expect(el.locator('[data-testid="disks-section-sec-fs"]')).toBeVisible({ timeout: 6_000 });
    await expect(el.locator('[data-testid="disks-section-sec-disks"]')).toBeVisible();
    await expect(el.locator('[data-testid="disks-section-sec-vol"]')).toBeVisible();
    await expect(el.locator('[data-testid="disks-row-disk:nvme0n1"]')).toBeVisible();

    // Filesystem-oriented list incl. a ZFS dataset (unprivileged, no scan).
    await expect(el.locator('[data-testid="disks-row-fs:/tank/ds"]')).toBeVisible();
    await el.locator('[data-testid="disks-row-fs:/tank/ds"]').click();
    await expect(el.locator('[data-testid="disks-fullness"]')).toBeVisible();

    // Every manager kind has a row.
    await expect(el.locator('[data-testid="disks-row-md:md0"]')).toBeVisible();
    await expect(el.locator('[data-testid="disks-row-vg:vg0"]')).toBeVisible();
    await expect(el.locator('[data-testid^="disks-row-btrfs:"]')).toBeVisible();
    await expect(el.locator('[data-testid="disks-row-pool:tank"]')).toBeVisible();

    // Default selection (first disk) shows the detail title.
    await expect(el.locator('[data-testid="disks-detail-title"]')).toBeVisible();

    // Select a mounted partition → a fullness bar renders.
    await el.locator('[data-testid="disks-row-part:nvme0n1p2"]').click();
    await expect(el.locator('[data-testid="disks-fullness"]')).toBeVisible();
  });

  test('SMART check renders a health badge + attributes', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await page.keyboard.press('Control+Space');
    const input = page.locator('[data-testid="palette-input"]');
    await input.fill('disks');
    await page.keyboard.press('Enter');

    const el = page.locator('wash-app-disks');
    await expect(el.locator('[data-testid="disks-row-disk:nvme0n1"]')).toBeVisible({ timeout: 6_000 });
    await el.locator('[data-testid="disks-row-disk:nvme0n1"]').click();

    // The fake source replies to the smart request without going through
    // wash-priv, so the badge + panel appear deterministically.
    await el.locator('[data-testid="disks-smart-check"]').click();
    await expect(el.locator('[data-testid="disks-smart-badge"]')).toHaveText('PASSED', { timeout: 5_000 });
    await expect(el.locator('[data-testid="disks-smart-panel"]')).toBeVisible();
  });
});
