// wash-term split layout — the term half of the kernel (docs/TERM_LAYOUT.md
// §3, §4). The splits themselves — paths, flattening, fractions, rects,
// dividers, geometric focus — are the shared kernel in @wash/ui/split-tree
// (docs/WM_LAYOUT.md §2), which the window manager tiles with too. What
// lives here is the leaf: a tab GROUP owns a tab list and one active tab, so
// a "pane" is just a group holding one tab and an unsplit window is a single
// group. Nothing here touches Solid or the DOM — every function is pure and
// returns a NEW tree, which is what makes the whole layer unit-testable and
// keeps the renderer a projection.
//
// The one rule the renderer must honour: terminal hosts are flat,
// absolutely-positioned siblings addressed by the rects this file computes.
// They are never reparented, because reparenting a mounted xterm loses its
// buffer (docs/TERM_LAYOUT.md §2).

import {
  ROOT,
  equalize as kEqualize,
  equalizeAll as kEqualizeAll,
  leafPaths,
  minFractionFor as kMinFractionFor,
  nodeAt,
  normalize as kNormalize,
  place,
  replaceAt,
  resizeSplit as kResizeSplit,
} from '@wash/ui/split-tree';
import type { Dir, LeafOps, PlacedDivider, Rect, Split as KSplit } from '@wash/ui/split-tree';

export { ROOT, childPath, parentPath, nodeAt, isSplit, focusNeighbor } from '@wash/ui/split-tree';
export type { Dir, FocusDir, PlacedDivider, Rect } from '@wash/ui/split-tree';

export interface Group {
  kind: 'group';
  // Channel ids, in strip order.
  tabs: number[];
  // The visible tab. Always a member of tabs (normalize repairs it).
  active: number;
}

export type Split = KSplit<Group>;

export type LayoutNode = Group | Split;

export interface PlacedGroup {
  path: string;
  group: Group;
  // rect covers the whole group (strip + content + status); content is what
  // the terminal gets, i.e. rect less the strip along the top and the status
  // bar along the bottom.
  rect: Rect;
  content: Rect;
  status: Rect;
}

export interface LayoutOpts {
  // Divider thickness in px, subtracted from a split's span before the
  // fractions are applied.
  gutter?: number;
  // Group tab-strip height in px, taken off the top of every group rect.
  strip?: number;
  // Group status-bar height in px, taken off the bottom of every group rect.
  status?: number;
}

export const DEFAULT_GUTTER = 4;
export const DEFAULT_STRIP = 24;
export const DEFAULT_STATUS = 0;

// A pane must keep a usable grid. Splits that would go under this are
// refused rather than producing an unreadable pane, and divider drags
// clamp to it. Expressed in px because that is what the kernel sees; the
// numbers are ~20 cols x 3 rows at the default cell size, plus the strip and
// per-pane status bar.
export const MIN_PANE_W = 140;
export const MIN_PANE_H = 92;

// GROUP_OPS is how the kernel's normalize treats groups: an emptied group
// does not survive — its space goes back to the siblings — and a group's
// active tab is always one of its tabs.
const GROUP_OPS: LeafOps<Group> = {
  empty: () => ({ kind: 'group', tabs: [], active: 0 }),
  keep: (g) => g.tabs.length > 0,
  repair: (g) => (g.tabs.includes(g.active)
    ? g
    : { kind: 'group', tabs: [...g.tabs], active: g.tabs[0] ?? 0 }),
};

export function groupAt(tree: LayoutNode, path: string): Group | undefined {
  const n = nodeAt(tree, path);
  return n && n.kind === 'group' ? n : undefined;
}

// ---- queries ----

export const groupPaths = (tree: LayoutNode): string[] => leafPaths(tree);

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

// ---- construction ----

export const singleGroup = (tabs: number[], active?: number): Group => ({
  kind: 'group',
  tabs: [...tabs],
  active: active !== undefined && tabs.includes(active) ? active : (tabs[0] ?? 0),
});

// ---- normalize ----
//
// Every invariant in docs/TERM_LAYOUT.md §3 is repaired here, and every
// mutation below runs its result through it. The split rules (fractions,
// same-direction flattening) are the kernel's; GROUP_OPS adds the group
// ones.

export const normalize = (node: LayoutNode): LayoutNode => kNormalize(node, GROUP_OPS);

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
// minFrac.
export const resizeSplit = (
  tree: LayoutNode,
  path: string,
  index: number,
  delta: number,
  minFrac = 0.08,
): LayoutNode => kResizeSplit(tree, path, index, delta, GROUP_OPS, minFrac);

// equalize gives every child of a split the same fraction.
export const equalize = (tree: LayoutNode, path: string): LayoutNode =>
  kEqualize(tree, path, GROUP_OPS);

// equalizeAll rebalances EVERY split in the tree, not just one level.
export const equalizeAll = (tree: LayoutNode): LayoutNode => kEqualizeAll(tree, GROUP_OPS);

// minFractionFor converts the pane minimum into the fraction a divider
// drag may not cross, given the span it is dividing. A pane can't be
// dragged below a readable grid — the same rule canSplit enforces up
// front, applied continuously.
export const minFractionFor = (dir: Dir, span: number): number =>
  kMinFractionFor(dir, span, MIN_PANE_W, MIN_PANE_H);

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

// layout assigns a rect to every group and every divider (the kernel's
// place), then cuts each group's content rect out of it: the rect less the
// strip along the top and the status bar along the bottom.
export function layout(tree: LayoutNode, rect: Rect, opts: LayoutOpts = {}): {
  groups: PlacedGroup[];
  dividers: PlacedDivider[];
} {
  const strip = opts.strip ?? DEFAULT_STRIP;
  const status = opts.status ?? DEFAULT_STATUS;
  const { leaves, dividers } = place(tree, rect, opts.gutter ?? DEFAULT_GUTTER);
  const groups = leaves.map(({ path, leaf, rect: r }): PlacedGroup => {
    const usable = Math.max(0, r.h - strip);
    const statusH = Math.min(status, usable);
    return {
      path,
      group: leaf,
      rect: r,
      content: { x: r.x, y: r.y + strip, w: r.w, h: Math.max(0, usable - statusH) },
      status: { x: r.x, y: r.y + r.h - statusH, w: r.w, h: statusH },
    };
  });
  return { groups, dividers };
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
