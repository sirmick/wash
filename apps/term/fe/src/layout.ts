// wash-term split layout — the pure kernel (docs/TERM_LAYOUT.md §3, §4).
//
// A window is a tree whose leaves are tab GROUPS: a group owns a tab list
// and one active tab, so a "pane" is just a group holding one tab and an
// unsplit window is a single group. Nothing here touches Solid or the DOM —
// every function is pure and returns a NEW tree, which is what makes the
// whole layer unit-testable and keeps the renderer a projection.
//
// The one rule the renderer must honour: terminal hosts are flat,
// absolutely-positioned siblings addressed by the rects this file computes.
// They are never reparented, because reparenting a mounted xterm loses its
// buffer (docs/TERM_LAYOUT.md §2).

export type Dir = 'row' | 'col';

export interface Group {
  kind: 'group';
  // Channel ids, in strip order.
  tabs: number[];
  // The visible tab. Always a member of tabs (normalize repairs it).
  active: number;
}

export interface Split {
  kind: 'split';
  // row = children side by side; col = children stacked.
  dir: Dir;
  children: LayoutNode[];
  // Fractions of the split's inner span (the span less the gutters),
  // parallel to children, summing to 1.
  sizes: number[];
}

export type LayoutNode = Group | Split;

export interface Rect { x: number; y: number; w: number; h: number }

