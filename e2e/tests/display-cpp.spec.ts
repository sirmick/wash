// wash-display (C++) proves a non-Go BE speaks the wash wire protocol.
//
// The compositor itself needs wlroots, but its wash-side half — the
// fd-3 framed-JSON handshake and the multi-window contract
// (docs/DISPLAY.md §4) — is plain sockets and builds without it. This
// test launches the C++ binary through the real router and drives its
// fake-display reference path, asserting it created real wash windows.
//
// Requires the native binary: `cmake --build wash-display/build` then
// copy build/wash-display to out/. BE-only (no browser).

import { test, expect } from '../fixtures/router';

test.use({ routerOpts: { apps: ['session', 'display'] } });

test.describe('wash-display C++ wire client', () => {
  test('a C++ BE handshakes and creates windows via window.create', async ({ router }) => {
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.display' });
    expect(launched.t).toBe('launched');
    const inst = launched.instance_id as string;

    const resp = await router.sendAppMsg(inst, { kind: 'display_open', id: 'd1', n: 2 });
    expect(resp.kind).toBe('display_opened');

    const wins = (resp.windows ?? []) as Array<{ win: number }>;
    expect(wins).toHaveLength(2);
    for (const w of wins) expect(w.win).toBeGreaterThan(0);
    expect(new Set(wins.map((w) => w.win)).size).toBe(2);

    // The router logged a window.create for the C++ instance.
    await router.waitForLog(new RegExp(`window\\.create instance=${inst} win=`), 5_000);
  });
});
