// Component test (Tier B) for the shared coding-agent roster — the pure
// renderer of com.wash.agentd's snapshot. Covers empty/populated
// rendering, the state language (colour + label), the "dir · branch*"
// place line, and the click-to-focus callback. The cross-process flow (a
// real agent in a real terminal reaching the sidebar) is
// term-agent-roster.spec.ts; that's the integration seam.

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup, screen } from '@solidjs/testing-library';
import { AgentRoster, fmtAgo, fmtElapsed, stateColor, stateLabel, type RosterAsk, type RosterRow } from './agent-roster.tsx';

afterEach(cleanup);

const row = (over: Partial<RosterRow> = {}): RosterRow => ({
  key: 'i-1:5',
  agent: 'claude',
  state: 'working',
  term_instance: 'i-1',
  window_id: 1,
  channel_id: 5,
  since_ms: 0,
  ...over,
});

const noop = () => {};
const at = (_key: string) => 0;

test('empty: says so rather than rendering an empty box', () => {
  const { queryByTestId } = render(() => (
    <AgentRoster rows={() => []} startedAt={at} now={() => 0} onActivate={noop} />
  ));
  expect(queryByTestId('agents-empty')).toBeTruthy();
});

test('populated: one row per agent, state on the element for the chrome to read', () => {
  const rows = [row({ key: 'a', state: 'needs-input', reason: 'permission' }), row({ key: 'b', agent: 'codex' })];
  const { queryByTestId, getByTestId } = render(() => (
    <AgentRoster rows={() => rows} startedAt={at} now={() => 0} onActivate={noop} />
  ));
  expect(queryByTestId('agents-empty')).toBeNull();
  expect(getByTestId('agents-row-a').getAttribute('data-agent-state')).toBe('needs-input');
  expect(getByTestId('agents-row-a').getAttribute('data-agent')).toBe('claude');
  expect(getByTestId('agents-row-b').getAttribute('data-agent')).toBe('codex');
  // The reason is what makes "needs input" actionable.
  expect(getByTestId('agents-row-a').textContent).toContain('needs input · permission');
});

test('place line: repo, branch, and a star when the tree is dirty', () => {
  const rows = [
    row({ key: 'clean', dir: 'wash', branch: 'main' }),
    row({ key: 'dirty', dir: 'wash', branch: 'main', dirty: true }),
    row({ key: 'nogit', dir: 'tmp' }),
  ];
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => rows} startedAt={at} now={() => 0} onActivate={noop} />
  ));
  expect(getByTestId('agents-row-clean').textContent).toContain('wash · main');
  expect(getByTestId('agents-row-dirty').textContent).toContain('wash · main*');
  // No branch (not a checkout) still shows where it is.
  expect(getByTestId('agents-row-nogit').textContent).toContain('tmp');
  expect(getByTestId('agents-row-nogit').textContent).not.toContain('·');
});

test('elapsed counts from the row anchor, not the push', () => {
  const rows = [row({ key: 'a' })];
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => rows} startedAt={() => 1_000} now={() => 91_000} onActivate={noop} />
  ));
  expect(getByTestId('agents-row-a').textContent).toContain('1m');
});

test('clicking a row asks to focus that agent’s terminal', () => {
  const rows = [row({ key: 'a' }), row({ key: 'b', term_instance: 'i-2' })];
  const seen: string[] = [];
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => rows} startedAt={at} now={() => 0} onActivate={(r) => seen.push(r.term_instance)} />
  ));
  fireEvent.click(getByTestId('agents-row-b'));
  expect(seen).toEqual(['i-2']);
});

test('state language matches the terminal’s own tab dot', () => {
  // Distinct colours for the three states a user acts on, and stale is
  // visibly not one of them.
  const colors = ['working', 'needs-input', 'done'].map(stateColor);
  expect(new Set(colors).size).toBe(3);
  expect(stateColor('stale')).not.toBe(stateColor('working'));
  // An unknown state still renders (a newer terminal can invent one).
  expect(stateColor('teleporting')).toBeTruthy();

  expect(stateLabel(row({ state: 'needs-input' }))).toBe('needs input');
  expect(stateLabel(row({ state: 'needs-input', reason: 'idle' }))).toBe('needs input · idle');
  expect(stateLabel(row({ state: 'stale' }))).toBe('not responding');
  expect(stateLabel(row({ state: 'working' }))).toBe('working');
});

