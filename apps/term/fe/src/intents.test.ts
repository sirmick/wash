// The split-intent contract: an intent belongs to one request, and only
// that request's arrival may consume it (docs/TERM_LAYOUT.md §5).

import { test } from 'node:test';
import assert from 'node:assert/strict';

import { SplitIntents } from './intents.ts';

test('a tab_opened carrying the request id consumes exactly that intent', () => {
  const s = new SplitIntents();
  const req = s.mint();
  s.set(req, { path: '0', dir: 'row' });
  assert.deepEqual(s.take(req), { path: '0', dir: 'row' });
  assert.equal(s.take(req), undefined, 'consumed once');
  assert.equal(s.size, 0);
});

test('an arrival with no request id never takes a pending intent', () => {
  // reconcile() after a reattach and agentd's exec_tab both add tabs the
  // FE never asked for; neither may turn a pending split into theirs.
  const s = new SplitIntents();
  const req = s.mint();
  s.set(req, { path: '0', dir: 'col' });
  assert.equal(s.take(undefined), undefined);
  assert.equal(s.take(''), undefined);
  assert.equal(s.size, 1, 'the split is still waiting for its own tab');
  assert.deepEqual(s.take(req), { path: '0', dir: 'col' });
});

test('an unknown request id takes nothing', () => {
  const s = new SplitIntents();
  s.set(s.mint(), { path: '0', dir: 'row' });
  assert.equal(s.take('t999'), undefined);
  assert.equal(s.size, 1);
});

test('tab_error drops its intent so the next plain New Tab is not a split', () => {
  const s = new SplitIntents();
  const failed = s.mint();
  s.set(failed, { path: '0', dir: 'row' });
  s.drop(failed);
  assert.equal(s.size, 0);
  // The plain New Tab that follows has no intent of its own…
  const plain = s.mint();
  assert.equal(s.take(plain), undefined);
  // …and the failed one's cannot resurface either.
  assert.equal(s.take(failed), undefined);
});

test('two fast splits are keyed, not queued: each arrival finds its own', () => {
  const s = new SplitIntents();
  const a = s.mint();
  const b = s.mint();
  s.set(a, { path: '0', dir: 'row' });
  s.set(b, { path: '0.1', dir: 'col' });
  // Out-of-order replies (the second pty forked faster) still land right.
  assert.deepEqual(s.take(b), { path: '0.1', dir: 'col' });
  assert.deepEqual(s.take(a), { path: '0', dir: 'row' });
});

test('minted ids are unique within an instance', () => {
  const s = new SplitIntents();
  const ids = new Set<string>();
  for (let i = 0; i < 50; i++) ids.add(s.mint());
  assert.equal(ids.size, 50);
});
