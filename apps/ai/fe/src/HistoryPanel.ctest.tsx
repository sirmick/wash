// Component test (Tier B) for the History panel.
//
// The panel is a pure renderer: searching happens in agentd, because the
// thing being searched is the stored CONVERSATION, which the FE has
// never seen. So what is testable here is the shape — that metadata a
// menu could not carry actually renders, that an empty list says which
// KIND of empty it is, and that the keyboard works. The round trip is
// e2e's job.

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { HistoryPanel, fmtAgo, fmtSpan, historyAction, sessionLabel, type SessionMeta } from './HistoryPanel.tsx';

afterEach(cleanup);

const noop = () => {};
const sess = (over: Partial<SessionMeta> = {}): SessionMeta => ({
  session_id: 's-1',
  agent: 'claude',
  model: 'Claude Opus 4.5',
  dir: 'wash',
  title: 'Fix the reconnect race',
  started_ms: 1_700_000_000_000,
  ended_ms: 1_700_000_600_000,
  end_reason: 'ended',
  events: 42,
  bytes: 2048,
  ...over,
});

const panel = (over: {
  sessions?: SessionMeta[];
  query?: string;
  onResume?: (s: SessionMeta) => void;
  onQuery?: (q: string) => void;
} = {}) =>
  render(() => (
    <HistoryPanel
      sessions={() => over.sessions ?? [sess()]}
      query={() => over.query ?? ''}
      onQuery={over.onQuery ?? noop}
      onResume={over.onResume ?? noop}
      onClose={noop}
    />
  ));

test('a row carries the metadata that made this a panel and not a menu', () => {
  const { getByTestId } = panel();
  expect(getByTestId('ai-history-title').textContent).toBe('Fix the reconnect race');
  expect(getByTestId('ai-history-agent').textContent).toBe('claude');
  // The model is the field the whole metadata pass existed for.
  expect(getByTestId('ai-history-model').textContent).toBe('Claude Opus 4.5');
  const row = getByTestId('ai-history-row');
  expect(row.getAttribute('data-session-id')).toBe('s-1');
  expect(row.textContent).toContain('42 lines');
  expect(row.textContent).toContain('10m'); // duration, from started→ended
});

// A session the router outlived resumes differently from one that
// finished, so the list says which it is.
test('a session that never ended is marked unfinished', () => {
  const { queryByTestId } = panel({ sessions: [sess({ end_reason: undefined, ended_ms: undefined })] });
  expect(queryByTestId('ai-history-unfinished')).toBeTruthy();
});

test('a finished session is not marked unfinished', () => {
  const { queryByTestId } = panel();
  expect(queryByTestId('ai-history-unfinished')).toBeNull();
});

// "No results for what you typed" and "you have no history" are
// different facts, and a single empty state would tell the wrong one.
test('empty says which kind of empty it is', () => {
  const none = panel({ sessions: [] });
  expect(none.getByTestId('ai-history-empty').textContent).toContain('No conversations yet');
  cleanup();

  const miss = panel({ sessions: [], query: 'reconnect' });
  expect(miss.getByTestId('ai-history-empty').textContent).toContain('Nothing matches');
});

test('typing asks the backend, because the search runs over stored conversations', () => {
  const seen: string[] = [];
  const { getByTestId } = panel({ onQuery: (q) => seen.push(q) });
  const input = getByTestId('ai-history-search') as HTMLInputElement;
  fireEvent.input(input, { target: { value: 'banner' } });
  expect(seen).toEqual(['banner']);
});

// The search box owns focus, so the arrows and Enter have to work from
// inside it or the list is mouse-only. This also proves Input forwards
// ref and onKeyDown rather than swallowing them.
test('the keyboard drives the list from the search box', () => {
  const resumed: string[] = [];
  const rows = [sess({ session_id: 'a' }), sess({ session_id: 'b' }), sess({ session_id: 'c' })];
  const { getByTestId } = panel({ sessions: rows, onResume: (s) => resumed.push(s.session_id) });
  const input = getByTestId('ai-history-search') as HTMLInputElement;

  // The panel opens focused: the first thing anyone does is type.
  expect(document.activeElement).toBe(input);

  fireEvent.keyDown(input, { key: 'Enter' });
  expect(resumed).toEqual(['a']);

  fireEvent.keyDown(input, { key: 'ArrowDown' });
  fireEvent.keyDown(input, { key: 'ArrowDown' });
  fireEvent.keyDown(input, { key: 'Enter' });
  expect(resumed).toEqual(['a', 'c']);

  fireEvent.keyDown(input, { key: 'ArrowUp' });
  fireEvent.keyDown(input, { key: 'Enter' });
  expect(resumed).toEqual(['a', 'c', 'b']);
});

