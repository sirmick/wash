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
// Window notes: the mount controls live in wash-connect, so we mount BEFORE
// opening any other window. Windows cascade (offset top-left), and a click
// anywhere on a window raises it — raiseWindow() clicks the peeking titlebar. A
// remote app's element tag is mangled per origin, so windows are addressed by
// their frame (`.wash-window`), scoped by content: the local fm window contains
// `wash-app-fm`; the remote window carries B's host stripe.

import { test, expect, remoteVmSkipReason, vmLogin, REMOTE_HOST } from '../fixtures/remote-vm';
import type { Page, Locator } from '@playwright/test';

test.beforeEach(() => {
  const reason = remoteVmSkipReason();
  test.skip(reason !== null, reason ?? '');
});

const REMOTE_DIR = '/home/wash';
const MOUNT_POINT = '/home/wash/wash/remote/wash_at_10.77.0.2/wash';
// A second B folder mounted in the lifecycle test, so the Remote panel lists
// more than one row and per-row unmount can be shown to drop only its own.
const SECOND_DIR = '/tmp';
const MOUNT_SECOND = '/home/wash/wash/remote/wash_at_10.77.0.2/tmp';

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

async function closeLaunchMenu(connect: Locator) {
  const backdrop = connect.locator('[data-testid="connect-launch-backdrop"]');
  try {
    await backdrop.waitFor({ state: 'visible', timeout: 20_000 });
    await backdrop.click({ force: true });
    await backdrop.waitFor({ state: 'hidden', timeout: 5_000 });
  } catch {
    /* menu never opened */
  }
}

async function mountFolder(connect: Locator, dir: string) {
  await closeLaunchMenu(connect);
  await connect.locator('[data-testid="connect-mount-input"]').fill(dir);
  await connect.locator('[data-testid="connect-mount-submit"]').click();
  await expect(connect.locator('[data-testid="connect-mount"]').first()).toHaveAttribute('data-status', 'mounted', {
    timeout: 60_000,
  });
}

// mountFolderAt mounts dir and waits for the SPECIFIC mount row (addressed by
// its derived local mount point) to report mounted — so a second mount doesn't
// race the first()-based assertion above.
async function mountFolderAt(connect: Locator, dir: string, mountPoint: string) {
  await closeLaunchMenu(connect);
  await connect.locator('[data-testid="connect-mount-input"]').fill(dir);
  await connect.locator('[data-testid="connect-mount-submit"]').click();
  await expect(connect.locator(`[data-testid="connect-mount"][data-mount="${mountPoint}"]`)).toHaveAttribute(
    'data-status',
    'mounted',
    { timeout: 60_000 },
  );
}

// openSettingsRemotePanel opens the Settings window and switches to the
// "Remote" pane supplied by com.wash.remote, returning the settings app
// locator. The panel subscribes to A's supervisor on mount, so it reflects the
// live sessions + mounts the connect window set up.
async function openSettingsRemotePanel(page: Page): Promise<Locator> {
  await openLocalApp(page, /^Settings$/);
  const settings = page.locator('wash-app-settings');
  await expect(settings).toBeVisible({ timeout: 30_000 });
  await settings.getByRole('button', { name: 'Remote' }).click();
  await expect(settings.locator('wash-settings-panel-remote')).toBeAttached({ timeout: 30_000 });
  return settings;
}

async function launchOnB(connect: Locator, appId: string) {
  const item = connect.locator(`[data-testid="connect-launch-${appId}"]`);
  for (let i = 0; i < 3 && !(await item.isVisible().catch(() => false)); i++) {
    await connect.locator('[data-testid="connect-launch"]').click();
  }
  await expect(item).toBeVisible({ timeout: 30_000 });
  await item.click();
}

async function openLocalApp(page: Page, name: RegExp) {
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name }).click();
}

// raiseWindow lifts a window to the top — a click anywhere on it raises it
// (focusWindow), and cascade offsets leave the top-left titlebar exposed.
async function raiseWindow(win: Locator) {
  await win.click({ position: { x: 14, y: 10 } });
}

async function navFm(win: Locator, path: string) {
  await win.locator('[data-testid="fm-path"]').fill(path);
  await win.locator('[data-testid="fm-path"]').press('Enter');
}

