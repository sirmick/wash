// M1 full-stack music e2e (docs/AUDIO.md §5): the Winamp-skinned player
// driven in kiosk mode — no shell, no sidebar. Proves the Case-1 audio
// pipeline end to end:
//   1. the wash-music window mounts and Webamp renders (the JS classic
//      Winamp skin engine paints its main window),
//   2. the FE↔BE `tracks` round-trip drives the playlist (our synthesized
//      sample track appears), and
//   3. the audio is actually reachable over the ingress reverse-proxy —
//      a GET for sample.wav returns 200/206 (Range), proving the BE file
//      server + PublishIngress + router proxy chain works.

import { test, expect } from '../fixtures/router';

test.describe('music app (kiosk, host-side full stack)', () => {
  test.use({ routerOpts: { kiosk: 'com.wash.music', apps: ['music'] } });

  test('renders Webamp and serves the track over ingress', async ({ page, router }) => {
    // Catch the ingress fetch for the sample track. Register the waiter
    // before navigation so we never race the fetch webamp kicks off once
    // the playlist loads.
    const audio = page.waitForResponse(
      (r) => /\/app\/[^/]+\/sample\.wav/.test(r.url()),
      { timeout: 20_000 },
    );

    await page.goto(router.url);

    // The window mounts.
    await expect(page.locator('wash-app-music')).toBeVisible();

    // Webamp painted — a real, sized control (the volume slider) is
    // visible. The #webamp root itself is 0×0 (its windows are absolutely
    // positioned), so we assert on a control with a bounding box.
    await expect(page.getByRole('slider', { name: 'Volume Bar' })).toBeVisible();

    // The FE↔BE `tracks` round-trip drove the playlist with our sample
    // (served by the BE; "0:03" is the duration webamp read from the WAV).
    await expect(page.locator('#webamp')).toContainText('Wash Test Tone');

    // Audio is reachable over the ingress proxy — webamp fetched the WAV
    // (for metadata) at /app/<token>/sample.wav and got a 200/206 (Range).
    const resp = await audio;
    expect([200, 206]).toContain(resp.status());
  });
});