test('fmtElapsed reads like a status line', () => {
  expect(fmtElapsed(0)).toBe('0s');
  expect(fmtElapsed(43_000)).toBe('43s');
  expect(fmtElapsed(90_000)).toBe('1m');
  expect(fmtElapsed(3 * 3_600_000)).toBe('3h');
  expect(fmtElapsed(-5_000)).toBe('0s');
});

// ---- M6: the question rows (docs/AGENT_TERM.md §12) ----

const ask = (over: Partial<RosterAsk> = {}): RosterAsk => ({
  id: 'ask-1',
  agent: 'claude',
  tool: 'Bash',
  subject: 'git push origin main',
  dir: 'wash',
  suggested_rule: 'Bash(git push*)',
  row_key: 'i-1:5',
  term_instance: 'i-1',
  age_ms: 0,
  ...over,
});

test('a pending question renders what the agent wants and three ways out', () => {
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => []} startedAt={at} now={() => 0} onActivate={noop} asks={() => [ask()]} />
  ));
  const row = getByTestId('agents-ask');
  expect(row.getAttribute('data-tool')).toBe('Bash');
  expect(row.getAttribute('data-ask-id')).toBe('ask-1');
  expect(getByTestId('agents-ask-what').textContent).toContain('git push origin main');
  // The rule "always" would write is named ON the button — what you
  // clicked is what gets saved.
  expect(getByTestId('agents-ask-always').textContent).toContain('Bash(git push*)');
  expect(getByTestId('agents-ask-allow')).toBeTruthy();
  expect(getByTestId('agents-ask-deny')).toBeTruthy();
});

test('each button answers with its own decision + remember flag', () => {
  const seen: string[] = [];
  const onAnswer = (_a: RosterAsk, d: string, r: boolean) => seen.push(`${d}:${r}`);
  const { getByTestId, unmount } = render(() => (
    <AgentRoster rows={() => []} startedAt={at} now={() => 0} onActivate={noop} asks={() => [ask()]} onAnswer={onAnswer} />
  ));
  fireEvent.click(getByTestId('agents-ask-allow'));
  fireEvent.click(getByTestId('agents-ask-always'));
  fireEvent.click(getByTestId('agents-ask-deny'));
  expect(seen).toEqual(['allow:false', 'allow:true', 'deny:false']);
  unmount();
});

test('a question with no suggestion still offers allow and deny', () => {
  const { getByTestId, queryByTestId } = render(() => (
    <AgentRoster rows={() => []} startedAt={at} now={() => 0} onActivate={noop}
      asks={() => [ask({ suggested_rule: undefined })]} />
  ));
  expect(queryByTestId('agents-ask-always')).toBeNull();
  expect(getByTestId('agents-ask-allow')).toBeTruthy();
});

test('questions render above the status rows — blocked beats informational', () => {
  const { container } = render(() => (
    <AgentRoster rows={() => [row({ key: 'r1' })]} startedAt={at} now={() => 0} onActivate={noop} asks={() => [ask()]} />
  ));
  const ids = [...container.querySelectorAll('[data-testid]')].map((el) => el.getAttribute('data-testid'));
  expect(ids.indexOf('agents-ask')).toBeLessThan(ids.indexOf('agents-row-r1'));
});

// ---- M7: the Recent list (docs/AGENT_TERM.md §13) ----

test('fmtAgo reads like a human said it', () => {
  const now = 1_000_000_000_000; // ms
  const sec = now / 1000;
  expect(fmtAgo(now, sec - 5)).toBe('just now');
  expect(fmtAgo(now, sec - 300)).toBe('5m ago');
  expect(fmtAgo(now, sec - 7200)).toBe('2h ago');
  expect(fmtAgo(now, sec - 2 * 86_400)).toBe('2d ago');
  expect(fmtAgo(now, 0)).toBe('');
});

// Earlier sessions live in the Agent app's History menu: the roster
// answers "what is running", and a list of things that are NOT running was
// answering a different question in the same space. The props that fed
// them are gone too (M2b) — this pins the scope, so a future "just add
// recent to the roster" has to argue with a test first.
test('the roster does not list sessions that ended', () => {
  const { queryByTestId } = render(() => (
    <AgentRoster rows={() => []} startedAt={at} now={() => Date.now()} onActivate={noop} />
  ));
  expect(queryByTestId('agents-recent')).toBeNull();
  expect(queryByTestId('agents-recent-row')).toBeNull();
});

