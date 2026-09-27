// Lays a workspace plan out left to right, with no graph library: top-level
// nodes (milestones, usually) are columns in the order their needs put them;
// a column's children sit inside it in layers by their own needs; anything
// deeper is listed inside its top child's box. Pure, so it is tested without
// a DOM; the Plan tab draws the boxes and edges it returns.

export interface PlanNodeInput {
  id: string;
  parent?: string;
  needs?: string[];
  template?: string;
}

export interface PlanBox {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  /** group: a top-level node with children, drawn as a column around them. */
  kind: 'group' | 'node';
  /** A group with nothing in it yet: a milestone still to be planned. */
  sketch?: boolean;
}

export interface PlanEdge { from: string; to: string; d: string }

export interface PlanLayout { width: number; height: number; boxes: PlanBox[]; edges: PlanEdge[] }

export const NODE_W = 220;
export const NODE_H = 58;
export const LINE_H = 20;
const GAP_X = 48;
const GAP_Y = 16;
const GROUP_HEAD = 34;
const GROUP_PAD = 12;
const MARGIN = 16;

/** rank gives each id its longest-path depth over needs among ids. */
function rank(ids: string[], needsOf: (id: string) => string[]): Map<string, number> {
  const out = new Map<string, number>();
  const visiting = new Set<string>();
  const set = new Set(ids);
  const visit = (id: string): number => {
    const known = out.get(id);
    if (known !== undefined) return known;
    if (visiting.has(id)) return 0; // the backend refuses cycles; never loop here
    visiting.add(id);
    let r = 0;
    for (const need of needsOf(id)) if (set.has(need) && need !== id) r = Math.max(r, visit(need) + 1);
    visiting.delete(id);
    out.set(id, r);
    return r;
  };
  for (const id of ids) visit(id);
  return out;
}

/**
 * layoutPlan places nodes. extraLines(id) is how many lines of content (its
 * members, its nested steps) a box carries below its title.
 */
export function layoutPlan(nodes: PlanNodeInput[], extraLines: (id: string) => number = () => 0): PlanLayout {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const parentOf = (id: string) => { const p = byId.get(id)?.parent; return p && byId.has(p) ? p : ''; };
  const childrenOf = (id: string) => nodes.filter((n) => parentOf(n.id) === id).map((n) => n.id);
  // The box a node is drawn in: itself at depth 0 or 1, else its depth-1 ancestor.
  const boxOf = (id: string): string => {
    let cur = id;
    for (let guard = 0; guard <= nodes.length; guard++) {
      const p = parentOf(cur);
      if (!p || !parentOf(p)) return cur;
      cur = p;
    }
    return cur;
  };
  const top = nodes.filter((n) => !parentOf(n.id)).map((n) => n.id);
  // A need on something inside another column counts as a need on that column.
  const topOf = (id: string) => { let cur = id; for (let g = 0; g <= nodes.length && parentOf(cur); g++) cur = parentOf(cur); return cur; };
  const topNeeds = (id: string) => {
    const out = new Set<string>();
    const walk = (n: string) => {
      for (const need of byId.get(n)?.needs ?? []) if (byId.has(need)) out.add(topOf(need));
      for (const c of childrenOf(n)) walk(c);
    };
    walk(id);
    out.delete(id);
    return [...out];
  };
  const topRank = rank(top, topNeeds);
  const columns: string[][] = [];
  for (const id of top) {
    const r = topRank.get(id) ?? 0;
    (columns[r] ??= []).push(id);
  }
  const boxes: PlanBox[] = [];
  let x = MARGIN;
  let height = 0;
  for (const column of columns) {
    if (!column) continue;
    let y = MARGIN;
    let columnWidth = NODE_W;
    for (const id of column) {
      const kids = childrenOf(id);
      if (!kids.length) {
        const sketch = byId.get(id)?.template === 'milestone';
        const h = sketch ? GROUP_HEAD + NODE_H : NODE_H + extraLines(id) * LINE_H;
        boxes.push({ id, x, y, w: NODE_W, h, kind: sketch ? 'group' : 'node', sketch: sketch || undefined });
        y += h + GAP_Y;
        continue;
      }
      // Children in layers by their needs on each other; each layer a sub-column.
      const siblingNeeds = (c: string) => {
        const out = new Set<string>();
        const walk = (n: string) => {
          for (const need of byId.get(n)?.needs ?? []) {
            if (!byId.has(need)) continue;
            const b = boxOf(need);
            if (kids.includes(b)) out.add(b);
          }
          for (const g of childrenOf(n)) walk(g);
        };
        walk(c);
        out.delete(c);
        return [...out];
      };
      const kidRank = rank(kids, siblingNeeds);
      const layers: string[][] = [];
      for (const k of kids) (layers[kidRank.get(k) ?? 0] ??= []).push(k);
      const heightOf = (k: string) => NODE_H + (extraLines(k) + nested(k)) * LINE_H;
      const nested = (k: string): number => childrenOf(k).reduce((n, c) => n + 1 + nested(c), 0);
      let innerX = x + GROUP_PAD;
      let innerH = 0;
      const placed: PlanBox[] = [];
      for (const layer of layers) {
        if (!layer) continue;
        let innerY = y + GROUP_HEAD;
        for (const k of layer) {
          const h = heightOf(k);
          placed.push({ id: k, x: innerX, y: innerY, w: NODE_W, h, kind: 'node' });
          innerY += h + GAP_Y;
        }
        innerH = Math.max(innerH, innerY - GAP_Y - (y + GROUP_HEAD));
        innerX += NODE_W + GAP_X / 2;
      }
      const w = innerX - GAP_X / 2 - x + GROUP_PAD;
      const h = GROUP_HEAD + innerH + GROUP_PAD;
      boxes.push({ id, x, y, w, h, kind: 'group' }, ...placed);
      columnWidth = Math.max(columnWidth, w);
      y += h + GAP_Y;
    }
    height = Math.max(height, y - GAP_Y + MARGIN);
    x += columnWidth + GAP_X;
  }
  const at = new Map(boxes.map((b) => [b.id, b]));
  const edges: PlanEdge[] = [];
  const seen = new Set<string>();
  for (const n of nodes) {
    const to = at.get(n.id) ?? at.get(boxOf(n.id));
    if (!to) continue;
    for (const need of n.needs ?? []) {
      if (!byId.has(need)) continue;
      const from = at.get(need) ?? at.get(boxOf(need));
      if (!from || from === to) continue;
      const key = `${from.id}>${to.id}`;
      if (seen.has(key)) continue;
      seen.add(key);
      // Group to group joins the headers; anything else the box middles.
      const fy = from.kind === 'group' && !from.sketch ? from.y + GROUP_HEAD / 2 : from.y + Math.min(from.h, NODE_H) / 2;
      const ty = to.kind === 'group' && !to.sketch ? to.y + GROUP_HEAD / 2 : to.y + Math.min(to.h, NODE_H) / 2;
      const fx = from.x + from.w;
      const tx = to.x;
      const bend = Math.max(24, (tx - fx) / 2);
      edges.push({ from: from.id, to: to.id, d: `M${fx},${fy} C${fx + bend},${fy} ${tx - bend},${ty} ${tx},${ty}` });
    }
  }
  return { width: Math.max(x - GAP_X + MARGIN, NODE_W + 2 * MARGIN), height: Math.max(height, NODE_H + 2 * MARGIN), boxes, edges };
}
