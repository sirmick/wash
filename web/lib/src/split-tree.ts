// split-tree — the generic n-ary split-layout kernel (docs/WM_LAYOUT.md §2).
//
// A layout is a tree whose inner nodes are splits — a direction, children,
// and the fraction of the span each child gets — and whose leaves are
// whatever the caller says they are. wash-term's leaves are tab groups
// (apps/term/fe/src/layout.ts); the window manager's are slots a window can
// sit in. Everything here is about the SPLITS: paths, flattening, fractions,
// rects, dividers, geometric focus. Nothing touches Solid or the DOM, and
// every function is pure and returns a new tree.
//
// The leaf type only has to carry a `kind` that is not 'split'; that is how
// the two are told apart, so a caller's persisted leaves keep their own tag.

export type Dir = 'row' | 'col';

export interface Split<L> {
  kind: 'split';
  // row = children side by side; col = children stacked.
  dir: Dir;
  children: Node<L>[];
  // Fractions of the split's inner span (the span less the gutters),
  // parallel to children, summing to 1.
  sizes: number[];
}

export type Node<L> = L | Split<L>;

export interface Rect { x: number; y: number; w: number; h: number }

export const isSplit = <L>(n: Node<L>): n is Split<L> =>
  (n as { kind?: unknown }).kind === 'split';

// LeafOps is how normalize treats the caller's leaves.
export interface LeafOps<L> {
  // empty is the leaf a tree collapses to when nothing survives.
  empty: () => L;
  // keep=false drops the leaf and gives its space to its siblings (term's
  // emptied groups). Omitted, every leaf survives (the WM's empty slots are
  // drop targets, so they must).
  keep?: (leaf: L) => boolean;
  // repair fixes a leaf's own invariants. Must return the same object when
  // nothing needed fixing.
  repair?: (leaf: L) => L;
}

// ---- paths ----
//
// A path addresses a node by child indices: '' is the root, '0' its first
// child, '0.1' that child's second. Paths are derived per render and never
// persisted — they move when the tree changes.

export const ROOT = '';

export const childPath = (path: string, i: number): string =>
  path === ROOT ? String(i) : `${path}.${i}`;

export const parentPath = (path: string): string => {
  const i = path.lastIndexOf('.');
  return i < 0 ? ROOT : path.slice(0, i);
};

export function nodeAt<L>(tree: Node<L>, path: string): Node<L> | undefined {
  if (path === ROOT) return tree;
  let n: Node<L> | undefined = tree;
  for (const seg of path.split('.')) {
    if (!n || !isSplit(n)) return undefined;
    n = n.children[Number(seg)];
  }
  return n;
}

// leafPaths lists every leaf in tree order (depth-first, left to right).
export function leafPaths<L>(tree: Node<L>): string[] {
  const out: string[] = [];
  const walk = (n: Node<L>, path: string) => {
    if (!isSplit(n)) { out.push(path); return; }
    n.children.forEach((c, i) => walk(c, childPath(path, i)));
  };
  walk(tree, ROOT);
  return out;
}

export function leaves<L>(tree: Node<L>): L[] {
  const out: L[] = [];
  const walk = (n: Node<L>) => {
    if (!isSplit(n)) { out.push(n); return; }
    n.children.forEach(walk);
  };
  walk(tree);
  return out;
}

// replaceAt returns a copy of tree with the node at path swapped out. A
// stale path returns the tree unchanged.
export function replaceAt<L>(tree: Node<L>, path: string, next: Node<L>): Node<L> {
  if (path === ROOT) return next;
  const segs = path.split('.').map(Number);
  const rec = (n: Node<L>, depth: number): Node<L> => {
    if (!isSplit(n)) return n;
    const i = segs[depth];
    const child = n.children[i];
    if (!child) return n;
    const replaced = depth === segs.length - 1 ? next : rec(child, depth + 1);
    const children = [...n.children];
    children[i] = replaced;
    return { kind: 'split', dir: n.dir, children, sizes: [...n.sizes] };
  };
  return rec(tree, 0);
}

