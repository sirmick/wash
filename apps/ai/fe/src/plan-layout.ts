// Lays a workspace plan out left to right, with no graph library: top-level
// nodes (milestones, usually) are columns in the order their needs put them;
// inside a column, the children sit in levels by their needs on each other —
// level 0 needs nothing of its siblings, level 1 needs something in level 0,
// and so on — each level a box, with one arrow from each level to the next;
// anything deeper is listed inside its top child's box. Within a level,
// nodes sit nearest what they need (the barycenter of their needs' places),
// so a node's own arrows, drawn when it is picked, are short. Done work
// folds away by default: a done milestone to its header, done nodes in a
// level to one chip. Pure, so it is tested without a DOM; the Plan tab
// draws the boxes and edges it returns.

export interface PlanNodeInput {
  id: string;
  parent?: string;
  needs?: string[];
  template?: string;
  state?: string;
}

export interface PlanBox {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  /**
   * group: a top-level node with children, drawn as a column around them.
   * level: one dependency depth inside a group, drawn as a box around its
   * nodes; its id is `<group>/<level>` and names no plan node.
   * done: the done nodes of a level, folded into one chip; its id is
   * `<group>/<level>/done`.
   */
  kind: 'group' | 'level' | 'node' | 'done';
  /** A group with nothing in it yet: a milestone still to be planned. */
  sketch?: boolean;
  /** A done group drawn as its header only, count nodes inside it. */
  collapsed?: boolean;
  /** level and done: the depth; level, done and collapsed group: how many nodes. */
  level?: number;
  count?: number;
}

export interface PlanEdge { from: string; to: string; d: string }

export interface PlanLayout {
  width: number;
  height: number;
  boxes: PlanBox[];
  /** The edges always drawn: milestone to milestone, level to level, and needs across milestones. */
  edges: PlanEdge[];
  /** Every need as an edge between the boxes that stand for its two ends, for drawing a picked node's own. */
  needEdges: PlanEdge[];
}

export interface PlanOptions {
  /** Fold done milestones to their header and done nodes to one chip per level (default true). */
  collapseDone?: boolean;
}

export const NODE_W = 220;
export const NODE_H = 58;
export const LINE_H = 20;
export const LEVEL_HEAD = 24;
const GAP_X = 48;
const GAP_Y = 16;
const GROUP_HEAD = 34;
const GROUP_PAD = 12;
const LEVEL_PAD = 8;
const MARGIN = 16;
/** A level's nodes wrap into sub-columns of this many, so fifty independent
 *  packages are a block to look at, not a column to scroll. */
export const MAX_ROWS = 10;

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

/** The y an edge joins a box at: a header's middle, or a node's. */
function anchorY(b: PlanBox): number {
  if (b.kind === 'group' && !b.sketch && !b.collapsed) return b.y + GROUP_HEAD / 2;
  if (b.kind === 'level') return b.y + LEVEL_HEAD / 2;
  return b.y + Math.min(b.h, NODE_H) / 2;
}

/** curve is the path from one box's right edge to another's left; when the
 *  target is not to the right, it leaves and arrives by the right edges. */
function curve(from: PlanBox, to: PlanBox): string {
  const fy = anchorY(from);
  const ty = anchorY(to);
  const fx = from.x + from.w;
  if (to.x < fx + 12) {
    const tx = to.x + to.w;
    const out = Math.max(fx, tx) + 32;
    return `M${fx},${fy} C${out},${fy} ${out},${ty} ${tx},${ty}`;
  }
  const tx = to.x;
  const bend = Math.max(24, (tx - fx) / 2);
  return `M${fx},${fy} C${fx + bend},${fy} ${tx - bend},${ty} ${tx},${ty}`;
}

/**
 * layoutPlan places nodes. extraLines(id) is how many lines of content (its
 * members, its nested steps) a box carries below its title.
 */
