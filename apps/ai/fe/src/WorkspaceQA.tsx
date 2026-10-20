import type { agentproto } from '@wash/ui';
import { For, Show, createEffect, createMemo, createSignal, untrack } from 'solid-js';
import type { Component } from 'solid-js';
import { Markdown, tokens } from '@wash/ui';
import { splitQAMarkdown } from './qa-view';
import type { WorkspaceAction } from './WorkspacePanels';

/** The colour a thread's state is drawn in: open work amber, resolved green. */
function qaStateColor(state: string): string {
  switch (state) {
    case 'resolved': return tokens.accentGreen;
    case 'blocked': return tokens.accentRed;
    default: return tokens.accentAmber;
  }
}

/** What an event kind is called in a card (swarm.WriteQAThread's names). */
const KIND: Record<string, string> = {
  open: 'Question', reply: 'Reply', question: 'Question', answer: 'Answer', assign: 'Assigned', block: 'Blocked',
  resolve: 'Resolved', reopen: 'Reopened', decision_request: 'Owner decision requested', decision_response: 'Owner decision',
  progress: 'Progress', instruction: 'Instruction',
};

/** The colour an event's kind is marked with: questions amber, answers blue, closure green. */
function kindColor(kind: string): string {
  switch (kind) {
    case 'open': case 'question': case 'decision_request': return tokens.accentAmber;
    case 'answer': case 'reply': case 'decision_response': return tokens.accentBlue;
    case 'resolve': return tokens.accentGreen;
    case 'block': return tokens.accentRed;
    default: return tokens.borderMenu;
  }
}

/** The events that answer nothing shown: the thread's top level. */
function roots(events: agentproto.QAEvent[]): agentproto.QAEvent[] {
  const ids = new Set(events.map((e) => e.id));
  return events.filter((e) => !e.answers || !ids.has(e.answers));
}

/**
 * One event, and under it the answers agentd linked to it (LinkAnswers):
 * an answer found by its message replying to the question's, or, marked
 * "probably", by being the next answer after an open question.
 */
