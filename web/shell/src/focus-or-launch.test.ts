// docs/AGENT_UX.md N1 — a door navigates, it does not spawn.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { pickWindow, type AppWindow } from './focus-or-launch.ts';

const w = (o: Partial<AppWindow> & { windowID: number }): AppWindow => ({
  origin: 'local',
  appID: 'com.wash.ai',
  focused: false,
  ...o,
});

test('nothing open means launch', () => {
  assert.equal(pickWindow([], 'local', 'com.wash.ai'), null);
  assert.equal(pickWindow([w({ windowID: 1, appID: 'com.wash.fm' })], 'local', 'com.wash.ai'), null);
});

test('one window open is the one to raise', () => {
  const win = w({ windowID: 3 });
  assert.equal(pickWindow([win], 'local', 'com.wash.ai'), win);
});

test('a window on another host is not this door', () => {
  assert.equal(pickWindow([w({ windowID: 1, origin: 'build01' })], 'local', 'com.wash.ai'), null);
  const remote = w({ windowID: 1, origin: 'build01' });
  assert.equal(pickWindow([remote], 'build01', 'com.wash.ai'), remote);
});

test('with several open, the newest wins', () => {
  const wins = [w({ windowID: 2 }), w({ windowID: 7 }), w({ windowID: 4 })];
  assert.equal(pickWindow(wins, 'local', 'com.wash.ai')?.windowID, 7);
});

test('clicking again while the raised one is focused cycles, and wraps', () => {
  const wins = [w({ windowID: 2 }), w({ windowID: 4 }), w({ windowID: 7, focused: true })];
  // 7 is focused and last → wrap to the oldest.
  assert.equal(pickWindow(wins, 'local', 'com.wash.ai')?.windowID, 2);
  const mid = [w({ windowID: 2 }), w({ windowID: 4, focused: true }), w({ windowID: 7 })];
  assert.equal(pickWindow(mid, 'local', 'com.wash.ai')?.windowID, 7);
});

test('a focused window of ANOTHER app does not start the cycle', () => {
  const wins = [
    w({ windowID: 9, appID: 'com.wash.fm', focused: true }),
    w({ windowID: 2 }),
    w({ windowID: 4 }),
  ];
  assert.equal(pickWindow(wins, 'local', 'com.wash.ai')?.windowID, 4);
});

test('a single focused window re-raises itself rather than nothing', () => {
  const win = w({ windowID: 3, focused: true });
  assert.equal(pickWindow([win], 'local', 'com.wash.ai'), win);
});
