import type { agentproto } from '@wash/ui';
import { For, Show, createSignal } from 'solid-js';
import type { Component } from 'solid-js';
import { AgentSession, Button, Markdown, Splitter, tokens } from '@wash/ui';
import type { QuestionAnswers } from '@wash/ui';
import { planPath, planStateColor } from './WorkspacePlanGraph';

export type WorkspaceAction = (name: string, args: agentproto.WorkspaceActionArgs) => void;
const heading = { font: tokens.type.titleSm, padding: `${tokens.spaceSm}px 0` };

// The member pane's divider between its brief (task, results) and its turns,
// as a percentage of the pane's height; one setting for every member.
const MEMBER_SPLIT_KEY = 'wash.agent.workspace.member.split';
const MEMBER_MIN = 10;
const MEMBER_MAX = 85;
const memberClamp = (value: number) => Math.max(MEMBER_MIN, Math.min(MEMBER_MAX, value));
const savedMemberSplit = () => {
  try {
    const saved = Number(localStorage.getItem(MEMBER_SPLIT_KEY) ?? 35);
    return Number.isFinite(saved) ? memberClamp(saved) : 35;
  } catch { return 35; }
};

export const WorkspaceMemberPanel: Component<{
  frame: agentproto.WorkspaceState; result?: agentproto.WorkspaceResult; memberID: string;
  onAnswer?: (id: string, decision: 'allow' | 'deny', rule?: string, scope?: 'workspace') => void;
  draft: string; onDraft: (draft: string) => void; onAction: WorkspaceAction;
  /** Opens the Plan tab on a node: the breadcrumb's destination. */
  onOpenNode?: (id: string) => void;
  onQuestionAnswer?: (id: string, action: 'accept' | 'decline', answers?: QuestionAnswers) => void;
}> = (props) => {
  const w = () => props.frame.workspace!;
  const member = () => (w().members ?? []).find((m) => m.id === props.memberID);
  const draft = () => props.draft;
  const setDraft = (text: string) => props.onDraft(text);
  const events = () => props.frame.preview?.member_id === props.memberID ? props.frame.preview.events ?? []
    : props.result?.operation === 'member_inspect' && props.result.transcript?.member_id === props.memberID ? props.result.transcript.events ?? [] : [];
  const asks = () => props.frame.preview?.member_id === props.memberID ? props.frame.preview.asks ?? [] : props.result?.operation === 'member_inspect' && props.result.transcript?.member_id === props.memberID ? props.result.transcript.asks ?? [] : [];
  // The member's questions for the human, live from the frame: answered here,
  // in its tab, like its permission asks.
  const questions = () => (props.frame.questions ?? []).filter((q) => q.member_id === props.memberID);
  let panes!: HTMLDivElement;
  const [split, setSplit] = createSignal(savedMemberSplit());
  const persistSplit = () => {
    try { localStorage.setItem(MEMBER_SPLIT_KEY, String(split())); } catch { /* storage can be disabled */ }
  };
  const previewNote = () => props.frame.preview?.member_id === props.memberID ? props.frame.preview.note
    : props.result?.operation === 'member_inspect' && props.result.transcript?.member_id === props.memberID ? props.result.transcript.note : undefined;
  return <div style={{ height: '100%', 'min-height': 0, padding: `${tokens.spaceMd}px`, 'box-sizing': 'border-box' }}>
      <Show when={member()}>{(m) => (
        <section data-testid="workspace-member-detail" style={{ height: '100%', display: 'flex', 'flex-direction': 'column', 'min-height': 0 }}>
          {/* Brief above, turns below, on a divider the person drags: a long
              task brief and a long transcript both need room, and which one
              matters changes as the member works. */}
          <div ref={panes} data-testid="workspace-member-panes" style={{
            flex: 1, 'min-height': 0, display: 'grid',
            'grid-template-rows': `minmax(0, ${split()}fr) 5px minmax(0, ${100 - split()}fr)`,
            // One column that may shrink: an implicit column is as wide as its
            // widest content, so one long line stretched the pane past the
            // window and nothing in it wrapped.
            'grid-template-columns': 'minmax(0, 1fr)',
          }}>
          <div data-testid="workspace-member-brief" style={{ overflow: 'auto', 'min-height': 0, 'min-width': 0, 'overflow-wrap': 'anywhere' }}>
          {/* Where this member sits in the plan; each step opens the Plan tab there. */}
          <Show when={planPath(w().plan, m().node).length}>
            <nav data-testid="workspace-member-breadcrumb" aria-label="Plan node" style={{ font: tokens.type.textSm, color: tokens.fgMuted }}>
              <For each={planPath(w().plan, m().node)}>{(n, i) => <>
                {i() ? ' › ' : ''}
                <button data-wash-hit type="button" onClick={() => props.onOpenNode?.(n.id)} title={`${n.title} · ${n.state}`}
                  style={{ border: 'none', background: 'transparent', padding: 0, cursor: 'pointer', font: 'inherit', color: planStateColor(n.state) }}>{n.emoji} {n.id}</button>
              </>}</For>
            </nav>
          </Show>
          <div style={heading}>{m().name} · {m().lifetime}</div>
          <Show when={m().launch_settings}>
            <p data-testid="workspace-member-launch" style={{ color: tokens.fgMuted, 'overflow-wrap': 'anywhere' }}>
              Launched: {[m().catalog, m().model && m().model !== m().launch_settings?.model ? `${m().model} slot` : '', m().provider, m().launch_settings?.model, m().launch_settings?.capability ? `capability ${m().launch_settings?.capability}` : "", m().launch_settings?.effort ? `effort ${m().launch_settings?.effort}` : ''].filter(Boolean).join(' · ')}
            </p>
          </Show>
          <Show when={m().state === 'paused' || m().state === 'failed'}><Button onClick={() => props.onAction('member_resume', { member_id: m().id })}>Resume member</Button></Show>
          <Button disabled={m().state === 'ended'} onClick={() => props.onAction('member_open', { member_id: m().id })}>Open Agent window</Button>
          <For each={(w().assignments ?? []).filter((a) => a.member_id === m().id)}>{(a) => (
            <section data-testid={`workspace-assignment-${a.id}`} style={{ 'margin-top': `${tokens.spaceMd}px` }}>
              <div style={{ color: tokens.fgMuted, font: tokens.type.textSm }}>Assignment · {a.state}</div>
              <Markdown text={a.text} />
              <Show when={a.result}>
                <div style={{ color: tokens.fgMuted, font: tokens.type.textSm, 'margin-top': `${tokens.spaceSm}px` }}>Result</div>
                <Markdown text={a.result!} />
              </Show>
            </section>
          )}</For>
          <Show when={previewNote()}><p>{previewNote()}</p></Show>
          </div>
          <div role="separator" aria-label="Resize brief and turns" aria-orientation="horizontal" tabIndex={0}
            aria-valuemin={MEMBER_MIN} aria-valuemax={MEMBER_MAX} aria-valuenow={Math.round(split())}
            data-testid="workspace-member-splitter" onKeyDown={(event) => {
              if (!['ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) return;
              event.preventDefault();
              setSplit(event.key === 'Home' ? MEMBER_MIN : event.key === 'End' ? MEMBER_MAX : memberClamp(split() + (event.key === 'ArrowUp' ? -2 : 2)));
              persistSplit();
            }}>
            <Splitter container={panes} orientation="horizontal" thickness={5} min={MEMBER_MIN} max={MEMBER_MAX} onChange={setSplit} onCommit={persistSplit} />
          </div>
          <div style={{ 'min-height': 0, 'min-width': 0 }}><AgentSession events={events} asks={asks} onAnswer={props.onAnswer} questions={questions} onQuestionAnswer={props.onQuestionAnswer} hideComposer /></div>
          </div>
          <textarea aria-label={`Message ${m().name}`} value={draft()} onInput={(e) => setDraft(e.currentTarget.value)} style={{ width: '100%', 'box-sizing': 'border-box' }} />
          <Button disabled={!draft().trim() || m().state === 'ended'} onClick={() => { props.onAction('member_message', { recipient: m().id, body: draft() }); setDraft(''); }}>Send message</Button>
        </section>
      )}</Show>
  </div>;
};