const QAEventCard: Component<{ event: agentproto.QAEvent; all: agentproto.QAEvent[]; who: (id: string) => string; depth?: number }> = (p) => {
  const e = p.event;
  const replies = () => p.all.filter((r) => r.answers === e.id);
  return <div data-testid={`workspace-qa-event-${e.id}`} data-kind={e.kind} style={{ 'border-left': `3px solid ${kindColor(e.kind)}`, padding: `${tokens.spaceXs}px ${tokens.spaceMd}px`, background: tokens.bgWindow, 'border-radius': tokens.radiusSm }}>
    <div style={{ display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'baseline', font: tokens.type.textSm }}>
      <span style={{ 'font-weight': 600 }}>{p.who(e.author)}</span>
      <span style={{ color: kindColor(e.kind) === tokens.borderMenu ? tokens.fgMuted : kindColor(e.kind) }}>{KIND[e.kind] ?? e.kind}</span>
      <Show when={(p.depth ?? 0) > 0}><span data-testid={`workspace-qa-answers-${e.id}`} style={{ color: tokens.fgMuted }}>↳ {e.answers_inferred ? 'probably answers the above' : 'answers the above'}</span></Show>
      <span style={{ flex: 1 }} />
      <time style={{ color: tokens.fgDim, font: tokens.type.monoSm }}>{new Date(e.created_at).toLocaleString()}</time>
    </div>
    <Markdown text={e.body} />
    <Show when={replies().length}>
      <div data-testid={`workspace-qa-replies-${e.id}`} style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceXs}px`, 'margin-top': `${tokens.spaceXs}px` }}>
        <For each={replies()}>{(r) => <QAEventCard event={r} all={p.all} who={p.who} depth={(p.depth ?? 0) + 1} />}</For>
      </div>
    </Show>
  </div>;
};

/** The frame's document shows an open thread's last 30 events and none of an archived one. */
const OMITTED = /Earlier events omitted|Events are in the thread's file/;

/**
 * The Questions tab: the QA document as one card per thread, its heading a
 * summary row (state, node, id, title, who has it) that opens on the
 * thread's events. Resolved threads start closed, so a long record reads as
 * a list of what is still open. A card whose events the document cut short
 * asks agentd for the whole thread when it opens (qa_thread), and shows
 * every event with its kind marked, so an answer stands under its question.
 */
export const WorkspaceQA: Component<{
  frame: agentproto.WorkspaceState;
  result?: agentproto.WorkspaceResult;
  onAction?: WorkspaceAction;
  /** A thread to open and scroll to (the sidebar's or a node's link). */
  focus?: string;
}> = (props) => {
  const w = () => props.frame.workspace;
  const split = createMemo(() => splitQAMarkdown(props.frame.qa_markdown ?? '# Workspace QA\n\nNo questions yet.'));
  const thread = (id: string) => (w()?.qa ?? []).find((q) => q.id === id);
  const who = (id: string) => (w()?.members ?? []).find((m) => m.id === id)?.name ?? w()?.qa_authors?.[id] ?? (id === 'human' ? 'Owner' : id);
  // Threads read in full, by id; and the ones asked for, so a slow answer
  // is not asked for twice.
  const [loaded, setLoaded] = createSignal<Record<string, agentproto.QAThread>>({});
  const [asked, setAsked] = createSignal<Record<string, 'pending' | string>>({});
  // These effects write the signals they consult, so the reads are
  // untracked (functional setters, untrack): tracking them would re-run
  // the effect on its own write, without end.
  createEffect(() => {
    const r = props.result;
    if (r?.operation !== 'qa_thread') return;
    if (r.thread) {
      const t = r.thread;
      setLoaded((p) => ({ ...p, [t.id]: t }));
      setAsked((p) => ({ ...p, [t.id]: 'done' }));
    } else if (r.error) {
      const pending = Object.entries(untrack(asked)).filter(([, s]) => s === 'pending').map(([id]) => id);
      if (pending.length) setAsked((p) => ({ ...p, [pending[0]]: r.error! }));
    }
  });
  const load = (id: string) => {
    if (!props.onAction || untrack(() => loaded()[id] || asked()[id] === 'pending')) return;
    setAsked((p) => ({ ...p, [id]: 'pending' }));
    props.onAction('qa_thread', { thread_id: id });
  };
  const cut = (id: string, body: string) => !!thread(id)?.archived || OMITTED.test(body);
  const [opened, setOpened] = createSignal<Record<string, boolean>>({});
  const isOpen = (id: string, state: string) => opened()[id] ?? state !== 'resolved';
  let root!: HTMLDivElement;
  createEffect(() => {
    const id = props.focus;
    props.frame.qa_markdown;
    if (!id) return;
    setOpened((p) => ({ ...p, [id]: true }));
    queueMicrotask(() => [...(root?.querySelectorAll('[data-thread]') ?? [])].find((el) => el.getAttribute('data-thread') === id)?.scrollIntoView?.({ block: 'start' }));
  });
  const pill = (state: string) => ({
    font: tokens.type.textSm, color: qaStateColor(state), border: `1px solid ${qaStateColor(state)}`,
    'border-radius': tokens.radiusSm, padding: `0 ${tokens.spaceXs}px`, 'white-space': 'nowrap' as const, 'flex-shrink': 0,
  });
  return <div ref={root} data-testid="workspace-qa-threads" style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceMd}px` }}>
    <Show when={split().preamble}><Markdown text={split().preamble} /></Show>
    <For each={split().threads}>{(t) => {
      const q = () => thread(t.id);
      const state = () => q()?.state ?? '';
      const open = () => isOpen(t.id, state());
      const full = () => loaded()[t.id];
      // Opening a cut card asks for the rest.
      createEffect(() => { if (open() && t.id && cut(t.id, t.body)) load(t.id); });
      return <details data-testid={`workspace-qa-thread-${t.id || t.title}`} data-thread={t.id} data-state={state()} open={open()}
        onToggle={(ev) => { const open = ev.currentTarget.open; setOpened((p) => ({ ...p, [t.id]: open })); }}
        style={{ border: `1px solid ${tokens.borderMenu}`, 'border-left': `4px solid ${qaStateColor(state())}`, 'border-radius': tokens.radiusLg, background: tokens.bgInset }}>
        <summary data-wash-hit="subtle" style={{ display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'baseline', padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`, cursor: 'pointer', 'list-style': 'none' }}>
          <Show when={state()}><span style={pill(state())}>{state()}</span></Show>
          <Show when={q()?.blocking}><span title="Blocks work on its node" style={{ color: tokens.accentRed, 'flex-shrink': 0 }}>●</span></Show>
          <span style={{ color: tokens.fgMuted, font: tokens.type.monoSm, 'flex-shrink': 0 }}>{t.node}{t.node && t.id ? ' · ' : ''}{t.id}</span>
          <span style={{ flex: 1, 'min-width': 0, 'font-weight': 600, 'overflow-wrap': 'anywhere' }}>{t.title}</span>
          <Show when={q()?.assignee}><span style={{ color: tokens.fgMuted, font: tokens.type.textSm, 'flex-shrink': 0 }}>{who(q()!.assignee)}</span></Show>
        </summary>
        <div style={{ padding: `0 ${tokens.spaceMd}px ${tokens.spaceSm}px`, 'border-top': `1px solid ${tokens.borderMenu}` }}>
          <Show when={full()} fallback={<>
            <Show when={t.id && cut(t.id, t.body)}>
              <div data-testid={`workspace-qa-cut-${t.id}`} style={{ color: tokens.fgMuted, font: tokens.type.textSm, padding: `${tokens.spaceSm}px 0 0` }}>
                {asked()[t.id] === 'pending' ? 'Not every event is here — loading the whole thread…'
                  : asked()[t.id] && asked()[t.id] !== 'done' ? `Not every event is here, and the thread could not be read: ${asked()[t.id]}`
                  : props.onAction ? 'Not every event is here.' : 'Not every event is here; the rest are in the thread\'s file.'}
              </div>
            </Show>
            <Markdown text={t.body} />
          </>}>{(th) => (
            <div data-testid={`workspace-qa-events-${t.id}`} style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceSm}px`, padding: `${tokens.spaceSm}px 0` }}>
              <div style={{ color: tokens.fgMuted, font: tokens.type.textSm }}>
                {(th().events ?? []).length} events · assigned to {who(th().assignee)}
                <Show when={th().evidence}> · Evidence: {th().evidence}</Show>
              </div>
              <For each={roots(th().events ?? [])}>{(e) => <QAEventCard event={e} all={th().events ?? []} who={who} />}</For>
            </div>
          )}</Show>
        </div>
      </details>;
    }}</For>
  </div>;
};
