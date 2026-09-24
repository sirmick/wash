import { For, Show, createMemo, createSignal } from 'solid-js';
import type { Component } from 'solid-js';
import { Button, Markdown, tokens, agentActivityLabel, agentActivityColor, agentActivityPulses } from '@wash/ui';
import type { AgentEvent, AgentAsk } from '@wash/ui';

export interface WorkspaceItem { id: string; text: string; emoji?: string; state: string; revision: number }
export interface WorkspaceProfile { capability?: string; provider: string; model?: string; thinking?: string; configs?: Record<string, string> }
export interface WorkspaceUsage { used: number; size: number }
export interface WorkspaceMember {
  usage?: WorkspaceUsage;
  profile?: string; launch_settings?: WorkspaceProfile; initial_configs?: Record<string, string>;
  id: string; name: string; provider: string; lifetime: string; state: string;
  package?: string; role?: string;
  status?: string; emoji?: string; waiting?: string; session_id: string;
}
export interface WorkspaceMessage {
  id: string; sender: string; recipient: string; type: string; body: string;
  delivery: string; assignment_id?: string;
}
export interface QAThread { id: string; package: string; title: string; assignee: string; state: string; blocking: boolean; revision: number }
export interface WorkspaceState {
  qa?: QAThread[];
  qa_document?: {path: string; title?: string};
  profiles?: Record<string, WorkspaceProfile>; default_profile?: string;
  packages?: Record<string, { title: string }>;
  id: string; name: string; state: string; revision: number; orchestrator: string;
  items: WorkspaceItem[]; members: WorkspaceMember[]; messages: WorkspaceMessage[];
  document?: { path: string; title?: string };
  assignments: { id: string; member_id: string; text: string; state: string; result?: string }[];
}
export interface WorkspaceFrame {
  sequence?: number;
  qa_markdown?: string;
  qa_document_status?: {path?: string; state: string; error?: string; saved_revision?: number};
  approvals?: (AgentAsk & {member_id: string})[];
  preview?: { member_id: string; events: AgentEvent[]; asks?: AgentAsk[]; note?: string };
  workspace: WorkspaceState | null;
  activity?: Record<string, string>;
  activity_detail?: Record<string, string>;
  usage?: Record<string, WorkspaceUsage>;
  document_text?: string;
  document_error?: string;
}
export interface WorkspaceResult {
  operation: string;
  result?: { member_id?: string; events?: AgentEvent[]; asks?: AgentAsk[]; note?: string };
  error?: string;
}

