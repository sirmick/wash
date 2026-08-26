// The guard that keeps a Bulk-class transcript from being overtaken by
// the Interactive `started` that switches sessions.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isStaleTranscript } from './transcript-guard.ts';

test('a frame for the session on screen is kept', () => {
  assert.equal(isStaleTranscript('k1', 'k1'), false);
});

test('a frame for the session we just left is dropped', () => {
  assert.equal(isStaleTranscript('k1', 'k2'), true);
});

test('a keyless frame is trusted — it predates the key', () => {
  assert.equal(isStaleTranscript(undefined, 'k1'), false);
  assert.equal(isStaleTranscript('', 'k1'), false);
});

test('a frame arriving before the window knows its session is kept', () => {
  // Dropping here would lose the only transcript we have: the guard is
  // for rejecting the wrong session, not for rejecting an unknown one.
  assert.equal(isStaleTranscript('k1', ''), false);
});

test('a non-string key is not a key', () => {
  assert.equal(isStaleTranscript(7, 'k1'), false);
});