// ── per-session verbs (GH #21) ──────────────────────────────────────────
//
// agentd has handled agent_detach / agent_cancel / agent_stop since the
// ACP tier landed; the rail never offered them, so a user who wanted to
// terminate a session found nothing to click and no request in the log.
// The verbs live in a per-row menu: the set grows, a sidebar row is
// narrow, and a menu can render an inapplicable verb DISABLED rather
// than vanishing.

// Menu portals into document.body so an overflow:auto ancestor can't
// clip it, which puts it OUTSIDE render()'s container — menu queries are
// document-scoped (screen), row queries are not.
const openRowMenu = (getByTestId: (id: string) => HTMLElement) => {
  fireEvent.click(getByTestId('agents-verbs-btn'));
  return screen.getByTestId('agents-row-actions');
};

test('verbs: the row offers a menu, and right-click opens the same one', () => {
  const { getByTestId, queryByTestId } = render(() => (
    <AgentRoster rows={() => [row({ key: 'a', state: 'working' })]} startedAt={at} now={() => 0}
      onActivate={noop} onDetach={noop} onCancel={noop} onStop={noop} />
  ));
  expect(screen.queryByTestId('agents-row-actions')).toBeNull();
  fireEvent.contextMenu(getByTestId('agents-row-a'));
  expect(screen.queryByTestId('agents-row-actions')).toBeTruthy();
});

test('verbs: an inapplicable verb is disabled, not missing', () => {
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => [row({ key: 'a', state: 'done' })]} startedAt={at} now={() => 0}
      onActivate={noop} onDetach={noop} onCancel={noop} onStop={noop} />
  ));
  openRowMenu(getByTestId);
  // A finished turn has nothing to stop — but the verb still shows, so
  // you learn it exists and why it does not apply.
  expect(screen.getByTestId('agents-menu-cancel')).toBeTruthy();
  expect(screen.getByTestId('agents-menu-cancel').hasAttribute('disabled')).toBe(true);
  expect(screen.getByTestId('agents-menu-detach').hasAttribute('disabled')).toBe(false);
});

test('verbs: a working agent can have its turn stopped', () => {
  let cancelled = 0;
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => [row({ key: 'a', state: 'working' })]} startedAt={at} now={() => 0}
      onActivate={noop} onCancel={() => { cancelled++; }} />
  ));
  openRowMenu(getByTestId);
  expect(screen.getByTestId('agents-menu-cancel').hasAttribute('disabled')).toBe(false);
  fireEvent.click(screen.getByTestId('agents-menu-cancel'));
  expect(cancelled).toBe(1);
});

test('verbs: an already-detached row cannot detach again, and offers to attach', () => {
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => [row({ key: 'a', state: 'done', detached: true })]} startedAt={at}
      now={() => 0} onActivate={noop} onDetach={noop} onStop={noop} />
  ));
  openRowMenu(getByTestId);
  expect(screen.getByTestId('agents-menu-detach').hasAttribute('disabled')).toBe(true);
  expect(screen.getByTestId('agents-menu-attach').textContent).toContain('Attach');
});

test('verbs: a host that passes no handler gets no menu at all', () => {
  const { queryByTestId } = render(() => (
    <AgentRoster rows={() => [row({ key: 'a', state: 'working' })]} startedAt={at} now={() => 0}
      onActivate={noop} />
  ));
  expect(queryByTestId('agents-verbs-btn')).toBeNull();
});

// End kills the adapter and everything it held, and it sits one row from
// Detach in a small list. Picking it must ask.
test('verbs: End asks before it ends, and Cancel backs out', () => {
  let stopped = 0;
  const { getByTestId, queryByTestId } = render(() => (
    <AgentRoster rows={() => [row({ key: 'a', state: 'done' })]} startedAt={at} now={() => 0}
      onActivate={noop} onStop={() => { stopped++; }} />
  ));
  openRowMenu(getByTestId);
  fireEvent.click(screen.getByTestId('agents-menu-end'));
  expect(stopped).toBe(0);
  // The confirm REPLACES the list — the destructive item must not stay
  // under the pointer that just landed on it.
  expect(screen.queryByTestId('agents-menu-end')).toBeNull();
  fireEvent.click(screen.getByTestId('agents-menu-end-cancel'));
  expect(stopped).toBe(0);
  expect(screen.queryByTestId('agents-row-actions')).toBeNull();

  openRowMenu(getByTestId);
  fireEvent.click(screen.getByTestId('agents-menu-end'));
  fireEvent.click(screen.getByTestId('agents-menu-end-confirm'));
  expect(stopped).toBe(1);
  // And the menu closes, so a stray second click ends nothing else.
  expect(screen.queryByTestId('agents-row-actions')).toBeNull();
});

