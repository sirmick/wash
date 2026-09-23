// One vocabulary for what an agent session is doing (docs/AGENT_MESSENGER.md
// M5).
//
// Four surfaces render this — the sessions list, the desktop rail, the
// Agent window's own status line, and a terminal tab's dot — and until
// this module they each mapped state to colour and words independently.
// They disagreed, and two of the disagreements were bugs rather than
// inconsistencies: a session that FAILED rendered green everywhere,
// because no state was ever red; and the rail counted a stale (not
// responding) session as a working one, so the headline number was wrong
// in exactly the case you would want it right.
//
// The rule this file exists to hold: a state that cannot be said here is
// a state we should not be rendering. Adding a colour or a word to a
// component is how the drift started.

import { tokens } from './tokens.ts';

/**
 * The states agentd publishes, plus the two the FE derives.
 *
 * `running` is "detected, not in a turn" — a T0 agent, or one between
 * turns. `stale` is agentd's own verdict on a row it has stopped hearing
 * from. `failed` is a turn that ended on an adapter error, which used to
 * be reported as `done`.
 *
 * `detached` is not in this union: it is orthogonal (a live session with
 * no window), carried on the row beside its state, and rendered by
 * `detachedLabel` below.
 */
export type AgentState =
  | 'running'
  | 'working'
  | 'needs-input'
  | 'done'
  | 'failed'
  | 'stale';

/** Every state, in the order a list should sort them by urgency. */
export const AGENT_STATES: readonly AgentState[] = [
  'needs-input',
  'working',
  'running',
  'failed',
  'done',
  'stale',
];

/**
 * agentStateColor is the dot / border hue for a state.
 *
 * Amber is reserved for "a human is required" and nothing else — the one
 * colour that should make you look. Red means a session ended badly, and
 * is the colour this vocabulary added: `failed` was previously `done`,
 * so an adapter error and a clean finish were the same green.
 *
 * An unknown state returns the muted default rather than throwing: a
 * newer agentd may publish a state this build has never heard of, and a
 * grey dot is a better answer than a crash or a blank.
 */
export function agentStateColor(state: string): string {
  switch (state) {
    case 'needs-input':
      return tokens.accentAmber;
    case 'working':
      return tokens.accentBlue;
    case 'done':
      return tokens.accentGreen;
    case 'failed':
      return tokens.accentRed;
    case 'stale':
      return tokens.fgDim;
    default:
      return tokens.fgMuted;
  }
}

/**
 * agentStateLabel is what a human reads. `reason` qualifies the states
 * that have one: which kind of input is wanted, or how a session ended.
 *
 * Never renders the raw token: "needs-input" is not English, and every
 * surface was independently translating it.
 */
export function agentStateLabel(state: string, reason?: string): string {
  switch (state) {
    case 'needs-input':
      return reason ? `needs you · ${reason}` : 'needs you';
    case 'failed':
      return reason && reason !== 'error' ? `failed · ${reason}` : 'failed';
    case 'stale':
      return 'not responding';
    case 'working':
    case 'running':
    case 'done':
      return state;
    default:
      return state;
  }
}

/**
 * detachedLabel is what a live session with no window says about itself.
 * Orthogonal to state — a detached session is still running, working or
 * blocked — so it is a separate word rather than a seventh state.
 */
export const detachedLabel = 'running, no window';

/**
 * needsHuman reports whether a state is a claim on someone's attention.
 * This is the predicate the rail's badge counts and the taskbar pill
 * pulses for; keeping it here stops each surface inventing its own.
 */
export function needsHuman(state: string): boolean {
  return state === 'needs-input';
}

/**
 * isWorking reports whether a session is actively doing something.
 *
 * Written as an allow-list on purpose. The rail asked the opposite
 * question — `state !== 'needs-input'` — which counted `stale` and `done`
 * rows as working, so a dead agent and a finished one both inflated
 * "N agents working".
 */
export function isWorking(state: string): boolean {
  return state === 'working';
}

/**
 * isOver reports whether a session has ended, however it ended. Both
 * arms render dim; only the colour distinguishes them.
 */
export function isOver(state: string): boolean {
  return state === 'done' || state === 'failed';
}


/** Detailed workspace activity, separate from session lifecycle. */
export function agentActivityLabel(activity: string): string {
  switch (activity) {
    case 'thinking': return 'Thinking';
    case 'tool': return 'Using tools';
    case 'responding': return 'Responding';
    case 'waiting-message': return 'Awaiting message';
    case 'needs-input': return 'Needs you';
    case 'working': return 'Working';
    case 'starting': return 'Starting';
    case 'idle': return 'Idle';
    case 'paused': return 'Paused';
    case 'failed': return 'Failed';
    case 'ended': return 'Ended';
    case 'offline': return 'Not connected';
    default: return activity;
  }
}
export function agentActivityColor(activity: string): string {
  switch (activity) {
    case 'thinking': return tokens.accentViolet;
    case 'tool': return tokens.accentCyan;
    case 'responding': return tokens.accentGreen;
    case 'waiting-message': return tokens.fgInfo;
    case 'needs-input': return agentStateColor('needs-input');
    case 'failed': return agentStateColor('failed');
    case 'working': case 'starting': return agentStateColor('working');
    default: return tokens.fgMuted;
  }
}
export function agentActivityPulses(activity: string): boolean {
  return ['thinking', 'tool', 'responding', 'working', 'starting', 'needs-input'].includes(activity);
}
