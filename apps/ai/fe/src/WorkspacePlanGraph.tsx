import type { agentproto } from '@wash/ui';
import { For, Show, createEffect, createMemo, createSignal } from 'solid-js';
import type { Component } from 'solid-js';
import { Markdown, tokens, agentActivityColor, agentActivityLabel } from '@wash/ui';
import { layoutPlan, LINE_H } from './plan-layout';

type Node = agentproto.Node;

/** The colour a node's state is drawn in; a state of the orchestrator's own is violet. */
export function planStateColor(state: string): string {
  switch (state) {
    case 'todo': return tokens.fgMuted;
    case 'active': return tokens.accentBlue;
    case 'reported': return tokens.accentAmber;
    case 'done': return tokens.accentGreen;
    case 'failed': return tokens.accentRed;
    default: return tokens.accentViolet;
  }
}

/** The ids from the top of the plan down to id: the breadcrumb a member tab shows. */
// plan is nullable as well as optional: Go marshals a nil slice as null, so
// the generated Workspace.plan is `Node[] | null`. The body already folds
// both to [].
export function planPath(plan: Node[] | null | undefined, id: string | undefined): Node[] {
  const out: Node[] = [];
  const byId = new Map((plan ?? []).map((n) => [n.id, n]));
  for (let cur = id ? byId.get(id) : undefined, guard = 0; cur && guard <= byId.size; cur = cur.parent ? byId.get(cur.parent) : undefined, guard++) out.unshift(cur);
  return out;
}

/**
 * The Plan tab: the plan as a graph, milestones as columns, each node with
 * its state, its members and what they are doing. A member opens its tab; a
 * node opens its detail below the graph.
 */
