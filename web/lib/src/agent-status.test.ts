// docs/AGENT_MESSENGER.md M5 — one vocabulary, and the three defects the
// survey found hiding in the drift between four copies of it.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  AGENT_STATES,
  agentStateColor,
  agentStateLabel,
  isOver,
  isWorking,
  needsHuman,
} from './agent-status.ts';
import { tokens } from './tokens.ts';

// --- defect 1: a failed session used to render green ---

test('a failed session is red, and a finished one is green', () => {
  assert.equal(agentStateColor('failed'), tokens.accentRed);
  assert.equal(agentStateColor('done'), tokens.accentGreen);
  assert.notEqual(agentStateColor('failed'), agentStateColor('done'));
});

test('failed says so, and says why when the reason adds anything', () => {
  assert.equal(agentStateLabel('failed'), 'failed');
  // "failed · error" is a tautology; the bare word is the honest form.
  assert.equal(agentStateLabel('failed', 'error'), 'failed');
  assert.equal(agentStateLabel('failed', 'auth'), 'failed · auth');
});

// --- defect 2: the rail counted stale and done rows as working ---

test('only working is working', () => {
  assert.equal(isWorking('working'), true);
  for (const s of ['stale', 'done', 'failed', 'running', 'needs-input']) {
    assert.equal(isWorking(s), false, `${s} must not count as working`);
  }
});

test('needing a human is exactly one state', () => {
  assert.equal(needsHuman('needs-input'), true);
  for (const s of ['working', 'running', 'done', 'failed', 'stale']) {
    assert.equal(needsHuman(s), false, `${s} must not claim attention`);
  }
});

test('over means over, however it ended', () => {
  assert.equal(isOver('done'), true);
  assert.equal(isOver('failed'), true);
  assert.equal(isOver('working'), false);
  // A stale session has NOT ended — nobody knows what it is doing, which
  // is a different and worse thing than being finished.
  assert.equal(isOver('stale'), false);
});

// --- defect 3: stale was inexpressible in one of the renderers ---

test('stale is a state with its own colour and its own words', () => {
  assert.equal(agentStateLabel('stale'), 'not responding');
  assert.notEqual(agentStateColor('stale'), agentStateColor('running'));
  assert.notEqual(agentStateColor('stale'), agentStateColor('done'));
});

// --- the vocabulary itself ---

test('amber means one thing: a human is required', () => {
  const amber = AGENT_STATES.filter((s) => agentStateColor(s) === tokens.accentAmber);
  assert.deepEqual(amber, ['needs-input']);
});

test('every state has a colour and a human-readable label', () => {
  for (const s of AGENT_STATES) {
    assert.ok(agentStateColor(s), `${s} has no colour`);
    const label = agentStateLabel(s);
    assert.ok(label.length > 0, `${s} has no label`);
    // The raw wire token is not English and must never reach a person.
    assert.ok(!label.includes('needs-input'), `${s} leaked the raw token`);
  }
});

test('needs-input names which kind of input, when it knows', () => {
  assert.equal(agentStateLabel('needs-input'), 'needs you');
  assert.equal(agentStateLabel('needs-input', 'permission'), 'needs you · permission');
});

test('an unknown state degrades to muted rather than blank or throwing', () => {
  // A newer agentd may publish a state this build has never heard of.
  assert.equal(agentStateColor('teleporting'), tokens.fgMuted);
  assert.equal(agentStateLabel('teleporting'), 'teleporting');
});
