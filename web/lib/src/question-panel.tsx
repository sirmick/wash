// The question panel: an agent's questions for the human, pinned above the
// session's composer until answered. One card per question, its options as
// buttons (one pick, or several), room for your own words, any question
// skippable, one Submit for all of them. An agent that asked a list of
// questions in prose left the person to answer them in prose; this is the
// structured form of the same thing (agentd question.go).

import { For, Show, createEffect, createSignal, on } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import type * as agentproto from './agent-protocol.gen';
import { tokens } from './tokens';
import { Markdown } from './markdown';

export type QuestionAnswers = Record<string, { selected?: string[]; text?: string }>;

export interface QuestionPanelProps {
  questions: () => agentproto.PendingQuestion[];
  /** Answer (accept, with answers) or decline one question set. */
  onAnswer?: (id: string, action: 'accept' | 'decline', answers?: QuestionAnswers) => void;
}

const chip = (on: boolean, recommended: boolean): JSX.CSSProperties => ({
  display: 'inline-flex', 'flex-direction': 'column', 'align-items': 'flex-start', gap: '2px',
  'text-align': 'left', font: tokens.type.textSm, cursor: 'pointer',
  padding: `${tokens.spaceXs}px ${tokens.spaceMd}px`, 'border-radius': tokens.radiusSm,
  border: `1px solid ${on ? tokens.borderFocus : recommended ? tokens.accentAmber : tokens.borderMenu}`,
  background: on ? tokens.bgInfo : tokens.bgWindow, color: on ? tokens.fgInfo : tokens.fg,
});

