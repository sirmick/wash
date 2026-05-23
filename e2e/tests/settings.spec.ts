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

import { test, expect } from '../fixtures/router';
import { readFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';

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
