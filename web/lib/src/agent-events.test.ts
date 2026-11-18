// The transcript delta fold (apps/agentd/be/transcript_emit.go's other half).

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { applyAgentEvent, mergeAgentEvents, TranscriptHeld, utf8Len } from './agent-events.ts';
import type * as agentproto from './agent-protocol.gen';

const msg = (seq: number, text: string, extra: Partial<agentproto.Event> = {}): agentproto.Event => ({
  seq,
  kind: 'message',
  text,
  text_len: utf8Len(text),
  at_ms: 0,
  ...extra,
});
const delta = (seq: number, text: string, textLen: number): agentproto.Event => ({
  seq,
  kind: 'message',
  text,
  append: true,
  text_len: textLen,
  at_ms: 0,
});

test('a delta appends to its row when the lengths line up', () => {
  let r = applyAgentEvent([], msg(1, 'Hello'));
  r = applyAgentEvent(r.events, delta(1, ', wörld', utf8Len('Hello, wörld')));
  assert.equal(r.gap, false);
  assert.equal(r.events[0].text, 'Hello, wörld');
  assert.equal(r.events[0].text_len, utf8Len('Hello, wörld'));
});

test('a delta with no base row, or one that skips text, is a gap', () => {
  assert.equal(applyAgentEvent([], delta(1, 'x', 1)).gap, true);
  const r = applyAgentEvent([msg(1, 'Hello')], delta(1, 'world', 12));
  assert.equal(r.gap, true);
  assert.equal(r.events[0].text, 'Hello', 'a gapped delta is not applied');
});

test('a delta we already hold (replay crossed it) is dropped, not a gap', () => {
  const r = applyAgentEvent([msg(1, 'Hello, world')], delta(1, ', world', 12));
  assert.equal(r.gap, false);
  assert.equal(r.events[0].text, 'Hello, world');
});

test('whole rows replace by seq and insert in order', () => {
  let r = applyAgentEvent([msg(1, 'a'), msg(3, 'c')], msg(2, 'b'));
  assert.deepEqual(r.events.map((e) => e.seq), [1, 2, 3]);
  r = applyAgentEvent(r.events, { seq: 2, kind: 'tool', status: 'completed', at_ms: 0 });
  assert.equal(r.events[1].kind, 'tool');
  assert.equal(r.events.length, 3);
});

test('a snapshot batch merges by seq', () => {
  const out = mergeAgentEvents([msg(1, 'old'), msg(2, 'b')], [msg(1, 'new'), msg(3, 'c')]);
  assert.deepEqual(out.map((e) => `${e.seq}:${e.text}`), ['1:new', '2:b', '3:c']);
});

test('does not mutate the previous list', () => {
  const prev = [msg(1, 'Hello')];
  applyAgentEvent(prev, delta(1, '!', 6));
  assert.equal(prev[0].text, 'Hello');
});

test('a replay that repeats rows keeps the objects already held', () => {
  const prev = [msg(1, 'a'), { seq: 2, kind: 'tool', title: 'ls', status: 'running', at_ms: 0 }];
  // A fresh decode: equal rows, new objects.
  const same = JSON.parse(JSON.stringify(prev)) as agentproto.Event[];
  assert.equal(mergeAgentEvents(prev, same), prev, 'nothing changed: the very same list');

  const moved = JSON.parse(JSON.stringify(prev)) as agentproto.Event[];
  moved[1].status = 'completed';
  const out = mergeAgentEvents(prev, [...moved, msg(3, 'c')]);
  assert.equal(out[0], prev[0], 'an unchanged row keeps its object');
  assert.notEqual(out[1], prev[1], 'a changed row is replaced');
  assert.equal(out[1].status, 'completed');
  assert.deepEqual(out.map((e) => e.seq), [1, 2, 3]);
});

test('an identical whole row leaves the list untouched', () => {
  const prev = [msg(1, 'a')];
  const r = applyAgentEvent(prev, msg(1, 'a'));
  assert.equal(r.events, prev);
  assert.equal(r.gap, false);
});

test('TranscriptHeld: asks for a whole replay until a snapshot names the epoch', () => {
  const h = new TranscriptHeld();
  h.see(msg(1, 'a', { rev: 3 }));
  assert.deepEqual(h.since(), {}, 'live events alone do not say which epoch');
  h.snapshot({ epoch: 'e1', events: [msg(1, 'a', { rev: 3 }), msg(2, 'b', { rev: 5 })] });
  assert.deepEqual(h.since(), { epoch: 'e1', since: 5 });
  h.see(msg(3, 'c', { rev: 9 }));
  assert.deepEqual(h.since(), { epoch: 'e1', since: 9 });
  // agentd restarted: its revs start over, ours mean nothing against them.
  h.snapshot({ epoch: 'e2', events: [msg(1, 'a', { rev: 1 })] });
  assert.deepEqual(h.since(), { epoch: 'e2', since: 1 });
  h.clear();
  assert.deepEqual(h.since(), {});
});
