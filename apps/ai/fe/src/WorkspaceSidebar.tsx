import { For, Show, createEffect, createMemo, createSignal, on } from 'solid-js';
import type { Component } from 'solid-js';
import { AgentSession, Button, Markdown, tokens } from '@wash/ui';
import type { AgentEvent } from '@wash/ui';

export interface WorkspaceItem { id: string; text: string; emoji?: string; state: string; revision: number }
export interface WorkspaceMember {
  id: string; name: string; provider: string; lifetime: string; state: string;
  status?: string; emoji?: string; waiting?: string; session_id: string;
}
export interface WorkspaceMessage {
  id: string; sender: string; recipient: string; type: string; body: string;
  delivery: string; assignment_id?: string;
}
export interface WorkspaceState {
  id: string; name: string; state: string; revision: number; orchestrator: string;
  items: WorkspaceItem[]; members: WorkspaceMember[]; messages: WorkspaceMessage[];
  document?: { path: string; title?: string };
  assignments: { id: string; member_id: string; text: string; state: string; result?: string }[];
}
export interface WorkspaceFrame {
  sequence?: number;
  preview?: { member_id: string; events: AgentEvent[]; note?: string };
  workspace: WorkspaceState | null;
  activity?: Record<string, string>;
  document_text?: string;
  document_error?: string;
}
export interface WorkspaceResult {
  operation: string;
  result?: { member_id?: string; events?: AgentEvent[]; note?: string };
  error?: string;
}

export const WorkspaceSidebar: Component<{
  frame: WorkspaceFrame;
  result?: WorkspaceResult;
  onAction: (name: string, args: Record<string, unknown>) => void;
}> = (props) => {
  const [selected, setSelected] = createSignal('');
  const [draft, setDraft] = createSignal('');
  const [answers, setAnswers] = createSignal<Record<string, string>>({});
  const w = () => props.frame.workspace!;
  const member = createMemo(() => w().members.find((m) => m.id === selected()));
  const questions = createMemo(() => w().messages.filter((m) => m.type === 'decision_request' && m.delivery === 'recorded'));
  const label = (id: string) => id === 'human' ? 'You' : w().members.find((m) => m.id === id)?.name ?? id;
  createEffect(on(selected, (id) => {
    // Subscribe to a read-only preview; this never claims the member's
    // controller or changes the main conversation.
    props.onAction('member_inspect', { member_id: member() ? id : '' });
  }));
  const events = () => props.frame.preview?.member_id === selected() ? props.frame.preview.events : props.result?.operation === 'member_inspect' && props.result.result?.member_id === selected()
    ? props.result.result.events ?? [] : [];
  const previewNote = () => props.frame.preview?.member_id === selected() ? props.frame.preview.note
    : props.result?.result?.member_id === selected() ? props.result.result.note : undefined;
  const heading = { font: tokens.type.titleSm, padding: `${tokens.spaceSm}px 0` };
  return (
    <aside data-testid="workspace-sidebar" aria-label="Agent workspace" style={{
      width: '340px', 'min-width': '250px', 'max-width': '65%', resize: 'horizontal',
      overflow: 'auto', 'overscroll-behavior': 'contain', 'min-height': 0,
      'border-left': `1px solid ${tokens.borderMenu}`, background: tokens.bgInset,
      padding: `${tokens.spaceMd}px`, 'box-sizing': 'border-box',
    }}>
      <div style={heading}>{w().name} <Show when={w().state !== 'active'}>· {w().state}</Show></div>
      <Show when={props.result?.error}><div role="alert">{props.result?.error}</div></Show>
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
        <Button onClick={() => setSelected(selected() === 'document' ? '' : 'document')}>
          {w().document?.title || 'Project document'}
        </Button>
      </Show>
      <div style={heading}>Team</div>
      <For each={w().members}>{(m) => (
        <button data-wash-hit type="button" data-testid={`workspace-member-${m.id}`} onClick={() => { setSelected(m.id); setDraft(''); }} style={{
          display: 'block', width: '100%', 'text-align': 'left', padding: `${tokens.spaceSm}px`,
          background: selected() === m.id ? tokens.bgWindow : 'transparent', color: tokens.fg,
          border: `1px solid ${selected() === m.id ? tokens.borderMenu : 'transparent'}`, cursor: 'pointer',
        }}>
          <div>{m.emoji} {m.name} <small>· {m.state === 'available' ? props.frame.activity?.[m.id] ?? 'idle' : m.state}</small></div>
          <Show when={m.status}><div>{m.status}</div></Show>
          <Show when={m.waiting}><small style={{ color: tokens.fgMuted }}>{m.waiting}</small></Show>
        </button>
      )}</For>
      <For each={questions()}>{(q) => (
        <section data-testid="workspace-decision" style={{ padding: `${tokens.spaceSm}px 0` }}>
          <div style={heading}>{label(q.sender)} needs your decision</div>
          <Markdown text={q.body} />
          <textarea aria-label="Decision response" value={answers()[q.id] ?? ''} onInput={(e) => setAnswers({ ...answers(), [q.id]: e.currentTarget.value })} style={{ width: '100%', 'box-sizing': 'border-box' }} />
          <Button disabled={!answers()[q.id]?.trim()} onClick={() => props.onAction('decision_response', { id: q.id, body: answers()[q.id] })}>Answer</Button>
        </section>
      )}</For>
      <Show when={selected() === 'document' && w().document}>
        <section data-testid="workspace-document" style={{ 'overflow-wrap': 'anywhere' }}>
          <div style={heading}>{w().document?.title || w().document?.path}</div>
          <Show when={!props.frame.document_error} fallback={<div role="alert">{props.frame.document_error}</div>}>
            <Markdown text={props.frame.document_text ?? ''} />
          </Show>
        </section>
      </Show>
      <Show when={member()}>{(m) => (
        <section data-testid="workspace-member-detail">
          <div style={heading}>{m().name} · {m().lifetime}</div>
          <Show when={m().state === 'paused' || m().state === 'failed'}><Button onClick={() => props.onAction('member_resume', { member_id: m().id })}>Resume member</Button></Show>
          <Button disabled={m().state === 'ended'} onClick={() => props.onAction('member_open', { member_id: m().id })}>Open Agent window</Button>
          <For each={w().assignments.filter((a) => a.member_id === m().id)}>{(a) => <section><p>{a.text} · {a.state}</p><Show when={a.result}><p style={{ 'white-space': 'pre-wrap' }}>{a.result}</p></Show></section>}</For>
          <Show when={previewNote()}><p>{previewNote()}</p></Show>
          <div style={{ height: '300px', 'min-height': 0 }}><AgentSession events={events} /></div>
          <textarea aria-label={`Message ${m().name}`} value={draft()} onInput={(e) => setDraft(e.currentTarget.value)} style={{ width: '100%', 'box-sizing': 'border-box' }} />
          <Button disabled={!draft().trim() || m().state === 'ended'} onClick={() => { props.onAction('member_message', { recipient: m().id, body: draft() }); setDraft(''); }}>Send message</Button>
        </section>
      )}</Show>
      <details>
        <summary data-wash-hit style={heading}>Messages</summary>
        <For each={w().messages.slice(-80)}>{(msg) => (
          <div style={{ padding: `${tokens.spaceSm}px 0`, 'overflow-wrap': 'anywhere' }}>
            <small>{label(msg.sender)} → {label(msg.recipient)} · {msg.type} · {msg.delivery}</small>
            <div style={{ 'white-space': 'pre-wrap' }}>{msg.body}</div>
          </div>
        )}</For>
      </details>
    </aside>
  );
};
