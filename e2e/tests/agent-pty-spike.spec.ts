// SPIKE (docs/AGENT_TERMINAL.md §4): can a WINDOWLESS background service
// own a pty whose bytes reach the browser?
//
// The whole "agentd owns ACP terminals" design rests on it. pty.Open takes a
// window id and agentd is SurfaceBackground with WindowID() == 0; the router
// binds raw channels to the SHELL rather than to a window, which suggests
// that is fine — but that is an inference from reading router.go, and the
// design is too load-bearing to build on an inference.
//
// So: agentd opens a pty on window 0 and logs its channel id. The PAGE then
// mounts that channel with window.wash.openRawChannel — the same shell API
// wash-connect's FE uses on a channel its BE opened — and we assert the
// bytes arrive. No FE changes, so nothing but the question is under test.
//
// Delete this file when M2 replaces the spike verb with CreateTerminal.

import { test, expect } from '../fixtures/router';

test.use({ routerOpts: { apps: ['session', 'agentd'] } });

test('a windowless service pty streams to the browser', async ({ page, router }) => {
  test.setTimeout(45_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  // Singleton: launch returns the running instance rather than a second one.
  const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.agentd' });
  expect(launched.t).toBe('launched');
  const instanceID = String(launched.instance_id ?? '');
  expect(instanceID).not.toBe('');

  const cursor = router.logCursor();
  await router.sendAppMsg(instanceID, { kind: 'agent_pty_spike' });

  // The channel id is the handle a real CreateTerminal would return.
  const line = await router.waitForLog(/agentd: pty spike (channel=\d+|FAILED)/, 15_000, cursor);
  expect(line, 'agentd could not open a pty from a windowless service').not.toContain('FAILED');
  const channelID = Number(/channel=(\d+)/.exec(line)![1]);
  expect(channelID).toBeGreaterThan(0);

  // Mount it from the page exactly as an app's FE would.
  const got = await page.evaluate(async (id) => {
    const w = window as unknown as {
      wash: { openRawChannel(id: number, onBytes: (b: Uint8Array) => void): () => void };
    };
    return await new Promise<string>((resolve) => {
      let seen = '';
      // Held indirectly: the router replays a channel's buffered bytes at
      // subscribe time, so this callback can fire DURING openRawChannel —
      // before the const would have been initialised.
      let stop: (() => void) | null = null;
      const done = () => {
        stop?.();
        resolve(seen);
      };
      stop = w.wash.openRawChannel(id, (bytes) => {
        seen += new TextDecoder().decode(bytes);
        if (seen.includes('wash-spike-ok')) done();
      });
      setTimeout(done, 10_000);
    });
  }, channelID);

  expect(got, 'no bytes reached the browser from the service-owned pty').toContain('wash-spike-ok');
});
