import type { agentproto } from '@wash/ui';
import { For, Show, createEffect, createMemo, createSignal } from 'solid-js';
import type { Component } from 'solid-js';
import { Markdown, tokens, agentActivityColor, agentActivityLabel } from '@wash/ui';
import { layoutPlan, LEVEL_HEAD, LINE_H } from './plan-layout';
import type { PlanBox } from './plan-layout';
import { frontierText, planFrontier } from './plan-frontier';

type Node = agentproto.Node;

/** Whether the graph shows done work in full; one setting for every workspace. */
const SHOW_DONE_KEY = 'wash.agent.plan.showDone';
/** How opaque a box is: the standing edges beneath show through faintly. */
const BOX_OPACITY = '90%';

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

/**
 * The commit hashes a node's body names, in order, once each: 7–40 hex
 * characters with at least one letter and one digit, so a date or a plain
 * word is not one.
 */
export function bodyCommits(body: string | undefined): string[] {
  const out: string[] = [];
  for (const m of (body ?? '').matchAll(/\b(?=[0-9a-f]*[a-f])(?=[0-9a-f]*\d)[0-9a-f]{7,40}\b/g)) if (!out.includes(m[0])) out.push(m[0]);
  return out;
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
 * its state, its members and what they are doing. Inside a milestone the
 * nodes sit in boxes by dependency depth; pick (click or hover) a node and
 * its own needs are drawn — what it waits on in amber, what waits on it in
 * blue — with everything else faded. Done work is folded away unless asked
 * for. A member opens its tab; a node opens its detail below the graph.
 */
export const WorkspacePlanGraph: Component<{
  frame: agentproto.WorkspaceState;
  focus?: string;
  onSelect: (id: string) => void;
}> = (props) => {
  const w = () => props.frame.workspace!;
  const plan = () => w().plan ?? [];
  const [selected, setSelected] = createSignal('');
  const [hover, setHover] = createSignal('');
  const [showDone, setShowDone] = createSignal(localStorage.getItem(SHOW_DONE_KEY) === '1');
  const toggleDone = (on: boolean) => { setShowDone(on); try { localStorage.setItem(SHOW_DONE_KEY, on ? '1' : '0'); } catch { /* private mode: the setting lasts the session */ } };
  let scroller!: HTMLDivElement;
  const membersOn = (id: string) => (w().members ?? []).filter((m) => m.node === id && m.state !== 'ended');
  const childrenOf = (id: string) => plan().filter((n) => n.parent === id);
  const activity = (m: agentproto.Member) => m.state !== 'available' ? m.state : props.frame.activity?.[m.id] ?? (m.waiting ? 'waiting-message' : 'idle');
  const needsYou = (id: string) => membersOn(id).some((m) => activity(m) === 'needs-input') || (props.frame.approvals ?? []).some((a) => membersOn(id).some((m) => m.id === a.member_id));
  const layout = createMemo(() => layoutPlan(plan(), (id) => membersOn(id).length + (needsYou(id) ? 1 : 0), { collapseDone: !showDone() }));
  const byId = createMemo(() => new Map(plan().map((n) => [n.id, n])));
  // How wide the work ahead is: when it narrows to one or two nodes a
  // wave, the team has nothing to fan out to, and the toolbar says so.
  const frontier = createMemo(() => planFrontier(plan(), (need) => (w().qa ?? []).some((q) => q.id === need && q.state !== 'resolved')));
  // The selected node's own needs, and the boxes at their ends: the
  // picture a click pins, which fades the rest. Hovering another node adds
  // its needs lightly, over the pinned picture, and takes nothing away.
  const edgesOf = (id: string) => id ? layout().needEdges.filter((e) => e.from === id || e.to === id) : [];
  const picked = () => selected() || hover();
  const own = createMemo(() => edgesOf(picked()));
  const hovered = createMemo(() => hover() && hover() !== picked() ? edgesOf(hover()) : []);
  const related = createMemo(() => {
    const out = new Set<string>();
    const p = picked();
    if (!p) return out;
    out.add(p);
    for (const e of own()) { out.add(e.from); out.add(e.to); }
    for (const e of hovered()) { out.add(e.from); out.add(e.to); }
    if (hover()) out.add(hover());
    return out;
  });
  // Containers (levels, open milestones) never fade: they are the frame the picked node's edges are read against.
  const faded = (box: PlanBox) => related().size > 0 && !related().has(box.id) && (box.kind === 'node' || box.kind === 'done' || (box.kind === 'group' && (box.sketch || box.collapsed)));
  // A breadcrumb in a member tab lands here with a node to show; a folded
  // one is unfolded first.
  createEffect(() => {
    const id = props.focus;
    if (!id) return;
    setSelected(id);
    const box = layout().boxes.find((b) => b.id === id);
    if (!box && !showDone() && byId().get(id)) { toggleDone(true); return; }
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
  const hoverable = (id: string) => ({ onMouseEnter: () => setHover(id), onMouseLeave: () => setHover('') });
  return <section data-testid="workspace-plan" style={{ height: '100%', display: 'flex', 'flex-direction': 'column', 'min-height': 0 }}>
    <Show when={plan().length} fallback={
      <p data-testid="workspace-plan-empty" style={{ padding: `${tokens.spaceMd}px`, color: tokens.fgMuted }}>
        No plan yet. The orchestrator adds nodes with plan_set; every assignment is on one.
      </p>
    }>
      <div data-testid="workspace-plan-toolbar" style={{ display: 'flex', gap: `${tokens.spaceMd}px`, 'align-items': 'baseline', padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`, color: tokens.fgMuted, font: tokens.type.textSm, 'border-bottom': `1px solid ${tokens.borderMenu}` }}>
        <Show when={w().phase === 'closing'}>
          <span data-testid="workspace-plan-phase" title="The team is folding back to one agent: members without open work hand off, new members launch only as replacements, and workspace_get view=dossier is the handover."
            style={{ font: tokens.type.textSm, color: tokens.accentAmber, border: `1px solid ${tokens.accentAmber}`, 'border-radius': tokens.radiusSm, padding: `0 ${tokens.spaceXs}px`, 'white-space': 'nowrap' }}>Closing</span>
        </Show>
        <span data-testid="workspace-plan-legend" style={{ flex: 1, 'min-width': 0, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }} title={w().legend ?? ''}>{w().legend ?? ''}</span>
        <span data-testid="workspace-plan-frontier" title="Nodes unblocked now, then what each wave of completions unblocks. One or two a wave for the rest of the way is a tail one agent closes faster than a team."
          style={{ 'white-space': 'nowrap', color: frontier().narrow ? tokens.accentAmber : tokens.fgMuted, 'font-weight': frontier().narrow ? 600 : undefined }}>
          {frontierText(frontier())}
        </span>
        <span data-testid="workspace-plan-hint" style={{ 'white-space': 'nowrap' }}>
          {picked() ? `${picked()}: ${own().filter((e) => e.to === picked()).length} needed · ${own().filter((e) => e.from === picked()).length} waiting on it${selected() ? '' : ' · click to pin'}` : 'Pick a node to see its needs'}
        </span>
        <label data-wash-hit style={{ display: 'flex', gap: `${tokens.spaceXs}px`, 'align-items': 'center', 'white-space': 'nowrap', cursor: 'pointer' }}>
          <input type="checkbox" data-testid="workspace-plan-show-done" checked={showDone()} onChange={(e) => toggleDone(e.currentTarget.checked)} />
          Show done
        </label>
      </div>
      <div ref={scroller} data-testid="workspace-plan-graph" data-wash-no-hit style={{ flex: 1, 'min-height': 0, overflow: 'auto', position: 'relative' }} onClick={() => setSelected('')}>
        <div style={{ position: 'relative', width: `${layout().width}px`, height: `${layout().height}px` }}>
          {/* The standing edges go under the boxes, which are nearly
              opaque: a line passing beneath a box shows as a faint ghost
              rather than running over its text. The picked node's own
              edges are drawn above everything, in the second svg. */}
          <svg aria-hidden="true" width={layout().width} height={layout().height} style={{ position: 'absolute', inset: 0, 'pointer-events': 'none' }}>
            <defs>
              <marker id="wash-plan-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto">
                <path d="M0,0 L8,4 L0,8 z" fill={tokens.fgDim} />
              </marker>
            </defs>
            <For each={layout().edges}>{(e) => (
              <path data-testid={`workspace-plan-edge-${e.from}-${e.to}`} d={e.d} fill="none" stroke={tokens.fgDim} stroke-width="1.5" marker-end="url(#wash-plan-arrow)" opacity={related().size ? 0.35 : 1} />
            )}</For>
          </svg>
          <For each={layout().boxes}>{(box) => {
            if (box.kind === 'level') {
              // A dependency depth inside a milestone: the nodes that need
              // nothing of their siblings, then those that need them, …
              const label = box.level === 0 ? 'No dependencies' : `Depth ${box.level} · needs the level before`;
              return <div data-testid={`workspace-plan-level-${box.id}`} aria-label={`${label}, ${box.count} nodes`}
                style={{
                  position: 'absolute', left: `${box.x}px`, top: `${box.y}px`, width: `${box.w}px`, height: `${box.h}px`,
                  'box-sizing': 'border-box', 'border-radius': tokens.radiusMd, border: `1px dashed ${tokens.borderMenu}`,
                  padding: `0 ${tokens.spaceSm}px`, color: tokens.fgMuted, font: tokens.type.textSm, 'pointer-events': 'none',
                }}>
                <div style={{ height: `${LEVEL_HEAD}px`, 'line-height': `${LEVEL_HEAD}px`, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
                  {label} · {box.count}
                </div>
              </div>;
            }
            if (box.kind === 'done') {
              // The level's done nodes, folded; a click unfolds the graph.
              return <button data-wash-hit type="button" data-testid={`workspace-plan-done-${box.id}`} title="Show the done nodes" {...hoverable(box.id)}
                onClick={(ev) => { ev.stopPropagation(); toggleDone(true); }}
                style={{
                  position: 'absolute', left: `${box.x}px`, top: `${box.y}px`, width: `${box.w}px`, height: `${box.h}px`,
                  'box-sizing': 'border-box', 'border-radius': tokens.radiusLg, cursor: 'pointer', 'text-align': 'left',
                  border: `1px solid ${tokens.borderMenu}`, 'border-left': `4px solid ${tokens.accentGreen}`,
                  background: `color-mix(in srgb, ${tokens.bgWindow} ${BOX_OPACITY}, transparent)`, color: tokens.fgMuted, font: tokens.type.textSm, padding: `${tokens.spaceSm}px ${tokens.spaceMd}px`,
                  opacity: faded(box) ? 0.3 : 1,
                }}>
                <div style={{ font: tokens.type.textMd, 'font-weight': 600, color: tokens.fg }}>{box.count} done</div>
                <div>click to show</div>
              </button>;
            }
            const node = () => byId().get(box.id)!;
            const group = box.kind === 'group';
            return <div data-wash-hit="subtle" data-testid={`workspace-plan-node-${box.id}`} data-state={node()?.state} data-selected={selected() === box.id ? 'true' : 'false'}
              role="button" tabIndex={0} aria-label={`${box.id} ${node()?.title}, ${node()?.state}`} {...hoverable(box.id)}
              onClick={(ev) => { ev.stopPropagation(); setSelected(box.id); }}
              onKeyDown={(ev) => { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); setSelected(box.id); } }}
              style={{
                position: 'absolute', left: `${box.x}px`, top: `${box.y}px`, width: `${box.w}px`, height: `${box.h}px`,
                'box-sizing': 'border-box', 'border-radius': tokens.radiusLg, cursor: 'pointer', overflow: 'hidden',
                border: `1px ${box.sketch ? 'dashed' : 'solid'} ${selected() === box.id ? tokens.borderFocus : related().has(box.id) && picked() !== box.id ? tokens.fgMuted : tokens.borderMenu}`,
                'border-left': group ? undefined : `4px solid ${planStateColor(node()?.state ?? '')}`,
                background: `color-mix(in srgb, ${group && !box.collapsed ? tokens.bgInset : tokens.bgWindow} ${BOX_OPACITY}, transparent)`, opacity: box.sketch ? 0.75 : faded(box) ? 0.3 : 1,
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
              <Show when={box.collapsed}><div data-testid={`workspace-plan-collapsed-${box.id}`} style={{ color: tokens.fgMuted, font: tokens.type.textSm }}>{box.count} nodes · done · Show done unfolds it</div></Show>
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
          <svg aria-hidden="true" width={layout().width} height={layout().height} style={{ position: 'absolute', inset: 0, 'pointer-events': 'none' }}>
            <defs>
              <marker id="wash-plan-arrow-in" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto">
                <path d="M0,0 L8,4 L0,8 z" fill={tokens.accentAmber} />
              </marker>
              <marker id="wash-plan-arrow-out" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto">
                <path d="M0,0 L8,4 L0,8 z" fill={tokens.accentBlue} />
              </marker>
            </defs>
            <For each={hovered()}>{(e) => {
              const incoming = e.to === hover();
              return <path data-testid={`workspace-plan-hover-${e.from}-${e.to}`} d={e.d} fill="none" stroke={incoming ? tokens.accentAmber : tokens.accentBlue} stroke-width="1.5" stroke-dasharray="4 3" opacity="0.7" marker-end={`url(#wash-plan-arrow-${incoming ? 'in' : 'out'})`} />;
            }}</For>
            <For each={own()}>{(e) => {
              const incoming = e.to === picked();
              return <path data-testid={`workspace-plan-need-${e.from}-${e.to}`} d={e.d} fill="none" stroke={incoming ? tokens.accentAmber : tokens.accentBlue} stroke-width="2.5" marker-end={`url(#wash-plan-arrow-${incoming ? 'in' : 'out'})`} />;
            }}</For>
          </svg>
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
          {/* The commits the body names, as links into the project's repo
              when its origin is a web host: where a finished node's work is. */}
          <Show when={w().repo_url && bodyCommits(n().body).length}>
            <div data-testid="workspace-plan-commits" style={{ color: tokens.fgMuted, font: tokens.type.textSm, display: 'flex', 'flex-wrap': 'wrap', gap: `${tokens.spaceSm}px` }}>
              Commits:
              <For each={bodyCommits(n().body)}>{(sha) => (
                <a data-wash-hit href={`${w().repo_url}/commit/${sha}`} target="_blank" rel="noopener noreferrer" data-testid={`workspace-plan-commit-${sha}`}
                  style={{ color: tokens.fgInfo, font: tokens.type.monoSm }}>{sha.slice(0, 9)}</a>
              )}</For>
            </div>
          </Show>
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
