// wash-settings — round-trips ~/.config/wash/desktop.json via the BE's
// settings.read / settings.write surface, and the wash-session BE's
// fswatch on the same file. Tests:
//
//   1. BE-level read/write: settings.write persists desktop.json
//      atomically; settings.read echoes it back via settings.value.
//   2. End-to-end: writing a new fallback_color via the Settings UI
//      propagates through disk + wash-session fswatch to the
//      browser's desktop background.
//   3. Singleton: two launches return the same instance_id.
//   4. Bad domain returns settings.read_err.
//
// xdgConfig fixture points XDG_CONFIG_HOME at a per-test tmpdir, so the
// developer's real desktop.json is never touched.

import { test, expect, displaySkipReason } from '../fixtures/router';
import { readFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { homedir } from 'node:os';
import { execSync } from 'node:child_process';

// The Developer pane drives the wash-vscode service, whose status
// reflects a real code-server install. Skip when it's absent (the badge
// would read "not installed" and the start/restart controls hide).
function codeServerInstalled(): boolean {
  const managed = join(homedir(), '.cache', 'wash', 'code-server', 'current', 'bin', 'code-server');
  if (existsSync(managed)) return true;
  try {
    execSync('command -v code-server', { stdio: 'ignore' });
    return true;
  } catch {
    return false;
  }
}

test.use({
  routerOpts: {
    apps: ['session', 'settings'],
    xdgConfig: true,
  },
});

const desktopJSONPath = (xdg: string) => join(xdg, 'wash', 'desktop.json');

test.describe('wash-settings', () => {
  test.setTimeout(15_000);

  test('settings.write persists desktop.json; settings.read returns the value', async ({ router }) => {
    const launch = await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    expect(launch.t).toBe('launched');
    const instance = launch.instance_id as string;
    expect(instance).toBeTruthy();

    const value = {
      wallpaper: { mode: 'tile', fallback_color: '#aabbcc' },
      clock: { format: '12h', show_seconds: true },
      taskbar: { position: 'top' },
    };
    const writeReply = await router.sendAppMsg(instance, {
      kind: 'settings.write',
      id: 'w1',
      domain: 'desktop',
      value,
    });
    expect(writeReply.kind).toBe('settings.write_ok');
    expect(writeReply.id).toBe('w1');

    // File on disk matches what we wrote.
    const path = desktopJSONPath(router.xdgConfigHome);
    expect(existsSync(path)).toBe(true);
    const onDisk = JSON.parse(readFileSync(path, 'utf8'));
    expect(onDisk).toEqual(value);

    // settings.read should hand it back via settings.value.
    const readReply = await router.sendAppMsg(instance, {
      kind: 'settings.read',
      id: 'r1',
      domain: 'desktop',
    });
    expect(readReply.kind).toBe('settings.value');
    expect(readReply.domain).toBe('desktop');
    expect(readReply.value).toEqual(value);
  });

  test('unknown domain → settings.read_err', async ({ router }) => {
    const launch = await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const instance = launch.instance_id as string;

    const reply = await router.sendAppMsg(instance, {
      kind: 'settings.read',
      id: 'r-bad',
      domain: 'not-a-domain',
    });
    expect(reply.kind).toBe('settings.read_err');
    expect(reply.code).toBe('bad_request');
  });

  test('singleton: two launches return the same instance', async ({ router }) => {
    const a = await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const b = await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    expect(a.t).toBe('launched');
    expect(b.t).toBe('launched');
    expect(b.instance_id).toBe(a.instance_id);
  });

  test('settings.write propagates to wash-session via fswatch (browser re-paints background)', async ({ page, router }) => {
    await page.goto(router.url);
    const session = page.locator('wash-app-session');
    await expect(session).toBeVisible();

    // Wash-session paints fallback_color into its host element's
    // background. The default (no desktop.json) is the radial
    // gradient — change to a flat hex via settings.write and assert
    // the browser picks it up.
    const launch = await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const instance = launch.instance_id as string;

    const written = await router.sendAppMsg(instance, {
      kind: 'settings.write',
      id: 'wp1',
      domain: 'desktop',
      value: {
        wallpaper: { fallback_color: '#123456' },
        taskbar: { position: 'top' },
      },
    });
    expect(written.kind).toBe('settings.write_ok');

    // fswatch is debounced 60ms in wash-session; allow a generous
    // budget for the FE to receive desktop.config and re-paint.
    await expect.poll(
      async () => session.evaluate((el) => getComputedStyle(el).backgroundColor),
      { timeout: 5_000 },
    ).toBe('rgb(18, 52, 86)'); // #123456 → rgb
  });
});

// ---------------------------------------------------------------------
// Theme packs (docs/PACKS). The Desktop pane's Theme gallery writes a
// `pack` id to desktop.json; wash-session fswatches it and applies the
// pack's --wash-* scheme to document.documentElement (cascades into every
// light-DOM app) plus its wallpaper. Built-in wallpapers are served over
// the asset.read channel (window.wash.fetchAsset), not bundled.
// ---------------------------------------------------------------------

test.describe('wash-settings — Theme packs', () => {
  test.setTimeout(15_000);

  test('Theme gallery renders the built-in pack cards', async ({ page, router }) => {
    await page.goto(router.url);
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const app = page.locator('wash-app-settings');
    await expect(app).toBeVisible();
    // Desktop is the default pane; the Theme gallery sits at its top.
    await expect(app.locator('[data-testid="pack-card-midnight"]')).toBeVisible();
    await expect(app.locator('[data-testid="pack-card-tokyo"]')).toBeVisible();
    await expect(app.locator('[data-testid="pack-card-seoul"]')).toBeVisible();
    await expect(app.locator('[data-testid="pack-card-retro"]')).toBeVisible();
  });

  test('selecting the Seoul pack applies a light scheme', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const app = page.locator('wash-app-settings');
    await expect(app).toBeVisible();
    // Seoul is the first light pack — ivory window surface, sumi-ink text.
    await app.locator('[data-testid="pack-card-seoul"]').click();
    await expect
      .poll(() => page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--wash-bg-window').trim()), { timeout: 5_000 })
      .toBe('#f6efdd');
  });

  test('selecting a pack writes `pack` to desktop.json and re-themes the live desktop', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const app = page.locator('wash-app-settings');
    await expect(app).toBeVisible();

    // The active pack's scheme lands as --wash-* vars on documentElement.
    const bgWindowVar = () =>
      page.evaluate(() =>
        getComputedStyle(document.documentElement).getPropertyValue('--wash-bg-window').trim(),
      );
    const path = desktopJSONPath(router.xdgConfigHome);
    const packOnDisk = () => (existsSync(path) ? JSON.parse(readFileSync(path, 'utf8')).pack ?? null : null);

    // Midnight is applied on load (its var == the tokens default).
    await expect.poll(bgWindowVar, { timeout: 5_000 }).toBe('#181828');

    // Pick Tokyo (Solarized Dark): the scheme re-themes live, and `pack`
    // persists through the FE → settings.write → desktop.json → fswatch chain.
    await app.locator('[data-testid="pack-card-tokyo"]').click();
    await expect.poll(bgWindowVar, { timeout: 5_000 }).toBe('#002b36');
    await expect.poll(packOnDisk, { timeout: 5_000 }).toBe('tokyo');

    // Back to Midnight: the var reverts (applyScheme clears the prior pack).
    await app.locator('[data-testid="pack-card-midnight"]').click();
    await expect.poll(bgWindowVar, { timeout: 5_000 }).toBe('#181828');
    await expect.poll(packOnDisk, { timeout: 5_000 }).toBe('midnight');
  });

  test('built-in wallpaper survives the asset.read channel intact (multi-chunk)', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    // fetchAsset pulls the wallpaper over asset.read. The SVG spans several
    // 64KB frames; a streamer regression (reused buffer) scrambles
    // multi-chunk payloads, so assert the reassembled bytes are coherent.
    const r = await page.evaluate(async () => {
      const a = await window.wash.fetchAsset('wallpapers/midnight.svg');
      const txt = new TextDecoder().decode(a.bytes);
      return { len: a.bytes.length, mime: a.mime, head: txt.slice(0, 5), tail: txt.trim().slice(-6) };
    });
    expect(r.len).toBeGreaterThan(64 * 1024); // multi-chunk → exercises the streamer
    expect(r.mime).toBe('image/svg+xml');
    expect(r.head).toBe('<svg ');
    expect(r.tail).toBe('</svg>');
  });
});