export interface PlacedGroup {
  path: string;
  group: Group;
  // rect covers the whole group (strip + content); content is what the
  // terminal gets, i.e. rect less the strip along the top.
  rect: Rect;
  content: Rect;
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

export interface LayoutOpts {
  // Divider thickness in px, subtracted from a split's span before the
  // fractions are applied.
  gutter?: number;
  // Group tab-strip height in px, taken off the top of every group rect.
  strip?: number;
}

export const DEFAULT_GUTTER = 4;
export const DEFAULT_STRIP = 24;

// A pane must keep a usable grid. Splits that would go under this are
// refused rather than producing an unreadable pane, and divider drags
// clamp to it. Expressed in px because that is what the kernel sees; the
// numbers are ~20 cols x 3 rows at the default cell size, plus the strip.
export const MIN_PANE_W = 140;
export const MIN_PANE_H = 72;

// ---- paths ----
//
// A path addresses a node by child indices: '' is the root, '0' its first
// child, '0.1' that child's second. Paths are derived per render and never
// persisted — they move when the tree changes. Identity is always a
// channel id.

export const ROOT = '';

export const childPath = (path: string, i: number): string =>
  path === ROOT ? String(i) : `${path}.${i}`;

export const parentPath = (path: string): string => {
  const i = path.lastIndexOf('.');
  return i < 0 ? ROOT : path.slice(0, i);
};

export function nodeAt(tree: LayoutNode, path: string): LayoutNode | undefined {
  if (path === ROOT) return tree;
  let n: LayoutNode | undefined = tree;
  for (const seg of path.split('.')) {
    if (!n || n.kind !== 'split') return undefined;
    n = n.children[Number(seg)];
  }
  return n;
}

export function groupAt(tree: LayoutNode, path: string): Group | undefined {
  const n = nodeAt(tree, path);
  return n && n.kind === 'group' ? n : undefined;
}

// ---- queries ----

export function groupPaths(tree: LayoutNode): string[] {
  const out: string[] = [];
  const walk = (n: LayoutNode, path: string) => {
    if (n.kind === 'group') { out.push(path); return; }
    n.children.forEach((c, i) => walk(c, childPath(path, i)));
  };
  walk(tree, ROOT);
  return out;
}

export function channels(tree: LayoutNode): number[] {
  const out: number[] = [];
  const walk = (n: LayoutNode) => {
    if (n.kind === 'group') { out.push(...n.tabs); return; }
    n.children.forEach(walk);
  };
  walk(tree);
  return out;
}

// pathOfChannel finds the group holding a channel. Undefined if the
// channel is not in the tree at all.
export function pathOfChannel(tree: LayoutNode, channel: number): string | undefined {
  for (const p of groupPaths(tree)) {
    const g = groupAt(tree, p)!;
    if (g.tabs.includes(channel)) return p;
  }
  return undefined;
}

// visibleChannels are the ones a renderer should show: each group's active
// tab. Everything else stays mounted but hidden.
export function visibleChannels(tree: LayoutNode): number[] {
  return groupPaths(tree)
    .map((p) => groupAt(tree, p)!.active)
    .filter((c) => c > 0);
}

export const isSplit = (n: LayoutNode): n is Split => n.kind === 'split';

// ---- construction ----

export const singleGroup = (tabs: number[], active?: number): Group => ({
  kind: 'group',
  tabs: [...tabs],
  active: active !== undefined && tabs.includes(active) ? active : (tabs[0] ?? 0),
});

const cloneNode = (n: LayoutNode): LayoutNode =>
  n.kind === 'group'
    ? { kind: 'group', tabs: [...n.tabs], active: n.active }
    : { kind: 'split', dir: n.dir, children: n.children.map(cloneNode), sizes: [...n.sizes] };

// replaceAt returns a copy of tree with the node at path swapped out.
function replaceAt(tree: LayoutNode, path: string, next: LayoutNode): LayoutNode {
  if (path === ROOT) return next;
  const segs = path.split('.').map(Number);
  const rec = (n: LayoutNode, depth: number): LayoutNode => {
    if (n.kind !== 'split') return n;
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
// Every invariant in docs/TERM_LAYOUT.md §3 is repaired here, and every
// mutation below runs its result through it. In particular a split never
// directly contains a split of the SAME direction — it is flattened into
// the parent, absorbing its fractions. Without that rule repeated
// splitting degenerates into a binary spine and n-ary buys nothing.

export function normalize(node: LayoutNode): LayoutNode {
  if (node.kind === 'group') {
    if (node.tabs.includes(node.active)) return node;
    return { kind: 'group', tabs: [...node.tabs], active: node.tabs[0] ?? 0 };
  }

  const pairs: Array<[LayoutNode, number]> = [];
  node.children.forEach((child, i) => {
    const size = node.sizes[i] ?? 1 / node.children.length;
    if (size <= 0) return;
    const n = normalize(child);
    if (n.kind === 'group') {
      // Empty groups do not survive: their space goes back to the siblings.
      if (n.tabs.length) pairs.push([n, size]);
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

  if (pairs.length === 0) return { kind: 'group', tabs: [], active: 0 };
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

// addTab appends a channel to the group at path and makes it active.
// Falls back to the first group when the path is stale.
export function addTab(tree: LayoutNode, path: string, channel: number): LayoutNode {
  if (pathOfChannel(tree, channel) !== undefined) return tree;
  const target = groupAt(tree, path) ? path : groupPaths(tree)[0];
  const g = target !== undefined ? groupAt(tree, target) : undefined;
  if (!g) return normalize(singleGroup([channel], channel));
  const next: Group = { kind: 'group', tabs: [...g.tabs, channel], active: channel };
  return normalize(replaceAt(tree, target, next));
}

// setActiveTab makes a channel the visible one in whichever group holds it.
export function setActiveTab(tree: LayoutNode, channel: number): LayoutNode {
  const path = pathOfChannel(tree, channel);
  if (path === undefined) return tree;
  const g = groupAt(tree, path)!;
  if (g.active === channel) return tree;
  return normalize(replaceAt(tree, path, { kind: 'group', tabs: [...g.tabs], active: channel }));
}

// closeTab removes a channel. An emptied group collapses and its space is
// redistributed to its siblings (normalize does both).
export function closeTab(tree: LayoutNode, channel: number): LayoutNode {
  const path = pathOfChannel(tree, channel);
  if (path === undefined) return tree;
  const g = groupAt(tree, path)!;
  const at = g.tabs.indexOf(channel);
  const tabs = g.tabs.filter((c) => c !== channel);
  // Activation follows the tab to the right, then the left — the same
  // instinct a browser tab strip has.
  const active = g.active === channel ? (tabs[at] ?? tabs[at - 1] ?? 0) : g.active;
  return normalize(replaceAt(tree, path, { kind: 'group', tabs, active }));
}

// canSplit reports whether splitting the group occupying `rect` in `dir`
// would leave both halves usable.
export function canSplit(rect: Rect, dir: Dir, opts: LayoutOpts = {}): boolean {
  const gutter = opts.gutter ?? DEFAULT_GUTTER;
  if (dir === 'row') return (rect.w - gutter) / 2 >= MIN_PANE_W;
  return (rect.h - gutter) / 2 >= MIN_PANE_H;
}

// splitGroup divides the group at path in two, putting `channel` in the new
// half. When the parent split already runs in the same direction the result
// flattens into it (see normalize), so three splits right give one n-ary
// row of three rather than a lopsided chain.
export function splitGroup(tree: LayoutNode, path: string, dir: Dir, channel: number): LayoutNode {
  const g = groupAt(tree, path);
  if (!g) return addTab(tree, groupPaths(tree)[0] ?? ROOT, channel);
  const fresh: Group = { kind: 'group', tabs: [channel], active: channel };
  const split: Split = { kind: 'split', dir, children: [g, fresh], sizes: [0.5, 0.5] };
  return normalize(replaceAt(tree, path, split));
}

// moveTabBefore drops a channel in front of `beforeChannel`. Within one
// group that is a reorder; across groups it is a move — and because the
// terminal host never moves in the DOM, the move costs nothing.
// beforeChannel < 0 appends to the group named by toPath instead.
export function moveTabBefore(
  tree: LayoutNode,
  channel: number,
  beforeChannel: number,
  toPath?: string,
): LayoutNode {
  const fromPath = pathOfChannel(tree, channel);
  if (fromPath === undefined) return tree;
  const targetPath = beforeChannel >= 0 ? pathOfChannel(tree, beforeChannel) : toPath;
  if (targetPath === undefined || !groupAt(tree, targetPath)) return tree;
  if (channel === beforeChannel) return tree;

  // Remove first, then insert, so an intra-group move computes its
  // insertion index against the post-removal list.
  let next = tree;
  const from = groupAt(next, fromPath)!;
  const tabs = from.tabs.filter((c) => c !== channel);
  const at = from.tabs.indexOf(channel);
  const active = from.active === channel ? (tabs[at] ?? tabs[at - 1] ?? 0) : from.active;
  next = replaceAt(next, fromPath, { kind: 'group', tabs, active });

  // The target group may have shifted if removal emptied a sibling, so
  // re-find it by channel rather than trusting the old path.
  const stillThere = beforeChannel >= 0 ? pathOfChannel(next, beforeChannel) : targetPath;
  const dest = stillThere !== undefined ? groupAt(next, stillThere) : undefined;
  if (!dest || stillThere === undefined) {
    // Target vanished (it only held the tab we just moved) — put the tab
    // back where it was rather than dropping it on the floor.
    return normalize(replaceAt(next, fromPath, { kind: 'group', tabs: from.tabs, active: from.active }));
  }
  const idx = beforeChannel >= 0 ? dest.tabs.indexOf(beforeChannel) : dest.tabs.length;
  const merged = [...dest.tabs];
  merged.splice(idx < 0 ? merged.length : idx, 0, channel);
  next = replaceAt(next, stillThere, { kind: 'group', tabs: merged, active: channel });
  return normalize(next);
}

// resizeSplit trades size between children i and i+1 of a split. delta is a
// fraction of the split's inner span. Clamped so neither side goes under
// minFrac. (Wired to divider drag in M2; kernel-complete now.)
export function resizeSplit(
  tree: LayoutNode,
  path: string,
  index: number,
  delta: number,
  minFrac = 0.08,
): LayoutNode {
  const n = nodeAt(tree, path);
  if (!n || n.kind !== 'split' || index < 0 || index + 1 >= n.children.length) return tree;
  const a = n.sizes[index];
  const b = n.sizes[index + 1];
  const pair = a + b;
  const next = Math.max(minFrac, Math.min(pair - minFrac, a + delta));
  if (next === a) return tree;
  const sizes = [...n.sizes];
  sizes[index] = next;
  sizes[index + 1] = pair - next;
  return normalize(replaceAt(tree, path, { kind: 'split', dir: n.dir, children: n.children, sizes }));
}

// equalize gives every child of a split the same fraction.
export function equalize(tree: LayoutNode, path: string): LayoutNode {
  const n = nodeAt(tree, path);
  if (!n || n.kind !== 'split') return tree;
  const even = 1 / n.children.length;
  return normalize(replaceAt(tree, path, {
    kind: 'split', dir: n.dir, children: n.children, sizes: n.children.map(() => even),
  }));
}

// equalizeAll rebalances EVERY split in the tree, not just one level.
// "Equalize" means the window looks even afterwards; evening one split
// while a nested one stays lopsided is not what anyone is asking for.
export function equalizeAll(tree: LayoutNode): LayoutNode {
  const walk = (n: LayoutNode): LayoutNode => {
    if (n.kind === 'group') return n;
    const children = n.children.map(walk);
    return { kind: 'split', dir: n.dir, children, sizes: children.map(() => 1 / children.length) };
  };
  return normalize(walk(tree));
}

// minFractionFor converts the pixel minimum into the fraction a divider
// drag may not cross, given the span it is dividing. A pane can't be
// dragged below a readable grid — the same rule canSplit enforces up
// front, applied continuously.
export function minFractionFor(dir: Dir, span: number): number {
  if (span <= 0) return 0.08;
  const px = dir === 'row' ? MIN_PANE_W : MIN_PANE_H;
  return Math.min(0.45, px / span);
}

// pruneToChannels drops every tab whose channel is no longer live and
// repairs the tree around the holes. This is the restore path: a persisted
// leaf whose pty died while the browser was away must not leave a blank
// pane (docs/TERM_LAYOUT.md §7).
export function pruneToChannels(tree: LayoutNode, live: Set<number>): LayoutNode {
  const walk = (n: LayoutNode): LayoutNode => {
    if (n.kind === 'group') {
      const tabs = n.tabs.filter((c) => live.has(c));
      return { kind: 'group', tabs, active: tabs.includes(n.active) ? n.active : (tabs[0] ?? 0) };
    }
    return { kind: 'split', dir: n.dir, children: n.children.map(walk), sizes: [...n.sizes] };
  };
  return normalize(walk(tree));
}

// ---- placement ----

// layout assigns a rect to every group and every divider. Gutters come off
// a split's span before the fractions are applied, so children never
// overlap their dividers; each group's content rect is its rect less the
// strip along the top.
export function layout(tree: LayoutNode, rect: Rect, opts: LayoutOpts = {}): {
  groups: PlacedGroup[];
  dividers: PlacedDivider[];
} {
  const gutter = opts.gutter ?? DEFAULT_GUTTER;
  const strip = opts.strip ?? DEFAULT_STRIP;
  const groups: PlacedGroup[] = [];
  const dividers: PlacedDivider[] = [];

  const walk = (n: LayoutNode, r: Rect, path: string) => {
    if (n.kind === 'group') {
      const h = Math.max(0, r.h - strip);
      groups.push({
        path,
        group: n,
        rect: r,
        content: { x: r.x, y: r.y + strip, w: r.w, h },
      });
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
  return { groups, dividers };
}

// ---- focus ----

export type FocusDir = 'left' | 'right' | 'up' | 'down';

// focusNeighbor picks the pane a directional key should land on, by
// GEOMETRY rather than tree order — in a nested tree the tree-order
// neighbour is routinely not the one under the arrow. Candidates are the
// panes strictly beyond the source edge in that direction; the winner is
// the nearest one, tie-broken by how well it lines up across the axis.
export function focusNeighbor(
  groups: PlacedGroup[],
  fromPath: string,
  dir: FocusDir,
): string | undefined {
  const from = groups.find((g) => g.path === fromPath);
  if (!from) return undefined;
  const r = from.rect;
  const cx = r.x + r.w / 2;
  const cy = r.y + r.h / 2;
  const eps = 1;

  let best: { path: string; gap: number; off: number } | undefined;
  for (const g of groups) {
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

// ---- persistence (docs/TERM_LAYOUT.md §7) ----
//
// snake_case on the wire like the rest of wash's saved state. The BE stores
// the blob opaquely, so this schema is ours alone.

export interface PersistedNode {
  kind: 'group' | 'split';
  tabs?: number[];
  active?: number;
  dir?: Dir;
  children?: PersistedNode[];
  sizes?: number[];
}

export function toPersisted(node: LayoutNode): PersistedNode {
  if (node.kind === 'group') return { kind: 'group', tabs: [...node.tabs], active: node.active };
  return {
    kind: 'split',
    dir: node.dir,
    children: node.children.map(toPersisted),
    sizes: [...node.sizes],
  };
}

// fromPersisted validates as it decodes: anything malformed yields
// undefined so the caller can fall back to the v1 single-group migration
// rather than rendering a broken tree.
export function fromPersisted(raw: unknown): LayoutNode | undefined {
  const dec = (v: unknown): LayoutNode | undefined => {
    if (!v || typeof v !== 'object') return undefined;
    const o = v as PersistedNode;
    if (o.kind === 'group') {
      const tabs = Array.isArray(o.tabs) ? o.tabs.filter((n) => typeof n === 'number') : [];
      return { kind: 'group', tabs, active: typeof o.active === 'number' ? o.active : (tabs[0] ?? 0) };
    }
    if (o.kind !== 'split' || !Array.isArray(o.children) || o.children.length === 0) return undefined;
    const children: LayoutNode[] = [];
    for (const c of o.children) {
      const n = dec(c);
      if (!n) return undefined;
      children.push(n);
    }
    const dir: Dir = o.dir === 'col' ? 'col' : 'row';
    const raws = Array.isArray(o.sizes) && o.sizes.length === children.length
      ? o.sizes.map((s) => (typeof s === 'number' && s > 0 ? s : 0))
      : children.map(() => 1);
    const total = raws.reduce((a, b) => a + b, 0) || children.length;
    return { kind: 'split', dir, children, sizes: raws.map((s) => (s || 1) / total) };
  };
  const n = dec(raw);
  if (!n) return undefined;
  const normalized = normalize(n);
  // A tree that normalizes to nothing is not worth restoring.
  return channels(normalized).length ? normalized : undefined;
}
