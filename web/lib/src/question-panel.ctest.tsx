import { afterEach, expect, test, vi } from 'vitest';
import { createSignal } from 'solid-js';
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library';
import type * as agentproto from './agent-protocol.gen';
import { QuestionPanel } from './question-panel';

afterEach(cleanup);

const pending = (questions: agentproto.Question[], title = ''): agentproto.PendingQuestion =>
  ({ id: 'set1', row_key: 'acp:1', source: 'elicitation', age_ms: 0, set: { title, questions } });

test('nothing waiting shows nothing', () => {
  render(() => <QuestionPanel questions={() => []} />);
  expect(screen.queryByTestId('question-panel')).toBeNull();
});

// Several questions at once, each answered its own way: a pick with a note,
// several picks, words alone, one skipped. One Submit sends only what was
// answered; a skipped question is left out.
test('one submit answers every question the way it was answered', async () => {
  const onAnswer = vi.fn();
  render(() => <QuestionPanel onAnswer={onAnswer} questions={() => [pending([
    { id: 'clock', question: 'Which clock?', header: 'Clock', options: [{ label: 'Monotonic', description: 'never goes back' }, { label: 'Wall' }], recommended: 'Monotonic' },
    { id: 'targets', question: 'Which targets?', multi: true, no_text: true, options: [{ label: 'rv32' }, { label: 'x86' }, { label: 'arm' }] },
    { id: 'name', question: 'Name it' },
    { id: 'later', question: 'Anything else?' },
  ], 'Timer design')]} />);
  expect(screen.getByTestId('question-panel').textContent).toContain('Timer design');
  expect(screen.getByTestId('question-option-clock-0').textContent).toContain('recommended');
  // Keys pick in the question in focus.
  screen.getByTestId('question-clock').focus();
  await fireEvent.focusIn(screen.getByTestId('question-clock'));
  await fireEvent.keyDown(screen.getByTestId('question-clock'), { key: '1' });
  expect(screen.getByTestId('question-option-clock-0').getAttribute('aria-checked')).toBe('true');
  await fireEvent.input(screen.getByTestId('question-text-clock'), { target: { value: 'RAW if available' } });
  await fireEvent.click(screen.getByTestId('question-option-targets-0'));
  await fireEvent.click(screen.getByTestId('question-option-targets-2'));
  expect(screen.queryByTestId('question-text-targets')).toBeNull();
  await fireEvent.input(screen.getByTestId('question-text-name'), { target: { value: 'tick' } });
  await fireEvent.input(screen.getByTestId('question-text-later'), { target: { value: 'maybe' } });
  await fireEvent.click(screen.getByTestId('question-skip-later'));
  expect(screen.getByTestId('question-submit-set1').textContent).toContain('3/4');
  await fireEvent.keyDown(screen.getByTestId('question-set-set1'), { key: 'Enter', ctrlKey: true });
  expect(onAnswer).toHaveBeenCalledWith('set1', 'accept', {
    clock: { selected: ['Monotonic'], text: 'RAW if available' },
    targets: { selected: ['rv32', 'arm'] },
    name: { text: 'tick' },
  });
});

test('a single choice toggles, and decline answers nothing', async () => {
  const onAnswer = vi.fn();
  render(() => <QuestionPanel onAnswer={onAnswer} questions={() => [pending([{ id: 'go', question: 'Ship?', options: [{ label: 'Yes' }, { label: 'No' }] }])]} />);
  await fireEvent.click(screen.getByTestId('question-option-go-0'));
  await fireEvent.click(screen.getByTestId('question-option-go-1'));
  expect(screen.getByTestId('question-option-go-0').getAttribute('aria-checked')).toBe('false');
  expect(screen.getByTestId('question-option-go-1').getAttribute('aria-checked')).toBe('true');
  await fireEvent.click(screen.getByTestId('question-decline-set1'));
  expect(onAnswer).toHaveBeenCalledWith('set1', 'decline');
});

// The roster re-sends every pending question as a fresh object on any
// update (a transcript line, a state change). A half-answered set must keep
// its answers across that, not remount empty.
test('answers survive the same question set arriving as a new object', async () => {
  const onAnswer = vi.fn();
  const qs: agentproto.Question[] = [
    { id: 'clock', question: 'Which clock?', options: [{ label: 'Monotonic' }, { label: 'Wall' }] },
    { id: 'targets', question: 'Which targets?', multi: true, options: [{ label: 'rv32' }, { label: 'x86' }] },
  ];
  const [list, setList] = createSignal([pending(qs)]);
  render(() => <QuestionPanel onAnswer={onAnswer} questions={list} />);
  await fireEvent.click(screen.getByTestId('question-option-clock-0'));
  setList([pending(qs)]);
  expect(screen.getByTestId('question-option-clock-0').getAttribute('aria-checked')).toBe('true');
  await fireEvent.click(screen.getByTestId('question-option-targets-1'));
  await fireEvent.click(screen.getByRole('button', { name: /^Submit/ }));
  expect(onAnswer).toHaveBeenCalledWith('set1', 'accept', { clock: { selected: ['Monotonic'] }, targets: { selected: ['x86'] } });
});
