// Unit tests for the desktop keyboard guard (keyguard.ts).
// Run: node --test --conditions=browser web/shell/src/keyguard.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { shouldSwallowDesktopKey } from './keyguard.ts';

const k = (key: string, o: Partial<{ ctrlKey: boolean; altKey: boolean; metaKey: boolean }> = {}) => ({
  key,
  ctrlKey: false,
  altKey: false,
  metaKey: false,
  ...o,
});

test('swallows Ctrl+W / Ctrl+N / Ctrl+T only while no window has focus', () => {
  for (const key of ['w', 'W', 'n', 't']) {
    assert.ok(shouldSwallowDesktopKey(k(key, { ctrlKey: true }), false), key);
    assert.ok(!shouldSwallowDesktopKey(k(key, { ctrlKey: true }), true), `${key} with a focused window belongs to the app`);
  }
});

test('leaves every other chord alone', () => {
  assert.ok(!shouldSwallowDesktopKey(k('w'), false), 'plain w');
  assert.ok(!shouldSwallowDesktopKey(k('w', { ctrlKey: true, altKey: true }), false), 'Ctrl+Alt+W is not a browser chord');
  assert.ok(!shouldSwallowDesktopKey(k('w', { metaKey: true }), false), 'Meta+W is the OS');
  assert.ok(!shouldSwallowDesktopKey(k('F5'), false), 'F5 is deliberately left alone');
  assert.ok(!shouldSwallowDesktopKey(k('r', { ctrlKey: true }), false), 'Ctrl+R (reload) is deliberately left alone');
  assert.ok(!shouldSwallowDesktopKey(k('Tab', { ctrlKey: true }), false));
});
