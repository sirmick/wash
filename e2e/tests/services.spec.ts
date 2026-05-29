// wash-services end-to-end. Drives the app with a stubbed systemctl
// on PATH so the listing, search, refresh, and action paths can be
// asserted without touching the host's real init system.
//
// The stub is a small bash script that:
//   - emits canned `list-units --output=json` + `list-unit-files`
//     responses so the BE has a stable two-unit catalog to parse
//   - logs every action invocation (start/stop/enable/…) to a file
//     the test reads back to prove the action made it through
//     wash-priv → fakesudo → systemctl
//
// One stub dir is created at module load; the action log is
// truncated before each test that asserts against it. test.use is
// static (no per-test routerOpts function), so the stub dir's path
// has to be ready at import time.

import { test, expect } from '../fixtures/router';
import { chmodSync, mkdtempSync, readFileSync, writeFileSync, truncateSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { webcrypto } from 'node:crypto';

// Crypto helpers ported from priv.spec.ts so the services test can
// approve+unlock priv via the control socket (no UI in kiosk mode).
const HKDF_INFO = 'wash-priv/password/v1';

function b64encode(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString('base64');
}
function b64decode(s: string): Uint8Array {
  return new Uint8Array(Buffer.from(s, 'base64'));
}

async function encryptPassword(
  password: string,
  bePubRaw: Uint8Array,
): Promise<{ ciphertext: string; fe_pubkey: string; nonce: string }> {
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
  const pwBytes = new TextEncoder().encode(password);
  const ct = new Uint8Array(await subtle.encrypt({ name: 'AES-GCM', iv: nonce }, aesKey, pwBytes));
  const fePubRaw = new Uint8Array(await subtle.exportKey('raw', fe.publicKey));
  return {
    ciphertext: b64encode(ct),
    fe_pubkey: b64encode(fePubRaw),
    nonce: b64encode(nonce),
  };
}

// Canonical stub responses. Two units: cups is active (Stop visible),
// foo is inactive (Start visible). Descriptions populate the
// secondary text column.
const STUB_UNITS_JSON = JSON.stringify([
  { unit: 'cups.service',  load: 'loaded', active: 'active',   sub: 'running', description: 'CUPS Scheduler' },
  { unit: 'foo.service',   load: 'loaded', active: 'inactive', sub: 'dead',    description: 'Foo Daemon' },
]);
const STUB_UNIT_FILES = [
  'cups.service  enabled  enabled',
  'foo.service   disabled disabled',
  '',
].join('\n');

const stubDir = mkdtempSync(join(tmpdir(), 'wash-e2e-srv-stub-'));
const actionLog = join(stubDir, 'action.log');
writeFileSync(actionLog, '');
{
  // The shim handles four invocation shapes:
  //   systemctl list-units --type=service ... --output=json
  //   systemctl list-unit-files --type=service ...
  //   systemctl <verb> <unit>             (start | stop | restart | reload)
  //   systemctl enable|disable <unit>
  // Anything else exits 1 so unhandled cases fail loudly.
  const shim = `#!/usr/bin/env bash
log='${actionLog}'
case "$1" in
  list-units)
    cat <<'JSON'
${STUB_UNITS_JSON}
JSON
    ;;
  list-unit-files)
    cat <<'EOF'
${STUB_UNIT_FILES}
EOF
    ;;
  start|stop|restart|reload|enable|disable)
    printf '%s %s\\n' "$1" "$2" >> "$log"
    ;;
  *)
    echo "stub-systemctl: unhandled: $*" >&2
    exit 1
    ;;
esac
`;
  const path = join(stubDir, 'systemctl');
  writeFileSync(path, shim);
  chmodSync(path, 0o755);
}

const emptyBinDir = mkdtempSync(join(tmpdir(), 'wash-e2e-srv-empty-'));

test.describe('wash-services (stubbed systemctl)', () => {
  test.use({
    routerOpts: {
      kiosk: 'com.wash.services',
      apps: ['services', 'priv'],
      fakesudo: true,
      // Stub goes first so the BE's exec.LookPath + spawned actions
      // both find the shim instead of the host's real systemctl.
      // Append the inherited PATH so /usr/bin (etc) stay reachable
      // for everything else the router + apps shell out to.
      extraEnv: { PATH: `${stubDir}:${process.env.PATH ?? ''}` },
    },
  });

  test.beforeEach(() => {
    // Reset the action log so each test asserts only against its own
    // invocations.
    truncateSync(actionLog, 0);
  });

  test('lists stubbed services with descriptions + state', async ({ page, router }) => {
    await page.goto(router.url);
    const app = page.locator('wash-app-services');
    await expect(app).toBeVisible();
    await expect(app.locator('[data-testid="srv-list"]')).toBeVisible();

    // Both stubbed units present.
    await expect(app.locator('[data-testid="srv-row-cups.service"]')).toBeVisible();
    await expect(app.locator('[data-testid="srv-row-foo.service"]')).toBeVisible();

    // Description column renders.
    await expect(app.locator('[data-testid="srv-row-cups.service"]')).toContainText('CUPS Scheduler');
    await expect(app.locator('[data-testid="srv-row-foo.service"]')).toContainText('Foo Daemon');

    // Active row offers Stop (Start is hidden); inactive row
    // offers Start. data-active is set on the row from the badge.
    await expect(app.locator('[data-testid="srv-row-cups.service"]')).toHaveAttribute('data-active', 'true');
    await expect(app.locator('[data-testid="srv-stop-cups.service"]')).toBeVisible();
    await expect(app.locator('[data-testid="srv-start-foo.service"]')).toBeVisible();

    // Status bar carries init flavour + count.
    await expect(app.locator('[data-testid="srv-status"]')).toContainText('init: systemd');
    await expect(app.locator('[data-testid="srv-status"]')).toContainText('2 / 2');
  });

  test('search filter narrows the list', async ({ page, router }) => {
    await page.goto(router.url);
    const app = page.locator('wash-app-services');
    await expect(app.locator('[data-testid="srv-row-foo.service"]')).toBeVisible();

    await app.locator('[data-testid="srv-search"]').fill('cups');
    await expect(app.locator('[data-testid="srv-row-foo.service"]')).toHaveCount(0);
    await expect(app.locator('[data-testid="srv-row-cups.service"]')).toBeVisible();
    await expect(app.locator('[data-testid="srv-status"]')).toContainText('1 / 2');

    // Empty filter restores both rows.
    await app.locator('[data-testid="srv-search"]').fill('');
    await expect(app.locator('[data-testid="srv-row-foo.service"]')).toBeVisible();
  });

  test('Start on inactive unit reaches stubbed systemctl through wash-priv', async ({ page, router }) => {
    await page.goto(router.url);
    const app = page.locator('wash-app-services');
    await expect(app.locator('[data-testid="srv-row-foo.service"]')).toBeVisible();

    // Trigger the action; wash-priv (a background service since M7)
    // gets a fresh request. This test runs in kiosk mode with no
    // session app, so the sidebar UI isn't present — we drive priv
    // approve + unlock via the control socket like priv.spec does.
    await app.locator('[data-testid="srv-start-foo.service"]').click();

    // Resolve the wash-priv instance for direct addressing.
    const privLaunched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.priv' });
    expect(privLaunched.t).toBe('launched');
    const privInst = privLaunched.instance_id as string;

    // services' Start path uses run_inline with NoPrompt; wash-priv
    // auto-prompts as soon as it ingests the request because priv is
    // locked. The need_password log line carries both the pubkey and
    // the req_id, so a single wait gets us both.
    const np = await router.waitForLog(
      /wash-priv: need_password be_pubkey=([A-Za-z0-9+/=]+) \(auto-prompt req=(\S+)\)/,
      5_000,
    );
    const bePub = b64decode(np.match(/be_pubkey=([A-Za-z0-9+/=]+)/)![1]);
    const enc = await encryptPassword('test123', bePub);
    await router.controlRequest({
      t: 'msg', instance_id: privInst,
      data: { kind: 'unlock', ciphertext: enc.ciphertext, fe_pubkey: enc.fe_pubkey, nonce: enc.nonce },
    });

    // Wait for the shim to log the call. fakesudo execs systemctl,
    // and our stub appends "start foo.service" to action.log.
    await expect.poll(() => {
      try { return readFileSync(actionLog, 'utf8'); }
      catch { return ''; }
    }, { timeout: 10000 }).toContain('start foo.service');
  });
});

test.describe('wash-services (no init system on PATH)', () => {
  test.use({
    routerOpts: {
      kiosk: 'com.wash.services',
      apps: ['services'],
      // Empty bin dir on PATH so the BE finds neither systemctl nor
      // rc-service; detectInit returns initUnknown and the FE flips
      // to the placeholder.
      extraEnv: { PATH: emptyBinDir },
    },
  });

  test('shows "no init system detected" placeholder', async ({ page, router }) => {
    await page.goto(router.url);
    const app = page.locator('wash-app-services');
    await expect(app).toBeVisible();
    await expect(app.locator('[data-testid="srv-no-init"]')).toBeVisible();
    await expect(app.locator('[data-testid="srv-no-init"]')).toContainText(/no supported init system/i);
    // Status bar reflects unknown init.
    await expect(app.locator('[data-testid="srv-status"]')).toContainText('init: —');
  });
});