// ---------------------------------------------------------------------
// Service-control panels (docs/SETTINGS.md §4). These render host-side
// over the BE's svc.* relay: the panel subscribes to a background
// service (svc.send), the service pushes snapshots back (svc.recv), and
// the Restart button cycles the singleton via svc.restart → the router's
// app.restart verb (settings declares the `restart` cap).
// ---------------------------------------------------------------------

test.describe('wash-settings — Developer panel (wash-vscode service)', () => {
  test.skip(!codeServerInstalled(), 'code-server not installed (Developer panel needs a real install)');
  test.use({
    routerOpts: {
      apps: ['session', 'settings', 'vscode'],
      xdgConfig: true,
    },
  });
  test.setTimeout(30_000);

  test('renders live service status from the vscode background service', async ({ page, router }) => {
    await page.goto(router.url);
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const app = page.locator('wash-app-settings');
    await expect(app).toBeVisible();

    // Switch to the Developer pane.
    await app.getByRole('button', { name: 'Developer' }).click();

    // The status badge only renders once the service answered the
    // on-mount subscribe with a status snapshot — i.e. the full
    // svc.send → service → svc.recv round-trip worked. code-server is
    // installed but not started, so the live state is "stopped".
    const badge = app.locator('[data-testid="service-badge"]');
    await expect(badge).toBeVisible({ timeout: 15_000 });
    await expect(badge).toHaveAttribute('data-tone', 'off');
    await expect(badge).toContainText('stopped');

    // Installed → the start + restart controls are present (proves the
    // status payload's `installed` flag drove the UI).
    await expect(app.locator('[data-testid="dev-start"]')).toBeVisible();
    await expect(app.locator('[data-testid="dev-restart"]')).toBeVisible();
  });

  test('Restart cycles the service via svc.restart → app.restart', async ({ page, router }) => {
    await page.goto(router.url);
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const app = page.locator('wash-app-settings');
    await expect(app).toBeVisible();
    await app.getByRole('button', { name: 'Developer' }).click();
    await expect(app.locator('[data-testid="dev-restart"]')).toBeVisible({ timeout: 15_000 });

    // The on-mount subscribe already spawned the singleton service; read
    // its current instance id (control launch resolves, doesn't respawn).
    const before = await router.controlRequest({ t: 'launch', app_id: 'com.wash.vscode' });
    const instA = before.instance_id as string;
    expect(instA).toBeTruthy();

    await app.locator('[data-testid="dev-restart"]').click();

    // svc.restart → settings BE RestartApp → router terminates the old
    // singleton and respawns a fresh one. Give the (fast, no-code-server)
    // respawn a moment so the resolve below sees the NEW instance rather
    // than racing the terminate window, then assert the id changed.
    await page.waitForTimeout(1_500);
    await expect.poll(async () => {
      const r = await router.controlRequest({ t: 'launch', app_id: 'com.wash.vscode' });
      return r.instance_id as string;
    }, { timeout: 15_000, intervals: [500, 1_000, 1_000] }).not.toBe(instA);
  });
});

