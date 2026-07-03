// Unit tests for installIngressFocusBridge — the fix that forwards a
// click inside the embedded (code-server) iframe up to the wash window
// manager so the window raises to the foreground (#12). Framework-free:
// we hand in a fake embedded window with a minimal EventTarget-style
// document and assert the bridge observes interactions and detaches
// cleanly. Run via `make fe-unit`.

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { installIngressFocusBridge } from './ingress-focus.ts';

// A minimal stand-in for the embedded window's document: records
// add/removeEventListener calls and can fire a registered listener.
function fakeEmbed() {
  const handlers: Record<string, Set<() => void>> = {};
  const doc = {
    addEventListener(type: string, fn: () => void) {
      (handlers[type] ??= new Set()).add(fn);
    },
    removeEventListener(type: string, fn: () => void) {
      handlers[type]?.delete(fn);
    },
    fire(type: string) {
      for (const fn of handlers[type] ?? []) fn();
    },
    count(type: string) {
      return handlers[type]?.size ?? 0;
    },
  };
  return { win: { document: doc } as unknown as Window, doc };
}

test('raises the window when the user interacts inside the embed', () => {
  const { win, doc } = fakeEmbed();
  let raises = 0;
  installIngressFocusBridge(win, () => raises++);

  // A pointerdown (mouse/touch) inside the iframe forwards a raise.
  doc.fire('pointerdown');
  assert.equal(raises, 1, 'pointerdown inside the embed should raise the window');

  // Keyboard focus landing in the frame (tab-in) also raises.
  doc.fire('focusin');
  assert.equal(raises, 2, 'focusin inside the embed should raise the window');
});

test('detach removes the listeners so stale frames stop raising', () => {
  const { win, doc } = fakeEmbed();
  let raises = 0;
  const detach = installIngressFocusBridge(win, () => raises++);
  assert.ok(doc.count('pointerdown') > 0, 'listener installed');

  detach();
  assert.equal(doc.count('pointerdown'), 0, 'pointerdown listener removed');
  assert.equal(doc.count('focusin'), 0, 'focusin listener removed');

  doc.fire('pointerdown');
  assert.equal(raises, 0, 'a detached bridge must not forward');
});

test('is a safe no-op for a null or cross-origin window', () => {
  // Null/undefined windows: no throw, cleanup is callable.
  assert.doesNotThrow(() => installIngressFocusBridge(null, () => {})());
  assert.doesNotThrow(() => installIngressFocusBridge(undefined, () => {})());

  // A cross-origin frame throws on document access; the bridge swallows
  // it and returns a no-op cleanup.
  const crossOrigin = {
    get document(): never {
      throw new Error('blocked a frame with origin ... from accessing a cross-origin frame');
    },
  };
  assert.doesNotThrow(() =>
    installIngressFocusBridge(crossOrigin as unknown as Window, () => {})(),
  );
});
