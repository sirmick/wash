// The rail counts agent states by string literal, because awareness.ts is
// dependency-free so it can run under plain `node --test` (@wash/ui's
// index uses bundler-style extensionless imports that node cannot
// resolve). That is a deliberate exception to "one vocabulary, one place"
// — docs/AGENT_MESSENGER.md M5 — and this file is what makes it safe.
//
// A Tier B test rather than a node:test one, precisely because vitest CAN
// resolve @wash/ui: it imports both sides and asserts they still agree.
// If the shared vocabulary renames or drops a state, the rail's inlined
// copy fails the build here rather than quietly counting nothing.

import { test, expect } from 'vitest';
import { AGENT_STATES, agentStateLabel, isWorking, needsHuman } from '@wash/ui';
import { agentSummary, waitingAgents } from './awareness.ts';

// The three literals awareness.ts inlines, restated as the questions it
// asks. If these ever disagree with the shared predicates, the rail is
// counting a different set of rows than the list is showing.
const rowsFor = (state: string) => ({ rows: [{ state }], asks: [] });

test('the rail agrees with the shared vocabulary about who is working', () => {
  for (const state of AGENT_STATES) {
    const counted = agentSummary(rowsFor(state)).includes('working');
    expect(counted, `${state}: rail says working=${counted}, vocabulary says ${isWorking(state)}`)
      .toBe(isWorking(state));
  }
});

test('the rail agrees about who is waiting on a human', () => {
  for (const state of AGENT_STATES) {
    expect(waitingAgents(rowsFor(state)) === 1, `${state}`).toBe(needsHuman(state));
  }
});

test('every state the vocabulary knows is one the rail can count', () => {
  // Not an assertion about wording — the rail summarises rather than
  // labelling — but a state the vocabulary defines and the rail silently
  // ignores is how "0 agents working" appeared next to a busy machine.
  for (const state of AGENT_STATES) {
    const summary = agentSummary(rowsFor(state));
    const accountedFor = isWorking(state) || needsHuman(state) || state === 'failed';
    expect(summary !== '', `${state} → ${JSON.stringify(summary)}`).toBe(accountedFor);
  }
  // And the labels exist for every one of them, which is what the list
  // renders beside the rail's count.
  for (const state of AGENT_STATES) {
    expect(agentStateLabel(state).length).toBeGreaterThan(0);
  }
});
