// wash-to-wash mount capstone (docs/MOUNT.md): two real VMs, real SSH + SFTP +
// FUSE + the shared watch service. VM-A is the desktop; VM-B is a wash host. From
// A's desktop we connect to B (com.wash.remote), mount one of B's folders as a
// real FUSE mount at ~/wash/remote/<host>/…, and assert:
//
//   1. mount: the wash-connect UI reports the folder "mounted".
//   2. data: a local fm on A browses + writes B's files through FUSE/SFTP.
//   3. watch: a change on B surfaces live in A's fm — proving B's inotify →
//      wash-fswatchd → com.wash.fswatch → fm.
//
// The torture test opens BOTH a local fm (on the mount) and a remote fm (on B)
// at the same folder and mutates from each side, asserting both UIs track it —
// two machines co-driving one tree.
//
// Window ordering: the mount controls live in wash-connect, so we mount BEFORE
// launching any other window (a new window composites on top and would intercept
// the click), and we dismiss wash-connect's auto-opened Launch dropdown first.

import { test, expect, remoteVmSkipReason, vmLogin, REMOTE_HOST } from '../fixtures/remote-vm';
import type { Page, Locator } from '@playwright/test';

test.beforeEach(() => {
  const reason = remoteVmSkipReason();
  test.skip(reason !== null, reason ?? '');
});

// Mount B's home. The supervisor runs as `wash` on A (home /home/wash), so the
// mount base is /home/wash/wash/remote and the mountpoint is deterministic:
// base + sanitized-host + basename(remoteRoot).
const REMOTE_DIR = '/home/wash';
const MOUNT_POINT = '/home/wash/wash/remote/wash_at_10.77.0.2/wash';

async function connectToB(page: Page, url: string): Promise<Locator> {
  await page.goto(url);
  await vmLogin(page);
  await expect(page.locator('wash-app-session')).toBeVisible({ timeout: 60_000 });
  await page.locator('[data-testid="sidebar-section-header-remote"]').click();
  await page.locator('[data-testid="remote-manage"]').click();
  const connect = page.locator('wash-app-connect');
  await expect(connect).toBeVisible({ timeout: 30_000 });
  await connect.locator('[data-testid="connect-host-input"]').fill(REMOTE_HOST);
  await connect.locator('[data-testid="connect-submit"]').click();
  await expect(connect.locator('[data-testid="connect-host-status"]')).toHaveAttribute('data-status', 'up', {
    timeout: 90_000,
  });
  return connect;
}

// closeLaunchMenu dismisses wash-connect's Launch dropdown, which auto-opens
// when B's catalog arrives and whose full-window backdrop would otherwise
// intercept clicks on the mount controls.
async function closeLaunchMenu(connect: Locator) {
  const backdrop = connect.locator('[data-testid="connect-launch-backdrop"]');
  try {
    await backdrop.waitFor({ state: 'visible', timeout: 20_000 });
    await backdrop.click({ force: true });
    await backdrop.waitFor({ state: 'hidden', timeout: 5_000 });
  } catch {
    /* menu never opened — the mount controls are already reachable */
  }
}

// mountFolder mounts dir on B and waits for the row to report "mounted". Must run
// while wash-connect is the top window (i.e. before launching anything else).
async function mountFolder(connect: Locator, dir: string) {
  await closeLaunchMenu(connect);
  await connect.locator('[data-testid="connect-mount-input"]').fill(dir);
  await connect.locator('[data-testid="connect-mount-submit"]').click();
  await expect(connect.locator('[data-testid="connect-mount"]').first()).toHaveAttribute('data-status', 'mounted', {
    timeout: 60_000,
  });
}

// launchOnB launches one of B's apps via wash-connect's Launch dropdown
// (re-opening it if closed). wash-connect must be the top window.
async function launchOnB(connect: Locator, appId: string) {
  const item = connect.locator(`[data-testid="connect-launch-${appId}"]`);
  if (!(await item.isVisible().catch(() => false))) {
    await connect.locator('[data-testid="connect-launch"]').click();
  }
  await expect(item).toBeVisible({ timeout: 30_000 });
  await item.click();
}

async function navFm(fm: Locator, path: string) {
  await fm.locator('[data-testid="fm-path"]').fill(path);
  await fm.locator('[data-testid="fm-path"]').press('Enter');
}

async function newFileInFm(fm: Locator, name: string) {
  await fm.locator('[data-testid="fm-new-file"]').click();
  const input = fm.locator('[data-testid="fm-pending-new-input"]');
  await expect(input).toBeVisible({ timeout: 10_000 });
  await input.fill(name);
  await input.press('Enter');
}

test('mount a remote folder, browse it, and watch a B-side change propagate', async ({ remoteVm, page }) => {
  test.setTimeout(300_000);
  const connect = await connectToB(page, remoteVm.url);

  // Mount FIRST, while wash-connect is the only window.
  await mountFolder(connect, REMOTE_DIR);

  // A remote fm on B is our B-side mutator: a file it creates is a real change
  // on B's disk, observed by B's inotify.
  await launchOnB(connect, 'com.wash.fm');
  const remoteFm = page.locator('wash-app-fm').last();
  await expect(remoteFm).toBeVisible({ timeout: 60_000 });
  await navFm(remoteFm, REMOTE_DIR);

  // A local fm on A, pointed at the FUSE mount of the same folder.
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  const localFm = page.locator('wash-app-fm').last();
  await expect(localFm).toBeVisible({ timeout: 30_000 });
  await navFm(localFm, MOUNT_POINT);

  // Create a file on B (via the remote fm) → it must surface live in the local
  // fm through the mount's watch channel (B inotify → wash-fswatchd →
  // com.wash.fswatch → fm), with no manual reload.
  await newFileInFm(remoteFm, 'wm_live.txt');
  await expect(localFm.locator('[data-testid="fm-entry-wm_live.txt"]')).toBeVisible({ timeout: 40_000 });
});

test('torture: local fm + remote fm on the same folder both track changes', async ({ remoteVm, page }) => {
  test.setTimeout(300_000);
  const connect = await connectToB(page, remoteVm.url);

  await mountFolder(connect, REMOTE_DIR);

  // Remote fm (on B) and local fm (on the mount), same folder.
  await launchOnB(connect, 'com.wash.fm');
  const remoteFm = page.locator('wash-app-fm').nth(0);
  await expect(remoteFm).toBeVisible({ timeout: 60_000 });
  await navFm(remoteFm, REMOTE_DIR);

  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  const localFm = page.locator('wash-app-fm').nth(1);
  await expect(localFm).toBeVisible({ timeout: 30_000 });
  await navFm(localFm, MOUNT_POINT);

  // Mutate from B (remote fm): must appear in BOTH — the remote fm by its own
  // listing, the local fm via the mount's watch channel.
  await newFileInFm(remoteFm, 'from_b.txt');
  await expect(remoteFm.locator('[data-testid="fm-entry-from_b.txt"]')).toBeVisible({ timeout: 40_000 });
  await expect(localFm.locator('[data-testid="fm-entry-from_b.txt"]')).toBeVisible({ timeout: 40_000 });

  // Mutate from A through the FUSE mount (local fm): a real write over SFTP that
  // lands on B and must surface in the remote fm via B's inotify.
  await newFileInFm(localFm, 'from_a.txt');
  await expect(localFm.locator('[data-testid="fm-entry-from_a.txt"]')).toBeVisible({ timeout: 40_000 });
  await expect(remoteFm.locator('[data-testid="fm-entry-from_a.txt"]')).toBeVisible({ timeout: 40_000 });
});
