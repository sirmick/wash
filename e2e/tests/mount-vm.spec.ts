// wash-to-wash mount capstone (docs/MOUNT.md): two real VMs, real SSH + SFTP +
// FUSE + the shared watch service. VM-A is the desktop; VM-B is a wash host. From
// A's desktop we connect to B (com.wash.remote), mount one of B's folders as a
// real FUSE mount at ~/wash/remote/<host>/…, and assert:
//
//   1. mount: the wash-connect UI reports the folder "mounted".
//   2. data: a local fm on A browses B's files through the FUSE/SFTP path.
//   3. watch: a change made on B (via a remote terminal) surfaces live in A's
//      fm — proving B's inotify → wash-fswatchd → com.wash.fswatch → fm.
//
// The torture test then opens BOTH a local fm (on the mount) and a remote fm
// (on B) at the same folder, churns the filesystem, and asserts both UIs track
// it — humans co-driving one tree from two machines.

import { test, expect, remoteVmSkipReason, vmLogin, REMOTE_HOST } from '../fixtures/remote-vm';

test.beforeEach(() => {
  const reason = remoteVmSkipReason();
  test.skip(reason !== null, reason ?? '');
});

// B-side paths. The supervisor runs as `wash` on A (home /home/wash), so the
// mount base is /home/wash/wash/remote and the mountpoint is deterministic.
const REMOTE_DIR = '/home/wash/torture';
const MOUNT_POINT = '/home/wash/wash/remote/wash_at_10.77.0.2/torture';

// connectToB drives wash-connect to SSH into VM-B and returns the connect app
// locator (its Launch dropdown lists B's catalog).
async function connectToB(page: import('@playwright/test').Page, url: string) {
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

// runOnB launches a fresh wash-term on B and runs one shell line in it. Returns
// the term locator so the caller can keep typing / asserting output.
async function launchRemoteTerm(page: import('@playwright/test').Page, connect: import('@playwright/test').Locator) {
  const termItem = connect.locator('[data-testid="connect-launch-com.wash.term"]');
  await expect(termItem).toBeVisible({ timeout: 30_000 });
  await termItem.click();
  const term = page.locator('.xterm').last();
  await expect(term).toBeVisible({ timeout: 60_000 });
  await expect(term).toContainText(/[$#>]/, { timeout: 30_000 });
  return term;
}

async function typeLine(page: import('@playwright/test').Page, term: import('@playwright/test').Locator, line: string) {
  await term.click();
  await page.keyboard.type(line);
  await page.keyboard.press('Enter');
}

test('mount a remote folder, browse it, and watch a B-side change propagate', async ({ remoteVm, page }) => {
  test.setTimeout(300_000);
  const connect = await connectToB(page, remoteVm.url);

  // Seed a folder on B (must exist before we mount + watch it).
  const term = await launchRemoteTerm(page, connect);
  await typeLine(page, term, `mkdir -p ${REMOTE_DIR} && echo hi > ${REMOTE_DIR}/seed.txt && echo SEED_DONE`);
  await expect(term).toContainText('SEED_DONE', { timeout: 20_000 });

  // Mount it: type the remote path into the host's mount input and submit.
  await connect.locator('[data-testid="connect-mount-input"]').fill(REMOTE_DIR);
  await connect.locator('[data-testid="connect-mount-submit"]').click();
  // The mount row reaches data-status="mounted" once the FUSE mount is up.
  await expect(connect.locator('[data-testid="connect-mount"]').first()).toHaveAttribute('data-status', 'mounted', {
    timeout: 60_000,
  });

  // Open a LOCAL fm on A and navigate to the mountpoint — this reads B's files
  // through the kernel FUSE mount over SFTP.
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  const fm = page.locator('wash-app-fm').last();
  await expect(fm).toBeVisible({ timeout: 30_000 });
  await fm.locator('[data-testid="fm-path"]').fill(MOUNT_POINT);
  await fm.locator('[data-testid="fm-path"]').press('Enter');
  await expect(fm.locator('[data-testid="fm-entry-seed.txt"]')).toBeVisible({ timeout: 30_000 });

  // Now mutate B directly and assert it surfaces live in A's fm — the watch
  // path (B inotify → wash-fswatchd → com.wash.fswatch → fm), no manual reload.
  await typeLine(page, term, `touch ${REMOTE_DIR}/live.txt`);
  await expect(fm.locator('[data-testid="fm-entry-live.txt"]')).toBeVisible({ timeout: 30_000 });
});

test('torture: local fm + remote fm on the same folder both track changes', async ({ remoteVm, page }) => {
  test.setTimeout(300_000);
  const connect = await connectToB(page, remoteVm.url);

  const term = await launchRemoteTerm(page, connect);
  await typeLine(page, term, `mkdir -p ${REMOTE_DIR} && echo READY`);
  await expect(term).toContainText('READY', { timeout: 20_000 });

  // Mount, and open a remote fm (on B) at the same folder.
  await connect.locator('[data-testid="connect-mount-input"]').fill(REMOTE_DIR);
  await connect.locator('[data-testid="connect-mount-submit"]').click();
  await expect(connect.locator('[data-testid="connect-mount"]').first()).toHaveAttribute('data-status', 'mounted', {
    timeout: 60_000,
  });

  const remoteFmItem = connect.locator('[data-testid="connect-launch-com.wash.fm"]');
  await expect(remoteFmItem).toBeVisible({ timeout: 30_000 });
  await remoteFmItem.click();
  const remoteFm = page.locator(`wash-app-fm[data-origin="${REMOTE_HOST}"]`).last();
  await expect(remoteFm).toBeVisible({ timeout: 60_000 });
  await remoteFm.locator('[data-testid="fm-path"]').fill(REMOTE_DIR);
  await remoteFm.locator('[data-testid="fm-path"]').press('Enter');

  // Open a LOCAL fm on the mount of the same folder.
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  const localFm = page.locator('wash-app-fm:not([data-origin])').last();
  await expect(localFm).toBeVisible({ timeout: 30_000 });
  await localFm.locator('[data-testid="fm-path"]').fill(MOUNT_POINT);
  await localFm.locator('[data-testid="fm-path"]').press('Enter');

  // A change made on B (the mutator) must appear in BOTH windows: the remote fm
  // via B's own inotify, the local fm via the mount's watch channel.
  await typeLine(page, term, `touch ${REMOTE_DIR}/from_b.txt`);
  await expect(remoteFm.locator('[data-testid="fm-entry-from_b.txt"]')).toBeVisible({ timeout: 30_000 });
  await expect(localFm.locator('[data-testid="fm-entry-from_b.txt"]')).toBeVisible({ timeout: 30_000 });

  // A change made through the LOCAL fm (a real write over the FUSE/SFTP path)
  // must land on B and surface in the remote fm.
  await localFm.locator('[data-testid="fm-new-file"]').click();
  const newInput = localFm.locator('[data-testid="fm-pending-new-input"]');
  await expect(newInput).toBeVisible({ timeout: 10_000 });
  await newInput.fill('from_a.txt');
  await newInput.press('Enter');
  await expect(remoteFm.locator('[data-testid="fm-entry-from_a.txt"]')).toBeVisible({ timeout: 30_000 });
  // And it is really on B's disk.
  await typeLine(page, term, `test -f ${REMOTE_DIR}/from_a.txt && echo A_FILE_ON_B`);
  await expect(term).toContainText('A_FILE_ON_B', { timeout: 20_000 });
});