test.describe('wash-settings — Display panel (wash-display compositor)', () => {
  test.use({
    routerOpts: {
      apps: ['session', 'settings', 'display'],
      xdgConfig: true,
    },
  });
  // Compositor boot (wlroots + Xwayland) is the slow part.
  test.setTimeout(60_000);
  // Needs the native compositor binary; skip (not fail) on a dep-less
  // checkout. The "compositor absent" describe below has no such guard —
  // it tests the not-installed path and runs everywhere.
  test.beforeEach(() => {
    const reason = displaySkipReason();
    test.skip(reason !== null, reason ?? '');
  });

  test('populates running + wayland display + window count from the live compositor', async ({ page, router }) => {
    await page.goto(router.url);
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const app = page.locator('wash-app-settings');
    await expect(app).toBeVisible();
    await app.getByRole('button', { name: 'Display' }).click();

    // The compositor autoboots on shell connect and answers the panel's
    // subscribe with a display.state snapshot ADDRESSED to the subscriber
    // (send_app_msg_to, not send_app_msg(0) — a background service has no
    // FE half). Before that fix this panel showed "not installed"; this
    // assertion locks the regression down.
    const badge = app.locator('[data-testid="service-badge"]');
    await expect(badge).toBeVisible({ timeout: 30_000 });
    await expect(badge).toHaveAttribute('data-tone', 'on');
    await expect(badge).toContainText('running');
    // The supplied panel element actually mounted (discover → load → mount).
    await expect(app.locator('wash-settings-panel-display')).toBeAttached();

    // Live snapshot fields: a wayland-N socket and a numeric window count
    // (0 with no clients mapped yet).
    await expect(app).toContainText(/wayland-\d+/);
    await expect(app).toContainText('Native windows');
  });

  test('Restart compositor cycles the singleton without flipping to not-installed', async ({ page, router }) => {
    await page.goto(router.url);
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const app = page.locator('wash-app-settings');
    await expect(app).toBeVisible();
    await app.getByRole('button', { name: 'Display' }).click();

    const restart = app.locator('[data-testid="display-restart"]');
    await expect(restart).toBeVisible({ timeout: 30_000 });

    const before = await router.controlRequest({ t: 'launch', app_id: 'com.wash.display' });
    const instA = before.instance_id as string;
    expect(instA).toBeTruthy();

    await restart.click();

    // The compositor teardown+respawn is heavier than vscode (wlroots +
    // Xwayland), so settle longer before resolving the new instance id.
    await page.waitForTimeout(3_000);
    await expect.poll(async () => {
      const r = await router.controlRequest({ t: 'launch', app_id: 'com.wash.display' });
      return r.instance_id as string;
    }, { timeout: 30_000, intervals: [1_000, 1_000, 2_000] }).not.toBe(instA);

    // The supplied panel stays mounted across the restart round-trip.
    await expect(app.locator('wash-settings-panel-display')).toBeAttached();
  });
});

