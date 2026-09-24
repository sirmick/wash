// The transcript delta fold (apps/agentd/be/transcript_emit.go's other half).

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { applyAgentEvent, mergeAgentEvents, utf8Len } from './agent-events.ts';
import type { AgentEvent } from './agent-session.tsx';

const msg = (seq: number, text: string, extra: Partial<AgentEvent> = {}): AgentEvent => ({
  seq,
  kind: 'message',
  text,
  text_len: utf8Len(text),
  at_ms: 0,
  ...extra,
});
const delta = (seq: number, text: string, textLen: number): AgentEvent => ({
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
