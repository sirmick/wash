// What an Agent window re-asks for after a connection blip (GH #21).

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { reconnectRequests } from './reconnect.ts';

test('the manager resubscribes, so a roster that went quiet while away comes back', () => {
  assert.deepEqual(reconnectRequests('manager', ''), [{ kind: 'manager_subscribe' }]);
});

test('a controller re-claims its session and asks for the whole transcript', () => {
  // The claim is what force-sends the row and its asks; without replay a
  // watcher agentd already knows gets nothing back.
  assert.deepEqual(reconnectRequests('session', 'acp:2'), [
    { kind: 'session_claim', key: 'acp:2' },
    { kind: 'transcript_subscribe', key: 'acp:2', replay: true },
  ]);
});

test('a controller still on its launcher has nothing to re-claim', () => {
  assert.deepEqual(reconnectRequests('session', ''), []);
});