// ---- normalize ----
//
// Every split invariant is repaired here, and every mutation below runs its
// result through it: fractions are positive and sum to 1, a split has at
// least two children, and a split never directly contains a split of the
// SAME direction — it is flattened into the parent, absorbing its
// fractions. Without that rule repeated splitting degenerates into a binary
// spine, and the same layout gets more than one shape.

export function normalize<L>(node: Node<L>, ops: LeafOps<L>): Node<L> {
  if (!isSplit(node)) return ops.repair ? ops.repair(node) : node;

  const pairs: Array<[Node<L>, number]> = [];
  node.children.forEach((child, i) => {
    const size = node.sizes[i] ?? 1 / node.children.length;
    if (size <= 0) return;
    const n = normalize(child, ops);
    if (!isSplit(n)) {
      if (!ops.keep || ops.keep(n)) pairs.push([n, size]);
      return;
    }
    if (n.dir === node.dir) {
      // Same-direction nesting flattens, splitting this child's fraction
      // among the grandchildren in their own proportions.
      n.children.forEach((gc, j) => pairs.push([gc, size * n.sizes[j]]));
      return;
    }
    pairs.push([n, size]);
  });

  if (pairs.length === 0) return ops.empty();
  if (pairs.length === 1) return pairs[0][0];

  const total = pairs.reduce((s, [, f]) => s + f, 0) || 1;
  return {
    kind: 'split',
    dir: node.dir,
    children: pairs.map(([c]) => c),
    sizes: pairs.map(([, f]) => f / total),
  };
}

// ---- mutations (all return a new, normalized tree) ----

// resizeSplit trades size between children i and i+1 of a split. delta is a
// fraction of the split's inner span. Clamped so neither side goes under
// minFrac.
export function resizeSplit<L>(
  tree: Node<L>,
  path: string,
  index: number,
  delta: number,
  ops: LeafOps<L>,
  minFrac = 0.08,
): Node<L> {
  const n = nodeAt(tree, path);
  if (!n || !isSplit(n) || index < 0 || index + 1 >= n.children.length) return tree;
  const a = n.sizes[index];
  const b = n.sizes[index + 1];
  const pair = a + b;
  const next = Math.max(minFrac, Math.min(pair - minFrac, a + delta));
  if (next === a) return tree;
  const sizes = [...n.sizes];
  sizes[index] = next;
  sizes[index + 1] = pair - next;
  return normalize(replaceAt(tree, path, { kind: 'split', dir: n.dir, children: n.children, sizes }), ops);
}

// equalize gives every child of a split the same fraction.
export function equalize<L>(tree: Node<L>, path: string, ops: LeafOps<L>): Node<L> {
  const n = nodeAt(tree, path);
  if (!n || !isSplit(n)) return tree;
  const even = 1 / n.children.length;
  return normalize(replaceAt(tree, path, {
    kind: 'split', dir: n.dir, children: n.children, sizes: n.children.map(() => even),
  }), ops);
}

// equalizeAll rebalances EVERY split in the tree, not just one level.
// "Equalize" means the layout looks even afterwards; evening one split
// while a nested one stays lopsided is not what anyone is asking for.
export function equalizeAll<L>(tree: Node<L>, ops: LeafOps<L>): Node<L> {
  const walk = (n: Node<L>): Node<L> => {
    if (!isSplit(n)) return n;
    const children = n.children.map(walk);
    return { kind: 'split', dir: n.dir, children, sizes: children.map(() => 1 / children.length) };
  };
  return normalize(walk(tree), ops);
}

// minFractionFor converts a pixel minimum into the fraction a divider drag
// may not cross, given the span it is dividing.
export function minFractionFor(dir: Dir, span: number, minW: number, minH: number): number {
  if (span <= 0) return 0.08;
  const px = dir === 'row' ? minW : minH;
  return Math.min(0.45, px / span);
}

// ---- placement ----