/** One question set: its cards and the Submit that answers them all. */
const QuestionSetCard: Component<{ q: agentproto.PendingQuestion; onAnswer?: QuestionPanelProps['onAnswer'] }> = (p) => {
  const [answers, setAnswers] = createSignal<QuestionAnswers>({});
  const [focus, setFocus] = createSignal(0);
  // A new question set in the same slot starts clean.
  createEffect(on(() => p.q.id, () => { setAnswers({}); setFocus(0); }));
  const get = (id: string) => answers()[id] ?? {};
  const pick = (qid: string, label: string, multi: boolean) => {
    const cur = get(qid).selected ?? [];
    const next = multi ? (cur.includes(label) ? cur.filter((l) => l !== label) : [...cur, label]) : (cur[0] === label ? [] : [label]);
    setAnswers({ ...answers(), [qid]: { ...get(qid), selected: next } });
  };
  const setText = (qid: string, text: string) => setAnswers({ ...answers(), [qid]: { ...get(qid), text } });
  const skip = (qid: string) => { const next = { ...answers() }; delete next[qid]; setAnswers(next); };
  const submit = () => {
    const out: QuestionAnswers = {};
    for (const [id, a] of Object.entries(answers())) {
      const text = a.text?.trim();
      if ((a.selected?.length ?? 0) === 0 && !text) continue;
      out[id] = { ...(a.selected?.length ? { selected: a.selected } : {}), ...(text ? { text } : {}) };
    }
    p.onAnswer?.(p.q.id, 'accept', out);
  };
  const answered = () => p.q.set.questions.filter((x) => (get(x.id).selected?.length ?? 0) > 0 || get(x.id).text?.trim()).length;
  // Keys: 1–9 pick an option of the question in focus; Ctrl+Enter submits.
  const onKeyDown = (e: KeyboardEvent) => {
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') { e.preventDefault(); submit(); return; }
    const inText = (e.target as HTMLElement)?.tagName === 'TEXTAREA';
    if (inText || e.altKey || e.ctrlKey || e.metaKey || !/^[1-9]$/.test(e.key)) return;
    const q = p.q.set.questions[focus()];
    const o = q?.options?.[Number(e.key) - 1];
    if (!q || !o) return;
    e.preventDefault();
    pick(q.id, o.label, !!q.multi);
  };
  return (
    <section data-testid={`question-set-${p.q.id}`} aria-label="Questions for you" onKeyDown={onKeyDown}
      style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceSm}px` }}>
      <div style={{ display: 'flex', 'align-items': 'baseline', gap: `${tokens.spaceSm}px` }}>
        <span style={{ font: tokens.type.titleSm, color: tokens.accentAmber }}>● Needs you</span>
        <span style={{ flex: 1, color: tokens.fgMuted, font: tokens.type.textSm }}>
          {p.q.source === 'decision' ? 'The agent is waiting for your answer.' : 'The agent asked, and waits for your answer.'}
        </span>
      </div>
      <Show when={p.q.set.title}><div style={{ font: tokens.type.textMd, 'font-weight': 600 }}><Markdown text={p.q.set.title!} /></div></Show>
      <For each={p.q.set.questions}>{(q, i) => (
        <div data-testid={`question-${q.id}`} tabIndex={0} onFocusIn={() => setFocus(i())}
          style={{ border: `1px solid ${focus() === i() ? tokens.borderFocus : tokens.borderMenu}`, 'border-radius': tokens.radiusMd, padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`, background: tokens.bgInset, outline: 'none' }}>
          <div style={{ display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'baseline' }}>
            <Show when={q.header}><span style={{ font: tokens.type.monoSm, color: tokens.fgMuted }}>{q.header}</span></Show>
            <span style={{ flex: 1 }}><Markdown text={`${i() + 1}. ${q.question}`} /></span>
            <button data-wash-hit type="button" data-testid={`question-skip-${q.id}`} onClick={() => skip(q.id)}
              style={{ border: 'none', background: 'transparent', color: tokens.fgDim, cursor: 'pointer', font: tokens.type.textSm }}>Skip</button>
          </div>
          <Show when={(q.options ?? []).length}>
            <div role={q.multi ? 'group' : 'radiogroup'} aria-label={q.question} style={{ display: 'flex', 'flex-wrap': 'wrap', gap: `${tokens.spaceXs}px`, 'margin-top': `${tokens.spaceXs}px` }}>
              <For each={q.options ?? []}>{(o, n) => {
                const on = () => (get(q.id).selected ?? []).includes(o.label);
                return <button data-wash-hit type="button" role={q.multi ? 'checkbox' : 'radio'} aria-checked={on()}
                  data-testid={`question-option-${q.id}-${n()}`} onClick={() => pick(q.id, o.label, !!q.multi)}
                  title={o.description} style={chip(on(), o.label === q.recommended)}>
                  <span>{n() < 9 ? `${n() + 1} ` : ''}{q.multi ? (on() ? '☑ ' : '☐ ') : ''}{o.label}{o.label === q.recommended ? ' · recommended' : ''}</span>
                  <Show when={o.description}><small style={{ color: tokens.fgMuted }}>{o.description}</small></Show>
                </button>;
              }}</For>
            </div>
          </Show>
          <Show when={!q.no_text}>
            <textarea data-testid={`question-text-${q.id}`} aria-label={`Your answer: ${q.question}`} rows={1}
              placeholder={(q.options ?? []).length ? 'Your own answer, or a note (optional)' : 'Your answer'}
              value={get(q.id).text ?? ''} onInput={(e) => setText(q.id, e.currentTarget.value)}
              style={{ width: '100%', 'box-sizing': 'border-box', 'margin-top': `${tokens.spaceXs}px`, font: tokens.type.textSm, background: tokens.bgWindow, color: tokens.fg, border: `1px solid ${tokens.borderMenu}`, 'border-radius': tokens.radiusSm, resize: 'vertical' }} />
          </Show>
        </div>
      )}</For>
      <div style={{ display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'center' }}>
        <button data-wash-hit type="button" data-testid={`question-submit-${p.q.id}`} onClick={submit}
          style={{ ...chip(true, false), 'flex-direction': 'row', background: tokens.bgSuccess, color: tokens.fgSuccess, border: `1px solid ${tokens.bgSuccess}` }}>
          Submit {answered()}/{p.q.set.questions.length} <small style={{ opacity: 0.7 }}>Ctrl+Enter</small>
        </button>
        <button data-wash-hit type="button" data-testid={`question-decline-${p.q.id}`} onClick={() => p.onAnswer?.(p.q.id, 'decline')}
          style={{ ...chip(false, false), 'flex-direction': 'row' }}>Decline</button>
      </div>
    </section>
  );
};

/** Every waiting question set of one session, scrollable, above its composer. */
export const QuestionPanel: Component<QuestionPanelProps> = (props) => (
  <Show when={props.questions().length}>
    <div data-testid="question-panel" style={{
      flex: 'none', 'max-height': '55%', 'overflow-y': 'auto', 'overscroll-behavior': 'contain',
      'border-top': `2px solid ${tokens.accentAmber}`, background: tokens.bgMenu,
      padding: `${tokens.spaceMd}px`, display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceLg}px`,
    }}>
      <For each={props.questions()}>{(q) => <QuestionSetCard q={q} onAnswer={props.onAnswer} />}</For>
    </div>
  </Show>
);