export const WorkspacePlanGraph: Component<{
  frame: agentproto.WorkspaceState;
  focus?: string;
  onSelect: (id: string) => void;
}> = (props) => {
  const w = () => props.frame.workspace!;
  const plan = () => w().plan ?? [];
  const [selected, setSelected] = createSignal('');
  let scroller!: HTMLDivElement;
  const membersOn = (id: string) => (w().members ?? []).filter((m) => m.node === id && m.state !== 'ended');
  const childrenOf = (id: string) => plan().filter((n) => n.parent === id);
  const activity = (m: agentproto.Member) => m.state !== 'available' ? m.state : props.frame.activity?.[m.id] ?? (m.waiting ? 'waiting-message' : 'idle');
  const needsYou = (id: string) => membersOn(id).some((m) => activity(m) === 'needs-input') || (props.frame.approvals ?? []).some((a) => membersOn(id).some((m) => m.id === a.member_id));
  const layout = createMemo(() => layoutPlan(plan(), (id) => membersOn(id).length + (needsYou(id) ? 1 : 0)));
  const byId = createMemo(() => new Map(plan().map((n) => [n.id, n])));
  // A breadcrumb in a member tab lands here with a node to show.
  createEffect(() => {
    const id = props.focus;
    if (!id) return;
    setSelected(id);
    const box = layout().boxes.find((b) => b.id === id);
    if (box && scroller) scroller.scrollTo?.({ left: Math.max(0, box.x - 40), top: Math.max(0, box.y - 40), behavior: 'smooth' });
  });
  const nested = (id: string, depth = 0): { node: Node; depth: number }[] =>
    childrenOf(id).flatMap((c) => [{ node: c, depth }, ...nested(c.id, depth + 1)]);
  const detail = createMemo(() => byId().get(selected()));
  const unmet = (n: Node) => (n.needs ?? []).filter((need) => {
    const dep = byId().get(need);
    if (dep) return dep.state !== 'done';
    const q = (w().qa ?? []).find((t) => t.id === need);
    return q ? q.state !== 'resolved' : false;
  });
  const pill = (state: string) => ({
    font: tokens.type.textSm, color: planStateColor(state), border: `1px solid ${planStateColor(state)}`,
    'border-radius': tokens.radiusSm, padding: `0 ${tokens.spaceXs}px`, 'white-space': 'nowrap' as const,
  });
  return <section data-testid="workspace-plan" style={{ height: '100%', display: 'flex', 'flex-direction': 'column', 'min-height': 0 }}>
    <Show when={plan().length} fallback={
      <p data-testid="workspace-plan-empty" style={{ padding: `${tokens.spaceMd}px`, color: tokens.fgMuted }}>
        No plan yet. The orchestrator adds nodes with plan_set; every assignment is on one.
      </p>
    }>
      <Show when={w().legend}>
        <div data-testid="workspace-plan-legend" style={{ padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`, color: tokens.fgMuted, font: tokens.type.textSm, 'border-bottom': `1px solid ${tokens.borderMenu}` }}>{w().legend}</div>
      </Show>
      <div ref={scroller} data-testid="workspace-plan-graph" style={{ flex: 1, 'min-height': 0, overflow: 'auto', position: 'relative' }}>
        <div style={{ position: 'relative', width: `${layout().width}px`, height: `${layout().height}px` }}>
          <svg aria-hidden="true" width={layout().width} height={layout().height} style={{ position: 'absolute', inset: 0, 'pointer-events': 'none' }}>
            <defs>
              <marker id="wash-plan-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto">
                <path d="M0,0 L8,4 L0,8 z" fill={tokens.fgDim} />
              </marker>
            </defs>
            <For each={layout().edges}>{(e) => (
              <path data-testid={`workspace-plan-edge-${e.from}-${e.to}`} d={e.d} fill="none" stroke={tokens.fgDim} stroke-width="1.5" marker-end="url(#wash-plan-arrow)" />
            )}</For>
          </svg>
          <For each={layout().boxes}>{(box) => {
            const node = () => byId().get(box.id)!;
            const group = box.kind === 'group';
            return <div data-wash-hit="subtle" data-testid={`workspace-plan-node-${box.id}`} data-state={node()?.state} data-selected={selected() === box.id ? 'true' : 'false'}
              role="button" tabIndex={0} aria-label={`${box.id} ${node()?.title}, ${node()?.state}`}
              onClick={(ev) => { ev.stopPropagation(); setSelected(box.id); }}
              onKeyDown={(ev) => { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); setSelected(box.id); } }}
              style={{
                position: 'absolute', left: `${box.x}px`, top: `${box.y}px`, width: `${box.w}px`, height: `${box.h}px`,
                'box-sizing': 'border-box', 'border-radius': tokens.radiusLg, cursor: 'pointer', overflow: 'hidden',
                border: `1px ${box.sketch ? 'dashed' : 'solid'} ${selected() === box.id ? tokens.borderFocus : tokens.borderMenu}`,
                'border-left': group ? undefined : `4px solid ${planStateColor(node()?.state ?? '')}`,
                background: group ? tokens.bgInset : tokens.bgWindow, opacity: box.sketch ? 0.75 : 1,
                padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`, color: tokens.fg,
              }}>
              <div style={{ display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'baseline', 'min-width': 0 }}>
                <span style={{ flex: 1, 'min-width': 0, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap', font: group ? tokens.type.titleSm : tokens.type.textMd, 'font-weight': 600 }}
                  title={`${node()?.id} · ${node()?.title}`}>
                  {node()?.emoji ? `${node()!.emoji} ` : ''}{node()?.id} · {node()?.title}
                </span>
                <span data-testid={`workspace-plan-state-${box.id}`} style={pill(node()?.state ?? '')}>{node()?.state}</span>
              </div>
              <Show when={box.sketch}><div style={{ color: tokens.fgMuted, font: tokens.type.textSm }}>Sketch: to be planned</div></Show>
              <Show when={!group}>
                <Show when={needsYou(box.id)}>
                  <div data-testid={`workspace-plan-needs-you-${box.id}`} style={{ height: `${LINE_H}px`, color: tokens.accentAmber, font: tokens.type.textSm, 'font-weight': 600 }}>● Needs you</div>
                </Show>
                <For each={membersOn(box.id)}>{(m) => (
                  <button data-wash-hit type="button" data-testid={`workspace-plan-member-${m.id}`}
                    onClick={(ev) => { ev.stopPropagation(); props.onSelect(m.id); }}
                    title={`Open ${m.name}`}
                    style={{ display: 'flex', gap: `${tokens.spaceXs}px`, 'align-items': 'center', width: '100%', height: `${LINE_H}px`, padding: 0, border: 'none', background: 'transparent', color: tokens.fg, font: tokens.type.textSm, cursor: 'pointer', 'text-align': 'left' }}>
                    <span aria-hidden="true" style={{ width: '7px', height: '7px', 'border-radius': '50%', background: agentActivityColor(activity(m)), 'flex-shrink': 0 }} />
                    <span style={{ overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>{m.emoji} {m.name} · {agentActivityLabel(activity(m))}{(activity(m) === 'background' || activity(m) === 'tool') && props.frame.activity_detail?.[m.id] ? `: ${props.frame.activity_detail[m.id]}` : ''}</span>
                  </button>
                )}</For>
                <For each={nested(box.id)}>{({ node: step, depth }) => (
                  <div data-testid={`workspace-plan-step-${step.id}`} style={{ height: `${LINE_H}px`, 'padding-left': `${depth * 10}px`, font: tokens.type.textSm, color: tokens.fgMuted, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
                    <span style={{ color: planStateColor(step.state) }}>▸</span> {step.emoji} {step.id} · {step.title} · {step.state}
                  </div>
                )}</For>
              </Show>
            </div>;
          }}</For>
        </div>
      </div>
      <Show when={detail()}>{(n) => (
        <section data-testid="workspace-plan-detail" style={{ 'max-height': '40%', overflow: 'auto', 'border-top': `1px solid ${tokens.borderMenu}`, padding: `${tokens.spaceMd}px`, 'overflow-wrap': 'anywhere' }}>
          <div style={{ display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'baseline' }}>
            <span style={{ font: tokens.type.titleSm }}>{n().emoji} {n().id} · {n().title}</span>
            <span style={pill(n().state)}>{n().state}</span>
            <Show when={n().template}><small style={{ color: tokens.fgMuted }}>{n().template}</small></Show>
          </div>
          <Show when={n().body}><Markdown text={n().body!} /></Show>
          <Show when={(n().needs ?? []).length}>
            <div style={{ color: tokens.fgMuted, font: tokens.type.textSm }}>
              Needs: <For each={n().needs ?? []}>{(need, i) => <>{i() ? ', ' : ''}<span style={{ color: unmet(n()).includes(need) ? tokens.accentAmber : tokens.accentGreen }}>{need}</span></>}</For>
            </div>
          </Show>
          <For each={n().overrides ?? []}>{(o) => <div data-testid="workspace-plan-override" style={{ color: tokens.accentAmber, font: tokens.type.textSm }}>Override: {o}</div>}</For>
          <For each={(w().assignments ?? []).filter((a) => a.node === n().id)}>{(a) => (
            <div style={{ font: tokens.type.textSm, padding: `${tokens.spaceXs}px 0` }}>
              <button data-wash-hit type="button" onClick={() => props.onSelect(a.member_id)} style={{ border: 'none', background: 'transparent', color: tokens.fgInfo, padding: 0, cursor: 'pointer', font: 'inherit' }}>
                {(w().members ?? []).find((m) => m.id === a.member_id)?.name ?? a.member_id}
              </button> · {a.state} · {a.text.split('\n')[0].slice(0, 120)}
            </div>
          )}</For>
          <For each={(w().qa ?? []).filter((q) => q.node === n().id)}>{(q) => (
            <button data-wash-hit type="button" data-testid={`workspace-plan-thread-${q.id}`} onClick={() => props.onSelect(`qa:${q.id}`)}
              style={{ display: 'block', border: 'none', background: 'transparent', color: tokens.fgInfo, padding: `${tokens.spaceXs}px 0`, cursor: 'pointer', font: tokens.type.textSm, 'text-align': 'left' }}>
              QA {q.id} · {q.state} · {q.title}
            </button>
          )}</For>
        </section>
      )}</Show>
    </Show>
  </section>;
};

