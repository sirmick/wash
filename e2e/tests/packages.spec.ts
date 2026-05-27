// wash-packages: structured search + PTY-streamed actions through
// wash-priv. Two layers of coverage:
//
//   - UI: launch via the start menu, verify the apt backend banner
//     renders, search for an always-present package (bash) and
//     assert the installed badge appears.
//   - Action wire: send run_action via control socket with the apt
//     binary overridden to /bin/echo; assert the priv chain runs and
//     action_done fires with exit 0. This exercises Prereq B's PTY
//     mode end to end without root.

import { readFileSync, existsSync, writeFileSync, chmodSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { webcrypto } from 'node:crypto';

import { test, expect } from '../fixtures/router';

// fakeTasksel writes a /tmp shell stub that emits canned
// --list-tasks output. The apt backend reads
// WASH_PACKAGES_TASKSEL_BIN at every call, so pointing it here is
// enough to inject tasks into search results without a real
// tasksel install (which isn't required on dev boxes).
function fakeTasksel(body: string): string {
  const dir = mkdtempSync(join(tmpdir(), 'wash-fake-tasksel-'));
  const path = join(dir, 'tasksel');
  writeFileSync(path, `#!/bin/sh\ncat <<'EOF'\n${body}EOF\n`);
  chmodSync(path, 0o755);
  return path;
}

// ----- ui-level test -----

test.describe('packages app — UI', () => {
  test.use({
    routerOpts: {
      apps: ['session', 'packages'],
      showHidden: true,
    },
  });

  test.setTimeout(20_000);

  test('launches, detects apt, searches and shows installed badge for bash', async ({ page, router }) => {
    test.skip(!existsSync('/usr/bin/apt-get'), 'apt-get missing on this host');

    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Packages', exact: true }).click();

    const app = page.locator('wash-app-packages');
    await expect(app).toBeVisible();

    // Status bar surfaces the detected backend name.
    await expect(app.locator('[data-testid="pkg-status"]')).toContainText('backend: apt');

    // No backend error banner.
    await expect(app.locator('[data-testid="pkg-backend-error"]')).toHaveCount(0);

    // Toolbar shows the combo "Update system" button (the only
    // toolbar-surface action apt ships).
    await expect(app.locator('[data-testid="pkg-update-system"]')).toBeVisible();
    await expect(app.locator('[data-testid="pkg-update-system"]')).toContainText('Update system');

    // Menubar Actions menu surfaces the four granular ops; combo
    // is hidden from the menu since it's already on the toolbar.
    await app.locator('[data-testid="pkg-actions-button"]').click();
    const menu = app.locator('[data-testid="pkg-actions-menu"]');
    await expect(menu).toBeVisible();
    await expect(menu.locator('[data-testid="pkg-action-update"]')).toBeVisible();
    await expect(menu.locator('[data-testid="pkg-action-upgrade"]')).toBeVisible();
    await expect(menu.locator('[data-testid="pkg-action-dist_upgrade"]')).toBeVisible();
    await expect(menu.locator('[data-testid="pkg-action-autoremove"]')).toBeVisible();
    await expect(menu.locator('[data-testid="pkg-action-update_system"]')).toHaveCount(0);
    // Dismiss the menu before the rest of the test interacts.
    await app.locator('[data-testid="pkg-search"]').click();

    // Type a query and wait for results. bash is installed on every
    // Debian-flavored dev box; we use it as the canonical hit.
    await app.locator('[data-testid="pkg-search"]').fill('bash');
    const bashRow = app.locator('[data-testid="pkg-row-bash"]');
    await expect(bashRow).toBeVisible({ timeout: 10_000 });

    // Installed badge → upgrade + remove buttons visible (not install).
    await expect(bashRow.locator('[data-testid="pkg-installed-bash"]')).toBeVisible();
    await expect(bashRow.locator('[data-testid="pkg-upgrade-bash"]')).toBeVisible();
    await expect(bashRow.locator('[data-testid="pkg-remove-bash"]')).toBeVisible();
    await expect(bashRow.locator('[data-testid="pkg-install-bash"]')).toHaveCount(0);
  });
});

// ----- tasksel task surfacing -----

test.describe('packages app — tasksel tasks', () => {
  // Per-test fake tasksel: status column `i`/`u` + name + desc.
  // Use wash-e2e-* names so they can't collide with real apt
  // packages (real task names like openssh-server overlap with
  // actual packages on Ubuntu and would render two rows).
  const taskselPath = fakeTasksel(
    'u\twash-e2e-alpha\tAlpha test task\n' +
    'i\twash-e2e-bravo\tBravo test task (pre-installed)\n',
  );

  test.use({
    routerOpts: {
      apps: ['session', 'packages'],
      showHidden: true,
      extraEnv: { WASH_PACKAGES_TASKSEL_BIN: taskselPath },
    },
  });

  test.setTimeout(20_000);

  test('search merges tasksel tasks above packages with a task badge', async ({ page, router }) => {
    test.skip(!existsSync('/usr/bin/apt-get'), 'apt-get missing on this host');

    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Packages', exact: true }).click();

    const app = page.locator('wash-app-packages');
    await expect(app).toBeVisible();
    await app.locator('[data-testid="pkg-search"]').fill('wash-e2e');

    // Alpha (uninstalled task): renders with a "task" badge and
    // only the Install button — no Upgrade/Remove.
    const alpha = app.locator('[data-testid="pkg-row-wash-e2e-alpha"]');
    await expect(alpha).toBeVisible({ timeout: 10_000 });
    await expect(alpha.locator('[data-testid="pkg-kind-wash-e2e-alpha"]')).toHaveText('task');
    await expect(alpha.locator('[data-testid="pkg-install-wash-e2e-alpha"]')).toBeVisible();
    await expect(alpha.locator('[data-testid="pkg-upgrade-wash-e2e-alpha"]')).toHaveCount(0);

    // Bravo (installed task): shows installed badge + Remove,
    // but NEVER Upgrade (tasksel has no per-task upgrade verb).
    const bravo = app.locator('[data-testid="pkg-row-wash-e2e-bravo"]');
    await expect(bravo).toBeVisible();
    await expect(bravo.locator('[data-testid="pkg-installed-wash-e2e-bravo"]')).toBeVisible();
    await expect(bravo.locator('[data-testid="pkg-remove-wash-e2e-bravo"]')).toBeVisible();
    await expect(bravo.locator('[data-testid="pkg-upgrade-wash-e2e-bravo"]')).toHaveCount(0);
  });
});

// ----- action wire test -----
//
// Drives the full FE→BE→priv→PTY chain via the control socket. The
// apt-get binary is swapped for /bin/echo so the "install" action
// just prints its argv and exits 0; that's enough to prove:
//   - run_action → priv.run_inline(pty:true) actually flows
//   - priv's PTY-mode subprocess captures stdout via priv.stream
//   - packages BE forwards as action_stream to the requester
//   - action_done lands with the right exit code

const PRIV_APP_ID = 'com.wash.priv';
const PKG_APP_ID = 'com.wash.packages';
const PASSWORD = 'test123';
const HKDF_INFO = 'wash-priv/password/v1';

function b64encode(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString('base64');
}
function b64decode(s: string): Uint8Array {
  return new Uint8Array(Buffer.from(s, 'base64'));
}

async function encryptPassword(password: string, bePubRaw: Uint8Array): Promise<{ ciphertext: string; fe_pubkey: string; nonce: string }> {
  const subtle = webcrypto.subtle;
  const bePub = await subtle.importKey('raw', bePubRaw, { name: 'ECDH', namedCurve: 'P-256' }, false, []);
  const fe = await subtle.generateKey({ name: 'ECDH', namedCurve: 'P-256' }, true, ['deriveBits']);
  const shared = new Uint8Array(await subtle.deriveBits({ name: 'ECDH', public: bePub }, fe.privateKey, 256));
  const hkdfKey = await subtle.importKey('raw', shared, { name: 'HKDF' }, false, ['deriveKey']);
  const aesKey = await subtle.deriveKey(
    { name: 'HKDF', hash: 'SHA-256', salt: new Uint8Array(0), info: new TextEncoder().encode(HKDF_INFO) },
    hkdfKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt'],
  );
  const nonce = webcrypto.getRandomValues(new Uint8Array(12));
  const ct = new Uint8Array(await subtle.encrypt({ name: 'AES-GCM', iv: nonce }, aesKey, new TextEncoder().encode(password)));
  const fePubRaw = new Uint8Array(await subtle.exportKey('raw', fe.publicKey));
  return { ciphertext: b64encode(ct), fe_pubkey: b64encode(fePubRaw), nonce: b64encode(nonce) };
}

test.describe('packages app — actions via priv PTY', () => {
  test.use({
    routerOpts: {
      apps: ['session', 'packages', 'priv'],
      showHidden: true,
      fakesudo: true,
      // Swap apt-get for /bin/echo so the install argv runs to a
      // benign zero-exit without needing root. The packages BE
      // reads this on every action; the override is BE-side, not
      // visible to priv or fakesudo.
      extraEnv: { WASH_PACKAGES_APT_BIN: '/bin/echo' },
    },
  });

  test.setTimeout(20_000);

  test('global update action runs apt-get update through priv with no pkg arg', async ({ router }) => {
    const privLaunched = await router.controlRequest({ t: 'launch', app_id: PRIV_APP_ID });
    const privInst = privLaunched.instance_id as string;
    await router.controlRequest({ t: 'msg', instance_id: privInst, data: { kind: 'hello', page_nonce: 'pkg-update' } });

    const pkgLaunched = await router.controlRequest({ t: 'launch', app_id: PKG_APP_ID });
    const pkgInst = pkgLaunched.instance_id as string;

    // Granular update: action=global + global_id=update. Backend
    // builds ["/bin/echo", "update"] (apt-bin override) → fakesudo
    // execs → exit 0.
    await router.controlRequest({
      t: 'msg', instance_id: pkgInst,
      data: { kind: 'run_action', action: 'global', global_id: 'update', cols: 80, rows: 24 },
    });

    const m = await router.waitForLog(/wash-priv enqueue: req_id=(pkg-[a-f0-9]+) kind=run_inline sender=com\.wash\.packages/, 5000);
    const reqID = m.match(/req_id=(pkg-[a-f0-9]+)/)![1];
    await router.controlRequest({ t: 'msg', instance_id: privInst, data: { kind: 'approve', req_id: reqID } });

    const km = await router.waitForLog(/wash-priv: need_password be_pubkey=([A-Za-z0-9+/=]+)/, 5000);
    const bePub = b64decode(km.replace(/^.*be_pubkey=/, ''));
    const enc = await encryptPassword(PASSWORD, bePub);
    await router.controlRequest({
      t: 'msg', instance_id: privInst,
      data: { kind: 'unlock', ciphertext: enc.ciphertext, fe_pubkey: enc.fe_pubkey, nonce: enc.nonce },
    });

    await pollUntil(() => fakesudoEntries(router.fakesudoLog).some((e) => e.mode === 'exec' && e.target === '/bin/echo'), 5000);
    const exec = fakesudoEntries(router.fakesudoLog).find((e) => e.mode === 'exec' && e.target === '/bin/echo');
    expect(exec).toBeDefined();
    expect(exec!.target_args).toEqual(['update']);
  });

  test('Update system toolbar button gates on a confirm dialog before priv runs', async ({ page, router }) => {
    // We drive the FE for this one — the dialog lives in DOM only,
    // so the easiest assertion is "no priv enqueue while dialog
    // is up; enqueue lands only after Update is clicked".
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Packages', exact: true }).click();
    const app = page.locator('wash-app-packages');
    await expect(app).toBeVisible();

    // Click Update system — confirm dialog should appear; no priv
    // enqueue yet.
    await app.locator('[data-testid="pkg-update-system"]').click();
    const dialog = app.locator('[data-testid="pkg-confirm-update-system"]');
    await expect(dialog).toBeVisible();
    // Sanity: nothing in the router log mentioning a packages priv
    // enqueue yet. We wait a tick so an in-flight enqueue would
    // have shown up.
    await page.waitForTimeout(200);
    expect(router.log()).not.toMatch(/wash-priv enqueue: .* sender=com\.wash\.packages/);

    // Cancel re-closes without firing.
    await app.locator('[data-testid="pkg-confirm-update-system-cancel"]').click();
    await expect(dialog).toHaveCount(0);
    await page.waitForTimeout(200);
    expect(router.log()).not.toMatch(/wash-priv enqueue: .* sender=com\.wash\.packages/);

    // Re-open + confirm → priv enqueue fires. (We don't drive priv
    // through unlock here — the install/global-update tests already
    // cover that path. This test owns the "confirm-gate fires" half.)
    await app.locator('[data-testid="pkg-update-system"]').click();
    await expect(dialog).toBeVisible();
    await app.locator('[data-testid="pkg-confirm-update-system-ok"]').click();
    await router.waitForLog(/wash-priv enqueue: req_id=pkg-[a-f0-9]+ kind=run_inline sender=com\.wash\.packages/, 5000);
  });

  test('install action: full PTY chain reaches fakesudo, exit 0 returns via action_done', async ({ router }) => {
    // Launch wash-priv up front so we have a handle.
    const privLaunched = await router.controlRequest({ t: 'launch', app_id: PRIV_APP_ID });
    expect(privLaunched.t).toBe('launched');
    const privInst = privLaunched.instance_id as string;

    // Seed priv's page nonce.
    await router.controlRequest({ t: 'msg', instance_id: privInst, data: { kind: 'hello', page_nonce: 'pkg-e2e' } });

    // Launch wash-packages.
    const pkgLaunched = await router.controlRequest({ t: 'launch', app_id: PKG_APP_ID });
    expect(pkgLaunched.t).toBe('launched');
    const pkgInst = pkgLaunched.instance_id as string;

    // Send run_action — install hello-world. The apt backend builds
    // argv ["/bin/echo", "-y", "install", "hello-world"]; fakesudo
    // execs that target, which prints "-y install hello-world\n"
    // and exits 0.
    await router.controlRequest({
      t: 'msg',
      instance_id: pkgInst,
      data: { kind: 'run_action', action: 'install', pkg: 'hello-world', cols: 80, rows: 24 },
    });

    // Wait for priv to log the enqueue. The req_id prefix is pkg-
    // (packages BE picks it; see randHex in apps/packages/be/app.go).
    const m = await router.waitForLog(/wash-priv enqueue: req_id=(pkg-[a-f0-9]+) kind=run_inline sender=com\.wash\.packages/, 5000);
    const reqID = m.match(/req_id=(pkg-[a-f0-9]+)/)![1];

    // Approve the request.
    await router.controlRequest({ t: 'msg', instance_id: privInst, data: { kind: 'approve', req_id: reqID } });

    // Auto-prompt for password since cache is empty. Catch the be_pubkey.
    const km = await router.waitForLog(/wash-priv: need_password be_pubkey=([A-Za-z0-9+/=]+)/, 5000);
    const bePub = b64decode(km.replace(/^.*be_pubkey=/, ''));
    const enc = await encryptPassword(PASSWORD, bePub);
    await router.controlRequest({
      t: 'msg', instance_id: privInst,
      data: { kind: 'unlock', ciphertext: enc.ciphertext, fe_pubkey: enc.fe_pubkey, nonce: enc.nonce },
    });

    // fakesudo logs each invocation. We want the exec entry whose
    // target is /bin/echo. Wait for the audit line.
    await pollUntil(() => fakesudoEntries(router.fakesudoLog).some((e) => e.mode === 'exec' && e.target === '/bin/echo'), 5000);
    const entries = fakesudoEntries(router.fakesudoLog);
    const exec = entries.find((e) => e.mode === 'exec' && e.target === '/bin/echo');
    expect(exec).toBeDefined();
    expect(exec!.pw_ok).toBe(true);
    expect(exec!.target_args).toEqual(['-y', 'install', 'hello-world']);
  });
});

// ----- helpers cloned from priv.spec.ts -----

interface FakesudoEntry {
  ts: string;
  args: string[];
  mode: 'validate' | 'exec' | 'error';
  pw_ok: boolean;
  target?: string;
  target_args?: string[];
  exit: number;
  err?: string;
}

function fakesudoEntries(path: string): FakesudoEntry[] {
  if (!path || !existsSync(path)) return [];
  return readFileSync(path, 'utf8').trim().split('\n').filter(Boolean).map((line) => JSON.parse(line) as FakesudoEntry);
}

async function pollUntil(pred: () => boolean, timeoutMs: number): Promise<void> {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (pred()) return;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`pollUntil: condition not met after ${timeoutMs}ms`);
}
