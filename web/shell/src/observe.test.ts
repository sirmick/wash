// Tests for the shell's observe fallbacks (docs/COMMANDER.md §4.1): auto
// means auto — a router `none` for an eligible app falls through to the
// app's provider, then the window's text, bounded and redacted.
// Run with: cd web/shell && npx tsx --test src/observe.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { fallback, normaliseText, redact, type Observation, type Holdings } from './observe.ts';

const none = (eligible: boolean): Observation => ({ source: 'none', eligible, captured_at: 1, host: 'local', window: { app: 'com.wash.about', instance_id: 'i-1' } });

const holdings = (provider: Holdings['provider'], text: () => string | undefined): Holdings => ({ provider, text });

test('a router answer that is not none is returned as is', () => {
  const o: Observation = { source: 'pty-tail', eligible: true, content: '$ ls', captured_at: 5, host: 'local' };
  assert.equal(fallback(o, holdings(() => ({ source: 'app', content: { x: 1 } }), () => 'text')), o);
});

test('an ineligible app is never read from the shell either', () => {
  const o = fallback(none(false), holdings(() => ({ source: 'app', content: { x: 1 } }), () => 'secret screen'));
  assert.equal(o.source, 'none');
  assert.equal(o.content, undefined);
});

test('the provider comes before the DOM, as JSON, redacted', () => {
  const o = fallback(none(true), holdings(() => ({ source: 'app', content: { path: '/tmp/a', password: 'hunter2' } }), () => 'rendered'));
  assert.equal(o.source, 'provider');
  assert.equal(o.content_type, 'application/json');
  assert.equal(o.content, '{"path":"/tmp/a","password":"[redacted]"}');
  assert.match(o.revision ?? '', /^fe:/);
  // The shell's mirror of the saved state is named as the router names it.
  const s = fallback(none(true), holdings(() => ({ source: 'backing-store', content: { tabs: [] } }), () => 'rendered'));
  assert.equal(s.source, 'app-state');
});

test('the window text is the last resort, normalised, with a revision that moves', () => {
  const h = holdings(() => ({ source: 'none' }), () => '\n\n  About   wash \n\n\n\nVersion 0.16.0\t\n  token: abc123 \n');
  const o = fallback(none(true), h);
  assert.equal(o.source, 'dom');
  assert.equal(o.content, 'About wash\n\nVersion 0.16.0\ntoken: [redacted]');
  assert.equal(o.truncated, false);
  const again = fallback(none(true), holdings(() => ({ source: 'none' }), () => 'About wash\nVersion 0.17.0'));
  assert.notEqual(o.revision, again.revision);
  // Nothing rendered either: the observation stays none, still eligible.
  const empty = fallback(none(true), holdings(() => ({ source: 'none' }), () => '   \n '));
  assert.equal(empty.source, 'none');
  assert.equal(empty.eligible, true);
});

test('content is bounded on a character boundary and marked', () => {
  const o = fallback(none(true), holdings(() => ({ source: 'none' }), () => 'é'.repeat(20)), 11);
  assert.equal(o.truncated, true);
  assert.equal(o.content, 'é'.repeat(5));
});

test('a provider that throws falls through to the text', () => {
  const o = fallback(none(true), holdings(() => { throw new Error('boom'); }, () => 'still here'));
  assert.equal(o.source, 'dom');
  assert.equal(o.content, 'still here');
});

test('normaliseText and redact on their own', () => {
  assert.equal(normaliseText('a\r\n\r\n\r\nb'), 'a\n\nb');
  assert.equal(redact('export OPENAI_API_KEY=sk-proj-abcdefgh12345678'), 'export OPENAI_API_KEY=[redacted]');
  assert.equal(redact("curl -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiJ9'"), "curl -H 'Authorization: Bearer [redacted]'");
  assert.equal(redact('aws AKIAIOSFODNN7EXAMPLE used'), 'aws [redacted] used');
});
