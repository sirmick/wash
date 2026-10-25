// How wide the work ahead is. A swarm's throughput is bounded by how many
// nodes are unblocked at once, and that width is fat at the start of a
// milestone and a thread at its end: when the waves ahead are one or two
// nodes wide, fan-out has nothing to fan out to, and one agent holding the
// whole picture closes the milestone faster than a team passing messages.
// Pure, so it is tested without a DOM; the Plan tab's toolbar reads it.

import type { PlanNodeInput } from './plan-layout';

export interface Frontier {
  /** Work nodes not done. */
  remaining: number;
  /** waves[0] is unblocked now; waves[k] what the k-th wave of completions unblocks. */
  waves: number[];
  /** Milestones with nothing planned inside them: width unknown, not narrow. */
  unplanned: string[];
  /** Work remains, every wave is at most two wide, and nothing is unplanned. */
  narrow: boolean;
}

const MAX_WAVES = 50;

/**
 * planFrontier counts the work ahead in waves. A work node is one the
 * graph boxes: a child of a milestone, or a top-level node that is not a
 * milestone; its needs are its own and its steps', each resolved to the
 * work node or milestone standing for it. blocked(need) says whether a
 * need naming no node (a QA thread) is still unresolved; it holds a node
 * out of the first wave only.
 */
export function planFrontier(nodes: PlanNodeInput[], blocked: (need: string) => boolean = () => false): Frontier {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const parentOf = (id: string) => { const p = byId.get(id)?.parent; return p && byId.has(p) ? p : ''; };
  const isTop = (id: string) => !parentOf(id);
  const childrenOf = (id: string) => nodes.filter((n) => parentOf(n.id) === id).map((n) => n.id);
  // The work node standing for id: itself at depth 0 or 1, else its depth-1 ancestor.
  const workOf = (id: string): string => {
    let cur = id;
    for (let guard = 0; guard <= nodes.length; guard++) {
      const p = parentOf(cur);
      if (!p || !parentOf(p)) return cur;
      cur = p;
    }
    return cur;
  };
  const milestone = (id: string) => byId.get(id)?.template === 'milestone';
  const done = (id: string) => byId.get(id)?.state === 'done';
  const unplanned = nodes.filter((n) => milestone(n.id) && isTop(n.id) && !childrenOf(n.id).length && !done(n.id)).map((n) => n.id);
  const work = nodes.filter((n) => (isTop(n.id) && !milestone(n.id)) || (!isTop(n.id) && isTop(parentOf(n.id)))).map((n) => n.id);
  const remaining = work.filter((id) => !done(id));
  // A node's needs — its steps' and its milestone's included, since a need
  // on a milestone holds everything inside it — as what stands for each.
  const needsOf = (id: string) => {
    const out = new Set<string>();
    const add = (n: string) => { for (const need of byId.get(n)?.needs ?? []) out.add(byId.has(need) ? (milestone(need) || isTop(need) ? need : workOf(need)) : need); };
    const walk = (n: string) => { add(n); for (const c of childrenOf(n)) walk(c); };
    walk(id);
    for (let p = parentOf(id), guard = 0; p && guard <= nodes.length; p = parentOf(p), guard++) add(p);
    out.delete(id);
    out.delete(parentOf(id));
    return [...out];
  };
  const satisfied = new Set<string>(nodes.filter((n) => done(n.id)).map((n) => n.id));
  // A milestone counts as satisfied once everything in it is done.
  for (const n of nodes) if (milestone(n.id) && !done(n.id) && childrenOf(n.id).length && work.filter((w) => workOf(w) === w && parentOf(w) === n.id).every(done)) satisfied.add(n.id);
  const waves: number[] = [];
  let left = [...remaining];
  for (let k = 0; left.length && k < MAX_WAVES; k++) {
    const ready = left.filter((id) => needsOf(id).every((need) => satisfied.has(need) || (!byId.has(need) && (k > 0 || !blocked(need)))));
    if (!ready.length) {
      // Nothing unblocked now because of threads alone: a zero-width
      // first wave, then the threads resolve. Otherwise a cycle, or
      // needs on nothing in the plan: the rest all at once.
      if (k === 0 && left.some((id) => needsOf(id).every((need) => satisfied.has(need) || !byId.has(need)))) { waves.push(0); continue; }
      waves.push(left.length);
      break;
    }
    waves.push(ready.length);
    for (const id of ready) satisfied.add(id);
    // A milestone whose last node just completed frees what needs it.
    for (const n of nodes) if (milestone(n.id) && !satisfied.has(n.id) && childrenOf(n.id).every((c) => satisfied.has(c))) satisfied.add(n.id);
    left = left.filter((id) => !satisfied.has(id));
  }
  return { remaining: remaining.length, waves, unplanned, narrow: remaining.length > 0 && !unplanned.length && waves.every((w) => w <= 2) };
}

/** frontierText says the frontier in a few words for the toolbar. */
export function frontierText(f: Frontier): string {
  if (!f.remaining) return f.unplanned.length ? `nothing planned ahead · ${f.unplanned.length} milestone${f.unplanned.length > 1 ? 's' : ''} unplanned` : 'all done';
  const waves = f.waves.slice(0, 6).join(' → ') + (f.waves.length > 6 ? ' → …' : '');
  const un = f.unplanned.length ? ` · ${f.unplanned.length} milestone${f.unplanned.length > 1 ? 's' : ''} unplanned` : '';
  return `ahead: ${waves} (${f.remaining} left)${un}${f.narrow ? ' · narrow: one agent could close this' : ''}`;
}