export const WorkspaceSidebar: Component<{
  frame: WorkspaceFrame;
  result?: WorkspaceResult;
  selected: string;
  onSelect: (id: string) => void;
  onAction: (name: string, args: Record<string, unknown>) => void;
}> = (props) => {
  const [answers, setAnswers] = createSignal<Record<string, string>>({});
  const w = () => props.frame.workspace!;
  const questions = createMemo(() => w().messages.filter((m) => m.type === 'decision_request' && m.delivery === 'recorded'));
  const needsAttention = () => (props.frame.approvals?.length ?? 0) + questions().length + (w().qa ?? []).filter(q => q.state === 'awaiting-owner').length + (props.frame.qa_document_status?.state === 'error' ? 1 : 0);
  const label = (id: string) => id === 'human' ? 'You' : w().members.find((m) => m.id === id)?.name ?? id;
  const activity = (m: WorkspaceMember) => m.state !== 'available' ? m.state : props.frame.activity?.[m.id] ?? (m.waiting ? 'waiting-message' : 'idle');
  const usage = (m: WorkspaceMember) => props.frame.usage?.[m.id] ?? m.usage;
  const count = (n: number) => n.toLocaleString('en-US');
  // "CT1 · Console input-flood test" where the orchestrator named it, the
  // bare code otherwise: a code alone is what made the sidebar cryptic.
  const pkg = (code: string) => { const t = w().packages?.[code]?.title; return t ? `${code} · ${t}` : code; };
  // Two levels: members with no package (orchestrator, architect) first, then
  // one group per package in first-seen order. Groups are keyed by code
  // STRINGS and rows are the members' own objects, so a frame update keeps
  // the rendered rows (a live activity dot) rather than rebuilding them.
  const teamCodes = createMemo(() => {
    const order: string[] = [];
    for (const m of w().members) {
      const code = m.package ?? '';
      if (!order.includes(code)) order.push(code);
    }
    return order.sort((a, b) => (a === '' ? -1 : b === '' ? 1 : 0));
  }, undefined, { equals: (a, b) => a.length === b.length && a.every((c, i) => c === b[i]) });
  const membersOf = (code: string) => w().members.filter((m) => (m.package ?? '') === code);
  const heading = { font: tokens.type.titleSm, padding: `${tokens.spaceSm}px 0` };
  return (
    <aside data-testid="workspace-sidebar" aria-label="Agent workspace" style={{
      'min-width': 0, width: '100%', 'overflow-wrap': 'anywhere',
      overflow: 'auto', 'overscroll-behavior': 'contain', 'min-height': 0,
      'border-left': `1px solid ${tokens.borderMenu}`, background: tokens.bgInset,
      padding: `${tokens.spaceMd}px`, 'box-sizing': 'border-box',
    }}>
      <style>{`
        @keyframes wash-workspace-pulse { 0%, 100% { opacity: 1; } 50% { opacity: .35; } }
        .wash-workspace-activity[data-pulse="true"] { animation: wash-workspace-pulse 1.6s ease-in-out infinite; }
        @media (prefers-reduced-motion: reduce) { .wash-workspace-activity[data-pulse="true"] { animation: none; } }
      `}</style>
      <div style={heading}>{w().name} <Show when={w().state !== 'active'}>· {w().state}</Show></div>
      <Show when={props.result?.error}><div role="alert">{props.result?.error}</div></Show>
      <Show when={needsAttention() > 0}>
        <section data-testid="workspace-attention" aria-label="Needs you">
          <div style={heading}>Needs you</div>
          <Show when={props.frame.qa_document_status?.state === 'error'}>
            <button data-wash-hit type="button" role="alert" data-testid="workspace-qa-save-error" onClick={() => props.onSelect('qa')} style={{color:tokens.accentRed, 'text-align':'left'}}>
              QA Markdown could not be saved: {props.frame.qa_document_status?.error}
            </button>
          </Show>
          <For each={props.frame.approvals ?? []}>{ask => (
            <Button data-testid={`workspace-approval-${ask.id}`} onClick={() => props.onSelect(ask.member_id)}>
              {label(ask.member_id)} · Approval needed: {ask.tool} {ask.subject}
            </Button>
          )}</For>
          <For each={(w().qa ?? []).filter(q => q.state === 'awaiting-owner')}>{q => (
            <Button onClick={() => props.onSelect(`qa:${q.id}`)}>{pkg(q.package)} · Owner question: {q.title}</Button>
          )}</For>
      <For each={questions()}>{(q) => (
        <section data-testid="workspace-decision" style={{ padding: `${tokens.spaceSm}px 0` }}>
          <div style={heading}>{label(q.sender)} needs your decision</div>
          <Markdown text={q.body} />
          <textarea aria-label="Decision response" value={answers()[q.id] ?? ''} onInput={(e) => setAnswers({ ...answers(), [q.id]: e.currentTarget.value })} style={{ width: '100%', 'box-sizing': 'border-box' }} />
          <Button disabled={!answers()[q.id]?.trim()} onClick={() => props.onAction('decision_response', { id: q.id, body: answers()[q.id] })}>Answer</Button>
        </section>
      )}</For>
        </section>
      </Show>
      <Button data-testid="workspace-plan-link" onClick={() => props.onSelect('plan')}>
        Plan · {w().items.filter((item) => item.state === 'done').length}/{w().items.length}
      </Button>
      <Show when={w().document}>
        <Button onClick={() => props.onSelect('plan')}>{w().document?.title || 'Project document'}</Button>
      </Show>
      <Button data-testid="workspace-qa-link" onClick={() => props.onSelect('qa')}>
        Questions · {(w().qa ?? []).filter(q => q.state !== 'resolved').length} open
      </Button>
      <For each={(w().qa ?? []).filter(q => q.state !== 'resolved')}>{q => (
        <button data-wash-hit type="button" data-testid={`workspace-question-${q.id}`} onClick={() => props.onSelect(`qa:${q.id}`)} style={{ display: 'block', width: '100%', 'text-align': 'left', background: 'transparent', color: tokens.fg, border: 'none', padding: `${tokens.spaceSm}px` }}>
          {q.blocking ? '⏳ ' : ''}{q.title}
          <small style={{ display: 'block', color: tokens.fgMuted }}>{pkg(q.package)}</small>
          <small style={{ display: 'block', color: tokens.fgMuted }}>{q.state} · {label(q.assignee)}</small>
        </button>
      )}</For>
      <div style={heading}>Team</div>
      <For each={teamCodes()}>{(code) => (
        <section data-testid={`workspace-team-${code || 'coordination'}`} aria-label={code ? pkg(code) : 'Coordination'}
          style={{ 'margin-left': code ? `${tokens.spaceMd}px` : '0' }}>
          <Show when={code}>
            <div style={{ font: tokens.type.textMd, 'font-weight': 600, padding: `${tokens.spaceSm}px 0 0`, 'margin-left': `-${tokens.spaceMd}px` }}>{pkg(code)}</div>
          </Show>
          <For each={membersOf(code)}>{(m) => (
        <button data-wash-hit type="button" data-testid={`workspace-member-${m.id}`} onClick={() => props.onSelect(m.id)} style={{
          display: 'block', width: '100%', 'text-align': 'left', padding: `${tokens.spaceSm}px`,
          background: props.selected === m.id ? tokens.bgWindow : 'transparent', color: tokens.fg,
          border: `1px solid ${props.selected === m.id ? tokens.borderMenu : 'transparent'}`, cursor: 'pointer',
        }}>
          <div style={{ display: 'flex', 'align-items': 'center', gap: `${tokens.spaceSm}px` }}>
            <span aria-hidden="true" class="wash-workspace-activity" data-testid={`workspace-activity-${m.id}`} data-activity={activity(m)} data-pulse={agentActivityPulses(activity(m)) ? 'true' : 'false'} style={{
              display: 'inline-block', width: '8px', height: '8px', 'border-radius': '50%', 'flex-shrink': 0, background: agentActivityColor(activity(m)),
            }} />
            <span>{m.emoji} {m.name}</span>
          </div>
          <small data-testid={`workspace-activity-label-${m.id}`} style={{ color: agentActivityColor(activity(m)) }}>{agentActivityLabel(activity(m))}</small>
          <Show when={props.frame.activity_detail?.[m.id] && activity(m) === 'tool'}>
            <div title={props.frame.activity_detail?.[m.id]} style={{ overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>{props.frame.activity_detail?.[m.id]}</div>
          </Show>
          <div data-testid={`workspace-usage-${m.id}`} style={{ color: tokens.fgMuted, 'font-variant-numeric': 'tabular-nums' }}>
            <Show when={usage(m)} fallback={<small>Context tokens not reported</small>}>{(u) => (
              <small title="Provider-reported context usage, not cumulative or billed tokens">
                Context: {count(u().used)}<Show when={u().size > 0}> / {count(u().size)}</Show> tokens
              </small>
            )}</Show>
          </div>
          <Show when={m.status}><div>{m.status}</div></Show>
          <Show when={m.waiting}><small style={{ color: tokens.fgMuted }}>{m.waiting}</small></Show>
        </button>
          )}</For>
        </section>
      )}</For>
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
