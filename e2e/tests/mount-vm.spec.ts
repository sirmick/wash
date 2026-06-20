// wash-to-wash mount capstone (docs/MOUNT.md): two real VMs, real SSH + SFTP +
// FUSE + the shared watch service. VM-A is the desktop; VM-B is a wash host. From
// A's desktop we connect to B (com.wash.remote), mount one of B's folders as a
// real FUSE mount at ~/wash/remote/<host>/…, and assert:
//
//   1. mount: the wash-connect UI reports the folder "mounted".
//   2. data: a local fm on A browses B's files through FUSE/SFTP.
//   3. watch: a change on B surfaces live in A's fm — B's inotify →
//      wash-fswatchd → com.wash.fswatch → fm.
//
// The torture test opens BOTH a local fm (on the mount) and a remote fm (on B)
// at the same folder and mutates from each side, asserting both UIs track it.
//
// Notes: (a) the mount controls live in wash-connect, so we mount BEFORE opening
// any other window (a new window composites on top and would intercept the
// click). (b) A remote app's custom-element tag is mangled per origin
// (`wash-app-fm-<slug>`), so `wash-app-fm` matches only the LOCAL fm; the remote
// fm is reached through its host-striped window.

import { test, expect, remoteVmSkipReason, vmLogin, REMOTE_HOST } from '../fixtures/remote-vm';
import type { Page, Locator } from '@playwright/test';

test.beforeEach(() => {
  const reason = remoteVmSkipReason();
  test.skip(reason !== null, reason ?? '');
});

// Mount B's home. The supervisor runs as `wash` on A (home /home/wash), so the
// mountpoint is deterministic: base + sanitized-host + basename(remoteRoot).
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

// closeLaunchMenu dismisses wash-connect's Launch dropdown (auto-opens on
// connect; its full-window backdrop would intercept the mount click).
async function closeLaunchMenu(connect: Locator) {
  const backdrop = connect.locator('[data-testid="connect-launch-backdrop"]');
  try {
    await backdrop.waitFor({ state: 'visible', timeout: 20_000 });
    await backdrop.click({ force: true });
    await backdrop.waitFor({ state: 'hidden', timeout: 5_000 });
  } catch {
    /* menu never opened — controls already reachable */
  }
}

// Mounts dir on B; must run while wash-connect is the top window.
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
  for (let i = 0; i < 3 && !(await item.isVisible().catch(() => false)); i++) {
    await connect.locator('[data-testid="connect-launch"]').click();
  }
  await expect(item).toBeVisible({ timeout: 30_000 });
  await item.click();
}

// openLocalApp opens the start menu (the "Apps" taskbar button) and launches a
// LOCAL app by its menu label.
async function openLocalApp(page: Page, name: RegExp) {
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name }).click();
}

async function navFm(scope: Locator, path: string) {
  await scope.locator('[data-testid="fm-path"]').fill(path);
  await scope.locator('[data-testid="fm-path"]').press('Enter');
}

async function newFileInFm(scope: Locator, name: string) {
  await scope.locator('[data-testid="fm-new-file"]').click();
  const input = scope.locator('[data-testid="fm-pending-new-input"]');
  await expect(input).toBeVisible({ timeout: 10_000 });
  await input.fill(name);
  await input.press('Enter');
}

test('mount a remote folder, browse it, and watch a B-side change propagate', async ({ remoteVm, page }) => {
  test.setTimeout(300_000);
  const connect = await connectToB(page, remoteVm.url);

  // Mount FIRST (wash-connect is the only window), then launch a B terminal as
  // the mutator — driven via .xterm, so its mangled element tag doesn't matter.
  await mountFolder(connect, REMOTE_DIR);
  await launchOnB(connect, 'com.wash.term');
  const term = page.locator('.xterm').last();
  await expect(term).toBeVisible({ timeout: 60_000 });
  await expect(term).toContainText(/[$#>]/, { timeout: 30_000 });

  // Local fm on A, pointed at the FUSE mount.
  await openLocalApp(page, /^Files$/);
  const localFm = page.locator('wash-app-fm');
  await expect(localFm).toBeVisible({ timeout: 30_000 });
  await navFm(localFm, MOUNT_POINT);

  // Create a file on B (real change on B's disk) — it must surface live in the
  // local fm through the mount's watch channel, no reload.
  await term.click();
  await page.keyboard.type('touch /home/wash/wm_live.txt');
  await page.keyboard.press('Enter');
  await expect(localFm.locator('[data-testid="fm-entry-wm_live.txt"]')).toBeVisible({ timeout: 40_000 });
});

test('torture: local fm + remote fm on the same folder both track changes', async ({ remoteVm, page }) => {
  test.setTimeout(300_000);
  const connect = await connectToB(page, remoteVm.url);

  await mountFolder(connect, REMOTE_DIR);

  // Remote fm (on B): reached through its host-striped window (its element tag
  // is mangled per origin). Launch it, then scope all ops to that window.
  await launchOnB(connect, 'com.wash.fm');
  const stripe = page.locator(`[data-testid="wash-host-stripe"][data-origin="${REMOTE_HOST}"]`);
  await expect(stripe).toBeVisible({ timeout: 60_000 });
  const remoteFm = page.locator('.wash-window', { has: stripe });
  await navFm(remoteFm, REMOTE_DIR);

  // Local fm (on the mount). The mangled remote tag means `wash-app-fm` is the
  // local one only.
  await openLocalApp(page, /^Files$/);
  const localFm = page.locator('wash-app-fm');
  await expect(localFm).toBeVisible({ timeout: 30_000 });
  await navFm(localFm, MOUNT_POINT);

  // Mutate from B (remote fm): appears in BOTH — remote fm by its own listing,
  // local fm via the mount's watch channel.
  await newFileInFm(remoteFm, 'from_b.txt');
  await expect(remoteFm.locator('[data-testid="fm-entry-from_b.txt"]')).toBeVisible({ timeout: 40_000 });
  await expect(localFm.locator('[data-testid="fm-entry-from_b.txt"]')).toBeVisible({ timeout: 40_000 });

  // Mutate from A through the FUSE mount (local fm): a real write over SFTP that
  // lands on B and surfaces in the remote fm via B's inotify.
  await newFileInFm(localFm, 'from_a.txt');
  await expect(localFm.locator('[data-testid="fm-entry-from_a.txt"]')).toBeVisible({ timeout: 40_000 });
  await expect(remoteFm.locator('[data-testid="fm-entry-from_a.txt"]')).toBeVisible({ timeout: 40_000 });
});
