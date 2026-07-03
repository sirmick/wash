// Unit tests for installIngressClipboardBridge — the fix that makes an
// embedded app's (code-server's) copy/paste work on wash's insecure LAN
// origin, where navigator.clipboard is withheld (#9). Framework-free: we
// stub the host window.wash + a minimal document (execCommand mirror path)
// and hand in fake embedded windows. Run via `make fe-unit`.

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { installIngressClipboardBridge } from './ingress-clipboard.ts';

// ---- host-side stubs (what washCopyText / washPasteText reach for) ----

interface FakeWash {
  data: string;
  clipboardSetText(text: string): void;
  clipboardGetText(): Promise<string>;
}

function installHost(): FakeWash {
  const w: FakeWash = {
    data: '',
    clipboardSetText(text) {
      w.data = text;
    },
    clipboardGetText() {
      return Promise.resolve(w.data);
    },
  };
  (globalThis as unknown as { window: unknown }).window = { wash: w };
  // washCopyText also best-effort mirrors to the SYSTEM clipboard via a
  // throwaway <textarea> + execCommand. In node there's no navigator
  // clipboard, so it takes the execCommand path — give it just enough DOM
  // to not throw (the mirror is best-effort; its outcome doesn't matter).
  (globalThis as unknown as { document: unknown }).document = {
    createElement: () => ({ setAttribute() {}, select() {}, remove() {}, style: {}, value: '' }),
    body: { appendChild() {} },
    activeElement: null,
    execCommand: () => true,
  };
  return w;
}

// A stand-in for an insecure-origin embedded window: a navigator with no
// clipboard, exactly like Chromium on http://192.168.x.x.
function insecureWindow(): { navigator: { clipboard?: unknown } } {
  return { navigator: {} };
}

test('bridges copy/paste when the embed has no navigator.clipboard', async () => {
  const host = installHost();
  const win = insecureWindow();

  const installed = installIngressClipboardBridge(win as unknown as Window);
  assert.equal(installed, true, 'should install on an insecure embed');

  const clip = win.navigator.clipboard as { readText(): Promise<string>; writeText(t: string): Promise<void> };
  assert.equal(typeof clip.readText, 'function');
  assert.equal(typeof clip.writeText, 'function');

  // Paste in the embed reads the wash clipboard…
  host.data = 'from-wash-42';
  assert.equal(await clip.readText(), 'from-wash-42');

  // …and copy in the embed lands in the wash clipboard (so wash apps see it).
  await clip.writeText('from-embed-99');
  assert.equal(host.data, 'from-embed-99');
});

test('leaves a working (secure-context) clipboard untouched', () => {
  installHost();
  const real = {
    readText: () => Promise.resolve('real'),
    writeText: () => Promise.resolve(),
  };
  const win = { navigator: { clipboard: real } };

  const installed = installIngressClipboardBridge(win as unknown as Window);
  assert.equal(installed, false, 'must not shadow a real clipboard');
  assert.equal(win.navigator.clipboard, real, 'the native clipboard is preserved');
});

test('is a safe no-op for a null or cross-origin window', () => {
  installHost();
  assert.equal(installIngressClipboardBridge(null), false);
  assert.equal(installIngressClipboardBridge(undefined), false);

  // A cross-origin frame throws on property access; the bridge swallows it.
  const crossOrigin = {
    get navigator(): never {
      throw new Error('blocked a frame with origin ... from accessing a cross-origin frame');
    },
  };
  assert.equal(installIngressClipboardBridge(crossOrigin as unknown as Window), false);
});
