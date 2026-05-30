// Unit tests for the replace-on-conflict retry flow.
// Run with: node --test web/fs-client/src/replace-flow.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { withReplacePrompt, type ReplaceFlowDeps } from './replace-flow.ts';
import type { BEMessage } from './bus.ts';

// Records requests + confirm calls and replies from a scripted queue.
function harness(replies: BEMessage[], confirmAnswer?: boolean) {
  const requests: Record<string, unknown>[] = [];
  const confirms: string[] = [];
  let i = 0;
  const deps: ReplaceFlowDeps = {
    request: async (req) => {
      requests.push(req);
      return replies[i++];
    },
    confirm: async (destPath) => {
      confirms.push(destPath);
      return confirmAnswer!;
    },
  };
  return { deps, requests, confirms };
}

test('a clean success returns immediately, no confirm, no retry', async () => {
  const { deps, requests, confirms } = harness([{ kind: 'rename_ok' }]);
  const reply = await withReplacePrompt(deps, { kind: 'rename', to: '/b' }, '/b', 'rename_err');
  assert.equal(reply.kind, 'rename_ok');
  assert.equal(requests.length, 1);
  assert.equal(confirms.length, 0);
});

test('an unrelated error (kind matches, code not exists) returns as-is, no prompt', async () => {
  const { deps, confirms } = harness([{ kind: 'rename_err', code: 'perm' }]);
  const reply = await withReplacePrompt(deps, { kind: 'rename' }, '/b', 'rename_err');
  assert.equal(reply.kind, 'rename_err');
  assert.equal(reply.code, 'perm');
  assert.equal(confirms.length, 0);
});

test('a different errKind with code exists does NOT prompt (kind must match)', async () => {
  const { deps, confirms } = harness([{ kind: 'other_err', code: 'exists' }]);
  const reply = await withReplacePrompt(deps, { kind: 'rename' }, '/b', 'rename_err');
  assert.equal(reply.kind, 'other_err');
  assert.equal(confirms.length, 0);
});

test('exists + confirm=true retries with replace:true and returns the retry reply', async () => {
  const { deps, requests, confirms } = harness(
    [{ kind: 'rename_err', code: 'exists' }, { kind: 'rename_ok' }],
    true,
  );
  const reply = await withReplacePrompt(deps, { kind: 'rename', to: '/b' }, '/b', 'rename_err');
  assert.equal(reply.kind, 'rename_ok');
  assert.equal(confirms.length, 1);
  assert.deepEqual(confirms[0], '/b'); // prompted for the dest path
  assert.equal(requests.length, 2);
  // first request had no replace flag; retry carries the original fields + replace:true
  assert.deepEqual(requests[0], { kind: 'rename', to: '/b' });
  assert.deepEqual(requests[1], { kind: 'rename', to: '/b', replace: true });
});

test('exists + confirm=false returns synthetic cancelled and does NOT retry', async () => {
  const { deps, requests, confirms } = harness([{ kind: 'rename_err', code: 'exists' }], false);
  const reply = await withReplacePrompt(deps, { kind: 'rename' }, '/b', 'rename_err');
  assert.equal(reply.kind, 'cancelled');
  assert.equal(confirms.length, 1);
  assert.equal(requests.length, 1); // no second request
});