export interface PlacedLeaf<L> {
  path: string;
  leaf: L;
  rect: Rect;
}

export interface PlacedDivider {
  // The split this divider belongs to, and the index of the child on its
  // leading side — i.e. dragging it trades size between children i and i+1.
  path: string;
  index: number;
  dir: Dir;
  rect: Rect;
  // The split's inner span in px along its axis (its extent less the
  // gutters). A drag converts pixels to fractions with this, so the
  // renderer never has to reconstruct the parent's geometry.
  span: number;
}

// place assigns a rect to every leaf and every divider. Gutters come off a
// split's span before the fractions are applied, so children never overlap
// their dividers.
export function place<L>(tree: Node<L>, rect: Rect, gutter: number): {
  leaves: PlacedLeaf<L>[];
  dividers: PlacedDivider[];
} {
  const placed: PlacedLeaf<L>[] = [];
  const dividers: PlacedDivider[] = [];

  const walk = (n: Node<L>, r: Rect, path: string) => {
    if (!isSplit(n)) {
      placed.push({ path, leaf: n, rect: r });
      return;
    }
    const horizontal = n.dir === 'row';
    const span = horizontal ? r.w : r.h;
    const inner = Math.max(0, span - gutter * (n.children.length - 1));
    let at = horizontal ? r.x : r.y;
    n.children.forEach((child, i) => {
      const size = inner * n.sizes[i];
      const childRect: Rect = horizontal
        ? { x: at, y: r.y, w: size, h: r.h }
        : { x: r.x, y: at, w: r.w, h: size };
      walk(child, childRect, childPath(path, i));
      at += size;
      if (i < n.children.length - 1) {
        dividers.push({
          path,
          index: i,
          dir: n.dir,
          span: inner,
          rect: horizontal
            ? { x: at, y: r.y, w: gutter, h: r.h }
            : { x: r.x, y: at, w: r.w, h: gutter },
        });
        at += gutter;
      }
    });
  };

  walk(tree, rect, ROOT);
  return { leaves: placed, dividers };
}

// ---- focus ----

export type FocusDir = 'left' | 'right' | 'up' | 'down';

// focusNeighbor picks the pane a directional key should land on, by
// GEOMETRY rather than tree order — in a nested tree the tree-order
// neighbour is routinely not the one under the arrow. Candidates are the
// panes strictly beyond the source edge in that direction; the winner is
// the nearest one, tie-broken by how well it lines up across the axis.
export function focusNeighbor(
  placed: ReadonlyArray<{ path: string; rect: Rect }>,
  fromPath: string,
  dir: FocusDir,
): string | undefined {
  const from = placed.find((g) => g.path === fromPath);
  if (!from) return undefined;
  const r = from.rect;
  const cx = r.x + r.w / 2;
  const cy = r.y + r.h / 2;
  const eps = 1;

  let best: { path: string; gap: number; off: number } | undefined;
  for (const g of placed) {
    if (g.path === fromPath) continue;
    const o = g.rect;
    let gap: number;
    let off: number;
    switch (dir) {
      case 'left':
        if (o.x + o.w > r.x + eps) continue;
        gap = r.x - (o.x + o.w);
        off = Math.abs(o.y + o.h / 2 - cy);
        break;
      case 'right':
        if (o.x < r.x + r.w - eps) continue;
        gap = o.x - (r.x + r.w);
        off = Math.abs(o.y + o.h / 2 - cy);
        break;
      case 'up':
        if (o.y + o.h > r.y + eps) continue;
        gap = r.y - (o.y + o.h);
        off = Math.abs(o.x + o.w / 2 - cx);
        break;
      default:
        if (o.y < r.y + r.h - eps) continue;
        gap = o.y - (r.y + r.h);
        off = Math.abs(o.x + o.w / 2 - cx);
        break;
    }
    if (!best || gap < best.gap - eps || (Math.abs(gap - best.gap) <= eps && off < best.off)) {
      best = { path: g.path, gap, off };
    }
  }
  return best?.path;
}