test('selection cannot walk off either end of the list', () => {
  const resumed: string[] = [];
  const rows = [sess({ session_id: 'a' }), sess({ session_id: 'b' })];
  const { getByTestId } = panel({ sessions: rows, onResume: (s) => resumed.push(s.session_id) });
  const input = getByTestId('ai-history-search') as HTMLInputElement;
  for (let i = 0; i < 5; i++) fireEvent.keyDown(input, { key: 'ArrowDown' });
  fireEvent.keyDown(input, { key: 'Enter' });
  expect(resumed).toEqual(['b']);
  for (let i = 0; i < 5; i++) fireEvent.keyDown(input, { key: 'ArrowUp' });
  fireEvent.keyDown(input, { key: 'Enter' });
  expect(resumed).toEqual(['b', 'a']);
});

test('clicking a row resumes that session', () => {
  const resumed: string[] = [];
  const { getAllByTestId } = panel({
    sessions: [sess({ session_id: 'x' }), sess({ session_id: 'y' })],
    onResume: (s) => resumed.push(s.session_id),
  });
  fireEvent.click(getAllByTestId('ai-history-row')[1]);
  expect(resumed).toEqual(['y']);
});

// A session that never named itself still needs something to click.
test('sessionLabel falls back through title → agent·dir → id', () => {
  expect(sessionLabel(sess())).toBe('Fix the reconnect race');
  expect(sessionLabel(sess({ title: undefined }))).toBe('claude · wash');
  expect(sessionLabel(sess({ title: undefined, dir: undefined, cwd: undefined }))).toBe('claude');
  expect(sessionLabel({ session_id: 'bare' })).toBe('bare');
});

test('fmtSpan is empty when a session never ended', () => {
  expect(fmtSpan(1_000_000, 1_060_000)).toBe('1m');
  expect(fmtSpan(1_000_000, 1_000_000 + 3_600_000 + 120_000)).toBe('1h 2m');
  expect(fmtSpan(1_000_000, undefined)).toBe('');
  // A clock that went backwards is not a negative duration.
  expect(fmtSpan(2_000_000, 1_000_000)).toBe('');
});

test('fmtAgo reads as recency, and says nothing about a session with no time', () => {
  const now = 1_700_000_000_000;
  expect(fmtAgo(now, now - 1_000)).toBe('just now');
  expect(fmtAgo(now, now - 300_000)).toBe('5m ago');
  expect(fmtAgo(now, now - 7_200_000)).toBe('2h ago');
  expect(fmtAgo(now, now - 172_800_000)).toBe('2d ago');
  expect(fmtAgo(now, 0)).toBe('');
});

// The panel had no notion of liveness at all, so it would happily offer
// to resume a session that was already running — duplicating it. That is
// exactly what the History MENU's live-filter exists to prevent, in the
// view that had no filter. Both views now ask the same question.
test('historyAction tells resume from reattach from focus from neither', () => {
  expect(historyAction(sess())).toBe('resume');
  expect(historyAction(sess({ live: true, detached: true, row_key: 'acp:2' }))).toBe('reattach');
  // Live with a window: go to it. Resuming would fork a second adapter
  // onto one conversation (docs/AGENT_UX.md N1).
  expect(historyAction(sess({ live: true, row_key: 'acp:2' }))).toBe('focus');
  // Live but unaddressable — no row key, so there is nothing to name in
  // either a reattach or a focus, and resuming would start a second copy.
  expect(historyAction(sess({ live: true }))).toBe('none');
  expect(historyAction(sess({ live: true, detached: true }))).toBe('none');
});

test('a running session does not act like one more thing to open', () => {
  const clicked: string[] = [];
  const { getAllByTestId } = render(() => (
    <HistoryPanel
      sessions={() => [
        sess({ session_id: 's-gone' }),
        // Live with no row key: nothing to address, so nothing to click.
        sess({ session_id: 's-live', live: true }),
        sess({ session_id: 's-det', live: true, detached: true, row_key: 'acp:2' }),
        sess({ session_id: 's-here', live: true, row_key: 'acp:3' }),
      ]}
      query={() => ''}
      loading={() => false}
      onQuery={noop}
      onClose={noop}
      onResume={(s) => clicked.push(s.session_id)}
    />
  ));
  const rows = getAllByTestId('ai-history-row');
  expect(rows.map((r) => r.getAttribute('data-action')))
    .toEqual(['resume', 'none', 'reattach', 'focus']);

  rows.forEach((r) => fireEvent.click(r));
  // Only the unaddressable one is inert; the rest all report up, each
  // for the host to turn into its own verb.
  expect(clicked).toEqual(['s-gone', 's-det', 's-here']);
});
