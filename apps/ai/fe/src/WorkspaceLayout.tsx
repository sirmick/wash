import { For, Show, createEffect, createMemo, createSignal, createUniqueId, on, untrack } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { Splitter, Tab, tokens } from '@wash/ui';
import { WorkspaceSidebar, type WorkspaceFrame, type WorkspaceResult } from './WorkspaceSidebar';
import { WorkspaceMemberPanel, WorkspacePlan, type WorkspaceAction } from './WorkspacePanels';

const MIN_SPLIT = 35;
const MAX_SPLIT = 85;
const SPLIT_KEY = 'wash.agent.workspace.split';
const clamp = (value: number) => Math.max(MIN_SPLIT, Math.min(MAX_SPLIT, value));
const savedSplit = () => {
  try {
    const saved = Number(localStorage.getItem(SPLIT_KEY) ?? 70);
    return Number.isFinite(saved) ? clamp(saved) : 70;
  } catch { return 70; }
};

/** Navigation never replaces the owning session: keeping it mounted preserves
 * its draft, attachments, pending questions and transcript across workspace tabs. */
export const WorkspaceLayout: Component<{
  frame: WorkspaceFrame; result?: WorkspaceResult; currentSessionID?: string;
  onAction: WorkspaceAction; children: JSX.Element;
}> = (props) => {
  let container!: HTMLDivElement;
  let tablist!: HTMLDivElement;
  const prefix = createUniqueId();
  const [split, setSplit] = createSignal(savedSplit());
  const [opened, setOpened] = createSignal<string[]>([]);
  const [active, setActive] = createSignal('conversation');
  const [drafts, setDrafts] = createSignal<Record<string, string>>({});
  const workspace = () => props.frame.workspace;
  const ownMember = createMemo(() => workspace()?.members.find((m) => m.session_id === props.currentSessionID)?.id);
  const tabs = () => ['conversation', ...opened()];
  const label = (id: string) => id === 'conversation' ? 'Conversation' : id === 'plan' ? 'Plan'
    : workspace()?.members.find((m) => m.id === id)?.name ?? id;
  const select = (id: string) => {
    if (id === ownMember()) id = 'conversation';
    if (id !== 'conversation' && !opened().includes(id)) setOpened([...opened(), id]);
    setActive(id);
  };
  const close = (id: string) => {
    if (id === 'conversation') return;
    const index = tabs().indexOf(id);
    if (active() === id) setActive(tabs()[index - 1] ?? 'conversation');
    setOpened(opened().filter((tab) => tab !== id));
  };
  const focusActive = () => tablist?.querySelector<HTMLButtonElement>('[aria-selected="true"]')?.focus();
  const persistSplit = () => {
    try { localStorage.setItem(SPLIT_KEY, String(split())); } catch { /* storage can be disabled */ }
  };
  const workspaceID = createMemo(() => workspace()?.id);
  createEffect(on(workspaceID, () => {
    setOpened([]);
    setActive('conversation');
    setDrafts({});
  }));
  // Membership may change in a snapshot; retired members remain navigable.
  createEffect(() => {
    const members = workspace()?.members ?? [];
    const own = ownMember();
    untrack(() => {
      const valid = opened().filter((id) => id === 'plan' || members.some((m) => m.id === id && id !== own));
      if (valid.length !== opened().length) setOpened(valid);
      if (active() !== 'conversation' && !valid.includes(active())) setActive('conversation');
    });
  });
  const inspected = createMemo(() => workspace() && active() !== 'conversation' && active() !== 'plan' ? active() : '');
  createEffect(on([workspaceID, inspected], ([id, memberID]) => {
    // Only the visible member streams events. Also clear any server-side
    // preview restored on reload, when this window starts at Conversation.
    if (id) props.onAction('member_inspect', { member_id: memberID });
  }));
  return <div ref={container} data-testid="workspace-layout" style={{
    flex: 1, 'min-width': 0, 'min-height': 0, display: 'grid', overflow: 'hidden',
    'grid-template-rows': 'minmax(0, 1fr)',
    'grid-template-columns': workspace() ? `minmax(0, ${split()}fr) 5px minmax(0, ${100 - split()}fr)` : 'minmax(0, 1fr)',
  }}>
    <div data-testid="workspace-main" style={{ 'min-width': 0, 'min-height': 0, display: 'flex', 'flex-direction': 'column' }}>
      <Show when={workspace()}>
        <div ref={tablist} role="tablist" aria-label="Workspace views" style={{ display: 'flex', overflow: 'auto', 'flex-shrink': 0, background: tokens.bgMenu }}>
          <For each={tabs()}>{(id) => <Tab role="tab" id={`${prefix}-tab-${id}`} aria-controls={`${prefix}-panel-${id}`}
            aria-selected={active() === id} tabIndex={active() === id ? 0 : -1} active={active() === id}
            onClick={() => select(id)} onClose={id === 'conversation' ? undefined : () => { close(id); focusActive(); }}
            closeTitle={`Close ${label(id)}`} closeTestId={`workspace-close-${id}`}
            onKeyDown={(event) => {
              const keys = ['ArrowLeft', 'ArrowRight', 'Home', 'End', 'Delete'];
              if (!keys.includes(event.key)) return;
              event.preventDefault();
              const all = tabs();
              if (event.key === 'Delete') close(id);
              else if (event.key === 'Home') select(all[0]);
              else if (event.key === 'End') select(all[all.length - 1]);
              else select(all[(all.indexOf(id) + (event.key === 'ArrowRight' ? 1 : all.length - 1)) % all.length]);
              focusActive();
            }}>{label(id)}</Tab>}</For>
        </div>
      </Show>
      <div role={workspace() ? 'tabpanel' : undefined} id={`${prefix}-panel-conversation`}
        aria-labelledby={workspace() ? `${prefix}-tab-conversation` : undefined}
        style={{ flex: 1, 'min-height': 0, display: active() === 'conversation' ? 'flex' : 'none', 'flex-direction': 'column' }}>
        {props.children}
      </div>
      <Show when={workspace() && active() !== 'conversation'}>
        <div role="tabpanel" id={`${prefix}-panel-${active()}`} aria-labelledby={`${prefix}-tab-${active()}`}
          style={{ flex: 1, 'min-height': 0, overflow: 'hidden' }}>
          <Show when={active() === 'plan'} fallback={
            <WorkspaceMemberPanel frame={props.frame} result={props.result} memberID={active()}
              draft={drafts()[active()] ?? ''} onDraft={(text) => setDrafts({ ...drafts(), [active()]: text })} onAction={props.onAction} />
          }><WorkspacePlan frame={props.frame} /></Show>
        </div>
      </Show>
    </div>
    <Show when={workspace()}>
      <div role="separator" aria-label="Resize workspace sidebar" aria-orientation="vertical" tabIndex={0}
        aria-valuemin={MIN_SPLIT} aria-valuemax={MAX_SPLIT} aria-valuenow={Math.round(split())}
        aria-valuetext={`${Math.round(100 - split())}% sidebar width`}
        data-testid="workspace-splitter" onKeyDown={(event) => {
          if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
          event.preventDefault();
          setSplit(event.key === 'Home' ? MIN_SPLIT : event.key === 'End' ? MAX_SPLIT : clamp(split() + (event.key === 'ArrowLeft' ? -2 : 2)));
          persistSplit();
        }}>
        <Splitter container={container} thickness={5} min={MIN_SPLIT} max={MAX_SPLIT} onChange={setSplit} onCommit={persistSplit} />
      </div>
      <WorkspaceSidebar frame={props.frame} result={props.result} selected={active() === 'conversation' ? ownMember() ?? '' : active()}
        onSelect={select} onAction={props.onAction} />
    </Show>
  </div>;
};