async function newFileInFm(win: Locator, name: string) {
  await win.locator('[data-testid="fm-new-file"]').click();
  const input = win.locator('[data-testid="fm-pending-new-input"]');
  await expect(input).toBeVisible({ timeout: 10_000 });
  await input.fill(name);
  await input.press('Enter');
}

// localFmWindow / remoteWindow address a window by its frame, scoped by content.
function localFmWindow(page: Page): Locator {
  return page.locator('.wash-window', { has: page.locator('wash-app-fm') });
}
function remoteWindow(page: Page): Locator {
  return page.locator('.wash-window', {
    has: page.locator(`[data-testid="wash-host-stripe"][data-origin="${REMOTE_HOST}"]`),
  });
}

test('mount a remote folder, browse it, and watch a B-side change propagate', async ({ remoteVm, page }) => {
  test.setTimeout(300_000);
  const connect = await connectToB(page, remoteVm.url);

  await mountFolder(connect, REMOTE_DIR);

  // Launch a B terminal (top + auto-focused) and background a touch loop on B —
  // files keep appearing for ~30s, so they land AFTER the local fm starts
  // watching, proving LIVE watch (not just an initial listing). No window
  // raising needed: we type while the term is freshly on top.
  await launchOnB(connect, 'com.wash.term');
  const term = page.locator('.xterm').last();
  await expect(term).toBeVisible({ timeout: 60_000 });
  await expect(term).toContainText(/[$#>]/, { timeout: 30_000 });
  await page.keyboard.type('for i in $(seq 1 15); do touch /home/wash/wm_$i.txt; sleep 2; done &');
  await page.keyboard.press('Enter');

  // Local fm on A, pointed at the FUSE mount.
  await openLocalApp(page, /^Files$/);
  const localFm = localFmWindow(page);
  await expect(localFm).toBeVisible({ timeout: 30_000 });
  await navFm(localFm, MOUNT_POINT);

  // A late file from the loop must surface live (created well after fm watches).
  await expect(localFm.locator('[data-testid="fm-entry-wm_12.txt"]')).toBeVisible({ timeout: 60_000 });
});

test('torture: local fm + remote fm on the same folder both track changes', async ({ remoteVm, page }) => {
  test.setTimeout(300_000);
  const connect = await connectToB(page, remoteVm.url);

  await mountFolder(connect, REMOTE_DIR);

  // Open the remote fm (on B) FIRST — it must be launched while wash-connect is
  // on top — then the local fm (on the mount). Cascade puts the local fm down-
  // right and on top, with the remote fm's titlebar exposed up-left.
  await launchOnB(connect, 'com.wash.fm');
  const remoteFm = remoteWindow(page);
  await expect(remoteFm).toBeVisible({ timeout: 60_000 });
  await navFm(remoteFm, REMOTE_DIR);

  await openLocalApp(page, /^Files$/);
  const localFm = localFmWindow(page);
  await expect(localFm).toBeVisible({ timeout: 30_000 });
  await navFm(localFm, MOUNT_POINT);

  // Mutate from A first (local fm is the top window — no raise). A real write
  // over FUSE/SFTP that lands on B and surfaces in the remote fm via B's inotify.
  await newFileInFm(localFm, 'from_a.txt');
  await expect(localFm.locator('[data-testid="fm-entry-from_a.txt"]')).toBeVisible({ timeout: 40_000 });
  await expect(remoteFm.locator('[data-testid="fm-entry-from_a.txt"]')).toBeVisible({ timeout: 60_000 });

  // Then mutate from B: raise the remote fm (its titlebar is exposed up-left, not
  // covered by the local fm) and create a file. It must appear in BOTH — the
  // remote fm by its own listing, the local fm via the mount's watch channel.
  await raiseWindow(remoteFm);
  await newFileInFm(remoteFm, 'from_b.txt');
  await expect(remoteFm.locator('[data-testid="fm-entry-from_b.txt"]')).toBeVisible({ timeout: 40_000 });
  await expect(localFm.locator('[data-testid="fm-entry-from_b.txt"]')).toBeVisible({ timeout: 60_000 });
});

test('chaos: killing the data ssh self-heals — a post-kill change still reaches the fm', async ({ remoteVm, page }) => {
  test.setTimeout(300_000);
  const connect = await connectToB(page, remoteVm.url);

  await mountFolder(connect, REMOTE_DIR);

  // A B terminal backgrounds a touch loop, so files keep appearing on B across
  // the kill — a file created AFTER the kill proves the data path reconnected.
  await launchOnB(connect, 'com.wash.term');
  const bterm = page.locator('.xterm').last();
  await expect(bterm).toBeVisible({ timeout: 60_000 });
  await expect(bterm).toContainText(/[$#>]/, { timeout: 30_000 });
  await page.keyboard.type('for i in $(seq 1 40); do touch /home/wash/chaos_$i.txt; sleep 1; done &');
  await page.keyboard.press('Enter');

  // Local fm on the mount; an early file confirms the mount works.
  await openLocalApp(page, /^Files$/);
  const localFm = localFmWindow(page);
  await expect(localFm).toBeVisible({ timeout: 30_000 });
  await navFm(localFm, MOUNT_POINT);
  await expect(localFm.locator('[data-testid="fm-entry-chaos_3.txt"]')).toBeVisible({ timeout: 40_000 });

  // Kill the data ssh on A via a LOCAL terminal. The watch channel (a separate
  // ssh wash-fswatchd) survives, so it keeps firing fs_events that drive the fm
  // re-list — which must re-dial the dropped SFTP connection.
  await openLocalApp(page, /^Terminal$/);
  const aterm = page.locator('.xterm').last();
  await expect(aterm).toBeVisible({ timeout: 30_000 });
  await expect(aterm).toContainText(/[$#>]/, { timeout: 30_000 });
  await page.keyboard.type('pkill -f sftp');
  await page.keyboard.press('Enter');

  // A file created well after the kill still surfaces → the mount self-healed.
  await expect(localFm.locator('[data-testid="fm-entry-chaos_30.txt"]')).toBeVisible({ timeout: 120_000 });
});

// Lifecycle through the Settings "Remote" panel (the supervisor-vended panel,
// docs/SETTINGS.md / docs/REMOTE.md §6.1): mount two of B's folders, assert the
// panel lists the live session + both mounts, then tear them down FROM THE PANEL
// — graceful unmount (escalating FUSE flush → lazy → abort) makes each mount row
// vanish (only its own), and Disconnect makes the session row vanish. This is
// the real-host assertion the standalone settings-remote-panel.spec stubs.
test('Settings Remote panel: mount two folders, assert listed, then unmount + disconnect → gone', async ({ remoteVm, page }) => {
  test.setTimeout(300_000);
  const connect = await connectToB(page, remoteVm.url);

  // Mount two distinct B folders from the connect window (its mount controls).
  await mountFolderAt(connect, REMOTE_DIR, MOUNT_POINT);
  await mountFolderAt(connect, SECOND_DIR, MOUNT_SECOND);

  const settings = await openSettingsRemotePanel(page);

  // The panel reflects the supervisor's live state: the connected session…
  const sessionRow = settings.locator(`[data-testid="remote-session-${REMOTE_HOST}"]`);
  await expect(sessionRow).toBeVisible({ timeout: 30_000 });
  await expect(sessionRow).toHaveAttribute('data-status', 'up');

  // …and both mounts, each addressed by its derived local mount point.
  const washRow = settings.locator(`[data-testid="remote-mount"][data-mount="${MOUNT_POINT}"]`);
  const secondRow = settings.locator(`[data-testid="remote-mount"][data-mount="${MOUNT_SECOND}"]`);
  await expect(washRow).toBeVisible({ timeout: 30_000 });
  await expect(secondRow).toBeVisible({ timeout: 30_000 });
  await expect(settings.locator('[data-testid="remote-mount"]')).toHaveCount(2);

  // Unmount the second folder FROM THE PANEL → its row goes, the first stays.
  await secondRow.locator('[data-testid="remote-unmount"]').click();
  await expect(secondRow).toHaveCount(0, { timeout: 60_000 });
  await expect(washRow).toBeVisible();

  // Unmount the first → no mounts left.
  await washRow.locator('[data-testid="remote-unmount"]').click();
  await expect(washRow).toHaveCount(0, { timeout: 60_000 });
  await expect(settings.locator('[data-testid="remote-mount"]')).toHaveCount(0);

  // Disconnect the session FROM THE PANEL → the session row goes too.
  await sessionRow.locator('[data-testid="remote-disconnect"]').click();
  await expect(sessionRow).toHaveCount(0, { timeout: 60_000 });
});
