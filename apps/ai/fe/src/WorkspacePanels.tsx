import { For, Show } from 'solid-js';
import type { Component } from 'solid-js';
import { AgentSession, Button, Markdown, tokens } from '@wash/ui';
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

export const WorkspaceMemberPanel: Component<{
  frame: WorkspaceFrame; result?: WorkspaceResult; memberID: string;
  onAnswer?: (id: string, decision: 'allow' | 'deny', rule?: string) => void;
  draft: string; onDraft: (draft: string) => void; onAction: WorkspaceAction;
}> = (props) => {
  const w = () => props.frame.workspace!;
  const member = () => w().members.find((m) => m.id === props.memberID);
  const draft = () => props.draft;
  const setDraft = (text: string) => props.onDraft(text);
  const events = () => props.frame.preview?.member_id === props.memberID ? props.frame.preview.events
    : props.result?.operation === 'member_inspect' && props.result.result?.member_id === props.memberID ? props.result.result.events ?? [] : [];
  const asks = () => props.frame.preview?.member_id === props.memberID ? props.frame.preview.asks ?? [] : props.result?.operation === 'member_inspect' && props.result.result?.member_id === props.memberID ? props.result.result.asks ?? [] : [];
  const previewNote = () => props.frame.preview?.member_id === props.memberID ? props.frame.preview.note
    : props.result?.operation === 'member_inspect' && props.result.result?.member_id === props.memberID ? props.result.result.note : undefined;
  return <div style={{ height: '100%', 'min-height': 0, padding: `${tokens.spaceMd}px`, 'box-sizing': 'border-box' }}>
      <Show when={member()}>{(m) => (
        <section data-testid="workspace-member-detail" style={{ height: '100%', display: 'flex', 'flex-direction': 'column', 'min-height': 0 }}>
          <div style={{ 'max-height': '35%', overflow: 'auto', 'flex-shrink': 0 }}>
          <div style={heading}>{m().name} · {m().lifetime}</div>
          <Show when={m().launch_settings}>
            <p data-testid="workspace-member-launch" style={{ color: tokens.fgMuted, 'overflow-wrap': 'anywhere' }}>
              Launched: {[m().profile, m().provider, m().launch_settings?.model, m().launch_settings?.capability ? `capability ${m().launch_settings?.capability}` : "", m().launch_settings?.thinking ? `thinking ${m().launch_settings?.thinking}` : ''].filter(Boolean).join(' · ')}
            </p>
          </Show>
          <Show when={m().state === 'paused' || m().state === 'failed'}><Button onClick={() => props.onAction('member_resume', { member_id: m().id })}>Resume member</Button></Show>
          <Button disabled={m().state === 'ended'} onClick={() => props.onAction('member_open', { member_id: m().id })}>Open Agent window</Button>
          <For each={w().assignments.filter((a) => a.member_id === m().id)}>{(a) => <section><p>{a.text} · {a.state}</p><Show when={a.result}><p style={{ 'white-space': 'pre-wrap' }}>{a.result}</p></Show></section>}</For>
          <Show when={previewNote()}><p>{previewNote()}</p></Show>
          </div>
          <div style={{ flex: 1, 'min-height': 0 }}><AgentSession events={events} asks={asks} onAnswer={props.onAnswer} hideComposer /></div>
          <textarea aria-label={`Message ${m().name}`} value={draft()} onInput={(e) => setDraft(e.currentTarget.value)} style={{ width: '100%', 'box-sizing': 'border-box' }} />
          <Button disabled={!draft().trim() || m().state === 'ended'} onClick={() => { props.onAction('member_message', { recipient: m().id, body: draft() }); setDraft(''); }}>Send message</Button>
        </section>
      )}</Show>
  </div>;
};
