// wash-settings — the "Remote" panel supplied by the com.wash.remote
// supervisor (docs/SETTINGS.md §2 revised, docs/REMOTE.md). Mirrors the
// vscode/display panel-discovery tests: registering wash-remote is enough for
// its manifest settings_panel to reach the catalog's `panels` list, for
// settings to render the Remote section, and for a click to load + define +
// mount wash-settings-panel-remote. The panel subscribes to the supervisor's
// state on mount; with no host connected it renders the empty states, and its
// "Open Connect…" button spawns the com.wash.connect window — the graceful
// teardown verbs (disconnect/unmount) are exercised against a real host in the
// two-VM remote suite, not here.

import { test, expect } from '../fixtures/router';

test.use({
  routerOpts: {
    apps: ['session', 'settings', 'connect', 'remote'],
    xdgConfig: true,
  },
});

test.describe('wash-settings — Remote panel (wash-remote supervisor)', () => {
  test.setTimeout(30_000);

  test('discovers + mounts the Remote panel and shows the idle empty states', async ({ page, router }) => {
    await page.goto(router.url);
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const app = page.locator('wash-app-settings');
    await expect(app).toBeVisible();

    // The Remote section is NOT hardcoded in settings — it comes from
    // wash-remote's manifest settings_panel, advertised in the catalog.
    const remote = app.getByRole('button', { name: 'Remote' });
    await expect(remote).toBeVisible();

    // Clicking pulls the panel.js over panel.read and blob-imports it,
    // defining + mounting the supplied element. On mount it subscribes to the
    // supervisor; with nothing connected the panel shows its empty states.
    await remote.click();
    await expect(app.locator('wash-settings-panel-remote')).toBeAttached({ timeout: 15_000 });
    await expect(app.locator('[data-testid="remote-sessions"]')).toHaveCount(0);
    await expect(app.locator('[data-testid="remote-mounts"]')).toHaveCount(0);
    await expect(app).toContainText('No active connections.');
    await expect(app).toContainText('No remote folders mounted.');
  });

  test('"Open Connect…" launches the com.wash.connect window', async ({ page, router }) => {
    await page.goto(router.url);
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const app = page.locator('wash-app-settings');
    await expect(app).toBeVisible();

    await app.getByRole('button', { name: 'Remote' }).click();
    const open = app.locator('[data-testid="remote-open-connect"]');
    await expect(open).toBeVisible({ timeout: 15_000 });

    // port.launch → settings BE SpawnRequest(com.wash.connect): the windowed
    // app mounts in the desktop.
    await open.click();
    await expect(page.locator('wash-app-connect')).toBeVisible({ timeout: 15_000 });
  });
});
