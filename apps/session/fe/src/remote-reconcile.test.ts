// Unit tests for the session FE's remote-attach reconciler (REVIEW-RECONNECT
// M4). The two-VM harness (make e2e-remote-vm) needs KVM and can't run here,
// so the FE logic is covered directly: the key property is that a host cycling
// up→reconnecting→up produces a fresh attach — the self-heal after an SSH blip
// with wash-connect closed.
//
// Run: node --test --conditions=browser apps/session/fe/src/remote-reconcile.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { reconcileRemoteAttachments, type ReconcileHost } from './remote-reconcile.ts';

function recorder() {
  const attached: string[] = [];
  const detached: string[] = [];
  return {
    attached,
    detached,
    actions: {
      attach: (o: string) => attached.push(o),
      detach: (o: string) => detached.push(o),
    },
  };
}

test('attaches an up host exactly once across repeated pushes', () => {
  const set = new Set<string>();
  const r = recorder();
  const up: ReconcileHost[] = [{ origin: 'alice@a', status: 'up' }];

  reconcileRemoteAttachments(up, set, r.actions);
  reconcileRemoteAttachments(up, set, r.actions); // repeated 'up' push
  assert.deepEqual(r.attached, ['alice@a'], 'attach once, not per push');
  assert.deepEqual(r.detached, []);
});

test('SSH blip: up→reconnecting→up re-attaches after the drop', () => {
  const set = new Set<string>();
  const r = recorder();

  // Up: attach.
  reconcileRemoteAttachments([{ origin: 'alice@a', status: 'up' }], set, r.actions);
  // Blip: status drops → detach.
  reconcileRemoteAttachments([{ origin: 'alice@a', status: 'reconnecting' }], set, r.actions);
  // Supervisor reconnects → up again → RE-attach (the M4 fix).
  reconcileRemoteAttachments([{ origin: 'alice@a', status: 'up' }], set, r.actions);

  assert.deepEqual(r.attached, ['alice@a', 'alice@a'], 're-attached after the blip');
  assert.deepEqual(r.detached, ['alice@a'], 'detached while down');
});

test('a host that vanishes from the list is detached', () => {
  const set = new Set<string>();
  const r = recorder();
  reconcileRemoteAttachments([{ origin: 'bob@b', status: 'up' }], set, r.actions);
  reconcileRemoteAttachments([], set, r.actions); // host gone entirely
  assert.deepEqual(r.detached, ['bob@b']);
  assert.equal(set.size, 0);
});

test('multiple hosts reconcile independently', () => {
  const set = new Set<string>();
  const r = recorder();
  reconcileRemoteAttachments(
    [
      { origin: 'a', status: 'up' },
      { origin: 'b', status: 'reconnecting' }, // not yet up → no attach
      { origin: 'c', status: 'up' },
    ],
    set,
    r.actions,
  );
  assert.deepEqual(r.attached.sort(), ['a', 'c']);
  assert.equal(set.has('b'), false);
});
