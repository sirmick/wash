import { For, Show, createSignal } from 'solid-js';
import type { Component } from 'solid-js';
import { AgentSession, Button, Markdown, Splitter, tokens } from '@wash/ui';
import type { WorkspaceFrame, WorkspaceResult } from './WorkspaceSidebar';

export type WorkspaceAction = (name: string, args: Record<string, unknown>) => void;
const heading = { font: tokens.type.titleSm, padding: `${tokens.spaceSm}px 0` };

export const WorkspacePlan: Component<{ frame: WorkspaceFrame }> = (props) => {
  const w = () => props.frame.workspace!;
  return <section data-testid="workspace-plan" style={{ height: '100%', overflow: 'auto', padding: `${tokens.spaceMd}px`, 'box-sizing': 'border-box' }}>
      <Show when={w().items.length}>
        <div style={heading}>Plan</div>
        <ol style={{ margin: 0, padding: 0, 'list-style': 'none' }}>
          <For each={w().items}>{(item) => (
            <li data-testid={`workspace-item-${item.id}`} style={{ padding: `${tokens.spaceSm}px 0`, display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'baseline' }}>
              <span aria-hidden="true">{item.emoji || ({ pending: '○', active: '◉', blocked: '⏳', done: '✓' }[item.state])}</span>
              <span style={{ flex: 1, 'overflow-wrap': 'anywhere' }}>{item.text}</span>
              <small style={{ color: tokens.fgMuted }}>{item.state}</small>
            </li>
          )}</For>
        </ol>
      </Show>
      <Show when={w().document}>
        <section data-testid="workspace-document" style={{ 'overflow-wrap': 'anywhere' }}>
          <div style={heading}>{w().document?.title || w().document?.path}</div>
          <Show when={!props.frame.document_error} fallback={<div role="alert">{props.frame.document_error}</div>}>
            <Markdown text={props.frame.document_text ?? ''} />
          </Show>
        </section>
      </Show>
  </section>;
};

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
  frame: WorkspaceFrame; result?: WorkspaceResult; memberID: string;
  onAnswer?: (id: string, decision: 'allow' | 'deny', rule?: string, scope?: 'workspace') => void;
  draft: string; onDraft: (draft: string) => void; onAction: WorkspaceAction;
}> = (props) => {
  const w = () => props.frame.workspace!;
  const member = () => w().members.find((m) => m.id === props.memberID);
  const draft = () => props.draft;
  const setDraft = (text: string) => props.onDraft(text);
  const events = () => props.frame.preview?.member_id === props.memberID ? props.frame.preview.events
    : props.result?.operation === 'member_inspect' && props.result.result?.member_id === props.memberID ? props.result.result.events ?? [] : [];
  const asks = () => props.frame.preview?.member_id === props.memberID ? props.frame.preview.asks ?? [] : props.result?.operation === 'member_inspect' && props.result.result?.member_id === props.memberID ? props.result.result.asks ?? [] : [];
  let panes!: HTMLDivElement;
  const [split, setSplit] = createSignal(savedMemberSplit());
  const persistSplit = () => {
    try { localStorage.setItem(MEMBER_SPLIT_KEY, String(split())); } catch { /* storage can be disabled */ }
  };
  const previewNote = () => props.frame.preview?.member_id === props.memberID ? props.frame.preview.note
    : props.result?.operation === 'member_inspect' && props.result.result?.member_id === props.memberID ? props.result.result.note : undefined;
  return <div style={{ height: '100%', 'min-height': 0, padding: `${tokens.spaceMd}px`, 'box-sizing': 'border-box' }}>
      <Show when={member()}>{(m) => (
        <section data-testid="workspace-member-detail" style={{ height: '100%', display: 'flex', 'flex-direction': 'column', 'min-height': 0 }}>
          {/* Brief above, turns below, on a divider the person drags: a long
              task brief and a long transcript both need room, and which one
              matters changes as the member works. */}
          <div ref={panes} data-testid="workspace-member-panes" style={{
            flex: 1, 'min-height': 0, display: 'grid',
            'grid-template-rows': `minmax(0, ${split()}fr) 5px minmax(0, ${100 - split()}fr)`,
          }}>
          <div data-testid="workspace-member-brief" style={{ overflow: 'auto', 'min-height': 0, 'overflow-wrap': 'anywhere' }}>
          <div style={heading}>{m().name} · {m().lifetime}</div>
          <Show when={m().launch_settings}>
            <p data-testid="workspace-member-launch" style={{ color: tokens.fgMuted, 'overflow-wrap': 'anywhere' }}>
              Launched: {[m().profile, m().provider, m().launch_settings?.model, m().launch_settings?.capability ? `capability ${m().launch_settings?.capability}` : "", m().launch_settings?.thinking ? `thinking ${m().launch_settings?.thinking}` : ''].filter(Boolean).join(' · ')}
            </p>
          </Show>
          <Show when={m().state === 'paused' || m().state === 'failed'}><Button onClick={() => props.onAction('member_resume', { member_id: m().id })}>Resume member</Button></Show>
          <Button disabled={m().state === 'ended'} onClick={() => props.onAction('member_open', { member_id: m().id })}>Open Agent window</Button>
          <For each={w().assignments.filter((a) => a.member_id === m().id)}>{(a) => (
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
          <div style={{ 'min-height': 0 }}><AgentSession events={events} asks={asks} onAnswer={props.onAnswer} hideComposer /></div>
          </div>
          <textarea aria-label={`Message ${m().name}`} value={draft()} onInput={(e) => setDraft(e.currentTarget.value)} style={{ width: '100%', 'box-sizing': 'border-box' }} />
          <Button disabled={!draft().trim() || m().state === 'ended'} onClick={() => { props.onAction('member_message', { recipient: m().id, body: draft() }); setDraft(''); }}>Send message</Button>
        </section>
      )}</Show>
  </div>;
};
