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

    // Trigger the action. wash-priv pops an approval window because
    // we enabled fakesudo. The priv window is part of the chrome
    // surface, not nested in the services app.
    await app.locator('[data-testid="srv-start-foo.service"]').click();

    const priv = page.locator('wash-app-priv');
    await expect(priv).toBeVisible({ timeout: 5000 });

    // The new request appears in priv's queue with a per-row Approve
    // button; we don't know the req_id in advance (BE-generated) so
    // we target the first one by testid prefix.
    const approve = priv.locator('[data-testid^="priv-req-approve-"]').first();
    await expect(approve).toBeVisible({ timeout: 5000 });
    // force: true — the password modal opens immediately on click
    // and overlays the button, which trips Playwright's post-click
    // stability check even though the click landed.
    await approve.click({ force: true });

    // wash-priv is locked, so clicking Approve opens the password
    // modal. Fill the fakesudo-accepted password and submit.
    await expect(priv.locator('[data-testid="priv-pw-modal"]')).toBeVisible({ timeout: 5000 });
    await priv.locator('[data-testid="priv-pw-input"]').fill('test123');
    await priv.locator('[data-testid="priv-pw-submit"]').click({ force: true });

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