test.describe('wash-settings — Display panel, compositor absent', () => {
  test.use({
    routerOpts: {
      // No 'display' app registered → no panel descriptor in the catalog.
      apps: ['session', 'settings'],
      xdgConfig: true,
    },
  });
  test.setTimeout(15_000);

  // App-supplied panels are invisible when their package is absent: with
  // no wash-display registered the router emits no panel descriptor, so
  // settings shows no Display section at all (the old in-panel "not
  // installed" hint is gone — absence is the signal, docs/SETTINGS.md).
  test('no Display section when wash-display is unregistered', async ({ page, router }) => {
    await page.goto(router.url);
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const app = page.locator('wash-app-settings');
    await expect(app).toBeVisible();
    // Desktop (the one built-in pane) is always present…
    await expect(app.getByRole('button', { name: 'Desktop' })).toBeVisible();
    // …but there is no Display nav section to click.
    await expect(app.getByRole('button', { name: 'Display' })).toHaveCount(0);
  });
});

// The discovery → panel.read → mount path itself, with no external dep
// (code-server / wlroots): registering wash-vscode is enough for its
// manifest settings_panel to reach the catalog's `panels` list, for
// settings to render the Developer section, and for a click to load +
// define + mount the supplied element. Proves the new mechanism end to
// end (docs/SETTINGS.md §2 revised, WIRE.md §5.1/§8).
test.describe('wash-settings — app-supplied panel discovery', () => {
  test.use({
    routerOpts: {
      apps: ['session', 'settings', 'vscode'],
      xdgConfig: true,
    },
  });
  test.setTimeout(30_000);

  test('discovers + loads + mounts the vscode-supplied Developer panel', async ({ page, router }) => {
    await page.goto(router.url);
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const app = page.locator('wash-app-settings');
    await expect(app).toBeVisible();

    // The Developer section is NOT hardcoded in settings — it comes from
    // wash-vscode's manifest settings_panel, advertised in the catalog.
    const dev = app.getByRole('button', { name: 'Developer' });
    await expect(dev).toBeVisible();

    // Clicking pulls vscode's panel.js over panel.read and blob-imports it,
    // defining + mounting wash-settings-panel-vscode (no code-server needed
    // — the panel renders "Connecting to service…" until status arrives).
    await dev.click();
    await expect(app.locator('wash-settings-panel-vscode')).toBeAttached({ timeout: 15_000 });
  });
});