// Reopening must not resume where it left off: a menu that remembered
// the armed confirm would fire on the next row you opened it from.
test('verbs: reopening the menu forgets a pending confirm', () => {
  const { getByTestId, queryByTestId } = render(() => (
    <AgentRoster rows={() => [row({ key: 'a', state: 'done' })]} startedAt={at} now={() => 0}
      onActivate={noop} onStop={noop} />
  ));
  openRowMenu(getByTestId);
  fireEvent.click(screen.getByTestId('agents-menu-end'));
  expect(screen.queryByTestId('agents-menu-end-confirm')).toBeTruthy();
  fireEvent.click(screen.getByTestId('agents-menu-end-cancel'));
  openRowMenu(getByTestId);
  expect(screen.queryByTestId('agents-menu-end-confirm')).toBeNull();
  expect(screen.queryByTestId('agents-menu-end')).toBeTruthy();
});

// The row itself is a focus target. A menu trigger that also focused the
// window would fight the action the user asked for.
test('verbs: opening the menu does not also focus the row', () => {
  let focused = 0;
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => [row({ key: 'a', state: 'working' })]} startedAt={at} now={() => 0}
      onActivate={() => { focused++; }} onDetach={noop} />
  ));
  openRowMenu(getByTestId);
  expect(focused).toBe(0);
});

// There used to be two `title` attributes on the row div; JSX kept the
// last, so a detached row advertised a terminal it does not have.
test('verbs: a detached row says it is detached', () => {
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => [row({ key: 'a', state: 'done', detached: true })]} startedAt={at}
      now={() => 0} onActivate={noop} />
  ));
  expect(getByTestId('agents-row-a').getAttribute('title')).toContain('Detached');
});

test('verbs: a detached row reattaches on a single click (docs/AGENT_UX.md N4)', () => {
  let reattached = 0;
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => [row({ key: 'a', state: 'done', detached: true })]} startedAt={at}
      now={() => 0} onActivate={noop} onReattach={() => { reattached++; }} />
  ));
  fireEvent.click(getByTestId('agents-row-a'));
  expect(reattached).toBe(1);
});

// ---- master-detail (docs/SIDEBAR.md M2) ----

test('the host\'s current session is marked, so the pane reads as selected', () => {
  const rows = [row({ key: 'a' }), row({ key: 'b' })];
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => rows} startedAt={at} now={() => 0} onActivate={noop} activeKey={() => 'b'} />
  ));
  expect(getByTestId('agents-row-b').getAttribute('data-active')).toBe('true');
  expect(getByTestId('agents-row-a').getAttribute('data-active')).toBe('false');
});

test('with no activeKey nothing is marked — the rail has no "current" row', () => {
  const rows = [row({ key: 'a' })];
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => rows} startedAt={at} now={() => 0} onActivate={noop} />
  ));
  expect(getByTestId('agents-row-a').getAttribute('data-active')).toBe('false');
});

test('activating a row hands the host the row, not an interpretation of it', () => {
  // The rail went to the owning terminal; the app points its detail pane
  // at the session. The roster must not decide which.
  const rows = [row({ key: 'a' }), row({ key: 'b' })];
  let got = '';
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => rows} startedAt={at} now={() => 0} onActivate={(r) => { got = r.key; }} />
  ));
  fireEvent.click(getByTestId('agents-row-b'));
  expect(got).toBe('b');
});

test('a detached row reattaches rather than activating — one gesture, two meanings', () => {
  // Clicking a detached row cannot "go to its window": it has none. The
  // double-spawn this used to guard against is agentd's problem, and
  // claimDetached solves it atomically (TestClaimDetachedAllowsOnlyOne-
  // Reattach), so the row is free to answer the first click.
  const rows = [row({ key: 'd', detached: true })];
  let activated = 0;
  let reattached = 0;
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => rows} startedAt={at} now={() => 0}
      onActivate={() => { activated++; }} onReattach={() => { reattached++; }} />
  ));
  fireEvent.click(getByTestId('agents-row-d'));
  expect(activated).toBe(0);
  expect(reattached).toBe(1);
});

test('a detached row with no reattach handler is simply inert', () => {
  const rows = [row({ key: 'd', detached: true })];
  let activated = 0;
  const { getByTestId } = render(() => (
    <AgentRoster rows={() => rows} startedAt={at} now={() => 0} onActivate={() => { activated++; }} />
  ));
  fireEvent.click(getByTestId('agents-row-d'));
  expect(activated).toBe(0);
});