export function layoutPlan(nodes: PlanNodeInput[], extraLines: (id: string) => number = () => 0, opts: PlanOptions = {}): PlanLayout {
  const collapseDone = opts.collapseDone ?? true;
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const done = (id: string) => byId.get(id)?.state === 'done';
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
  const edges: PlanEdge[] = [];
  // Where a node that has no box of its own is drawn: its level's done chip.
  const standIn = new Map<string, string>();
  const descendants = (id: string): number => childrenOf(id).reduce((n, c) => n + 1 + descendants(c), 0);
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
      if (collapseDone && done(id)) {
        // Finished: its header and a count, the width of one node.
        const h = GROUP_HEAD + NODE_H;
        boxes.push({ id, x, y, w: NODE_W, h, kind: 'group', collapsed: true, count: descendants(id) });
        y += h + GAP_Y;
        continue;
      }
      // Children in levels by their needs on each other; each level a box.
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
      const levels: PlanBox[] = [];
      const placed: PlanBox[] = [];
      // Each placed node's place in its level, 0..1 top to bottom, for the
      // barycenter of the level after it.
      const place = new Map<string, number>();
      for (let depth = 0; depth < layers.length; depth++) {
        const layer = layers[depth];
        if (!layer) continue;
        const shown = collapseDone ? layer.filter((k) => !done(k)) : layer;
        const folded = layer.length - shown.length;
        // Nearest what it needs: sort by the mean place of its needs in
        // earlier levels; a node needing nothing placed keeps the middle.
        const key = (k: string) => {
          const places = siblingNeeds(k).map((n) => place.get(n)).filter((p): p is number => p !== undefined);
          return places.length ? places.reduce((a, b) => a + b, 0) / places.length : 0.5;
        };
        const ordered = shown.map((k, i) => ({ k, i, key: key(k) })).sort((a, b) => a.key - b.key || a.i - b.i).map((o) => o.k);
        ordered.forEach((k, i) => place.set(k, ordered.length > 1 ? i / (ordered.length - 1) : 0.5));
        const levelY = y + GROUP_HEAD;
        // Sub-columns of at most MAX_ROWS, balanced, left to right; the
        // done chip takes the first slot.
        const slots = ordered.length + (folded ? 1 : 0);
        const cols = Math.max(1, Math.ceil(slots / MAX_ROWS));
        const rows = Math.ceil(slots / cols);
        let colX = innerX + LEVEL_PAD;
        let stackH = 0;
        let innerY = levelY + LEVEL_HEAD;
        let inCol = 0;
        const next = (h: number) => {
          innerY += h + GAP_Y;
          if (++inCol === rows) {
            stackH = Math.max(stackH, innerY - GAP_Y - (levelY + LEVEL_HEAD));
            colX += NODE_W + GAP_X / 2;
            innerY = levelY + LEVEL_HEAD;
            inCol = 0;
          }
        };
        if (folded) {
          const chip: PlanBox = { id: `${id}/${depth}/done`, x: colX, y: innerY, w: NODE_W, h: NODE_H, kind: 'done', level: depth, count: folded };
          placed.push(chip);
          for (const k of layer) if (done(k)) { standIn.set(k, chip.id); place.set(k, 0); }
          next(chip.h);
        }
        for (const k of ordered) {
          const h = heightOf(k);
          placed.push({ id: k, x: colX, y: innerY, w: NODE_W, h, kind: 'node' });
          next(h);
        }
        if (inCol) { stackH = Math.max(stackH, innerY - GAP_Y - (levelY + LEVEL_HEAD)); colX += NODE_W + GAP_X / 2; }
        const w = colX - GAP_X / 2 - innerX + LEVEL_PAD;
        const h = LEVEL_HEAD + stackH + LEVEL_PAD;
        const level: PlanBox = { id: `${id}/${depth}`, x: innerX, y: levelY, w, h, kind: 'level', level: depth, count: layer.length };
        const prev = levels[levels.length - 1];
        if (prev) edges.push({ from: prev.id, to: level.id, d: curve(prev, level) });
        levels.push(level);
        innerH = Math.max(innerH, h);
        innerX += w + GAP_X;
      }
      const w = innerX - GAP_X - x + GROUP_PAD;
      const h = GROUP_HEAD + innerH + GROUP_PAD;
      boxes.push({ id, x, y, w, h, kind: 'group' }, ...levels, ...placed);
      columnWidth = Math.max(columnWidth, w);
      y += h + GAP_Y;
    }
    height = Math.max(height, y - GAP_Y + MARGIN);
    x += columnWidth + GAP_X;
  }
  const at = new Map(boxes.map((b) => [b.id, b]));
  // The box standing for a node: itself, its package, its done chip, or
  // (inside a collapsed milestone) the milestone.
  const standFor = (id: string): PlanBox | undefined => {
    const b = boxOf(id);
    return at.get(id) ?? at.get(standIn.get(id) ?? '') ?? at.get(b) ?? at.get(standIn.get(b) ?? '') ?? at.get(topOf(id));
  };
  const needEdges: PlanEdge[] = [];
  const seen = new Set<string>();
  const seenNeed = new Set<string>();
  for (const n of nodes) {
    const to = standFor(n.id);
    if (!to) continue;
    for (const need of n.needs ?? []) {
      if (!byId.has(need)) continue;
      const from = standFor(need);
      if (!from || from === to) continue;
      const key = `${from.id}>${to.id}`;
      if (!seenNeed.has(key)) {
        seenNeed.add(key);
        needEdges.push({ from: from.id, to: to.id, d: curve(from, to) });
      }
      // Inside one group the level arrows say it; a node's own needs are drawn when it is picked.
      if (parentOf(from.id) && topOf(from.id) === topOf(to.id)) continue;
      if (seen.has(key)) continue;
      seen.add(key);
      edges.push({ from: from.id, to: to.id, d: curve(from, to) });
    }
  }
  return { width: Math.max(x - GAP_X + MARGIN, NODE_W + 2 * MARGIN), height: Math.max(height, NODE_H + 2 * MARGIN), boxes, edges, needEdges };
}
