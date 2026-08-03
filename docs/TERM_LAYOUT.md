# Split panes for wash-term — design + implementation plan

Goal, in one line: **a wash-term window is a tree of tab groups — each group
has its own tab strip and its own controls, splits nest arbitrarily, and no
terminal ever remounts while the layout changes.**

Decisions already made (discussion 2026-08-02, against an interactive mock
that ran the rect kernel):

- **The leaf is a *group*, not a pane.** A group holds a tab list and one
  active tab; a "pane" is just a group with one tab. An unsplit window is a
  single group, which renders pixel-identically to wash-term today and makes
  the persisted-state migration a one-liner.
- **No window-level tab bar.** Tabs live only in group strips (the VS Code
  editor-group model). wash-term is `InstancingMulti` and wash has a real WM,
  so "another window" is already cheap — a third hierarchy level would earn
  its keep only for whole-layout switching. The door is left open: the
  persisted root is versioned, so wrapping it in a workspace list later is
  additive.
- **Layout is rects, not nested DOM.** Terminal hosts stay flat, absolutely
  positioned siblings for their whole life; a layout change writes
  `left/top/width/height` and nothing else. See §2 for why this is
  load-bearing rather than a style preference.
- **Controls ship with the feature, not after it.** Per-group strip buttons,
  a `Split` menubar menu, and keybindings all land in M1, over one command
  layer (§5).
- **No per-pane title/status strip.** The strip already names the pane (its
  tabs) and the window-level status bar describes the focused one. Terminal
  rows are the scarce resource.
- **The BE does not change.** Every tab is already an independent raw channel
  with its own resize, and `sdk.HandlePersist` stores the FE blob opaquely
  ("the schema belongs to the app", `internal/sdk/persist.go:38`). This is an
  FE feature end to end: no wire change, no router change.

## 1. Architecture

```
apps/term/fe/src/
  layout.ts        pure kernel — tree ops + rect assignment (unit-tested)
  layout-actions.ts one command layer: split/close/focus/zoom/equalize/move
  main.tsx         two independent <For>s over the same tree:
                     1. terminal hosts  — flat, keyed by channel id, NEVER reparented
                     2. group chrome    — strips + dividers, rebuilt freely
```

The window is one tree. Rendering walks it twice: once to place the terminal
hosts (style writes only), once to draw the strips and dividers (free to
remount — they hold no xterm state).

```
┌ menubar ─ Edit  Tab  Theme  Paste  Font  Split ─────────────────┐
├─────────────────────────────┬───────────────────────────────────┤
│ zsh │ make │      + ⊞ ⊟ ⤢   │ claude ●│        + ⊞ ⊟ ⤢          │  ← group strips (24px)
│                             ├───────────────────────────────────┤
│  terminal (channel 101)     │  terminal (channel 102)           │
│                             ├═══════════════════════════════════┤  ← divider (4px)
│                             │ logs │       + ⊞ ⊟ ⤢              │
│                             │  terminal (channel 104)           │
├─────────────────────────────┴───────────────────────────────────┤
│ zsh · 96×28 · ● claude needs input 4m                           │  ← window status bar
└─────────────────────────────────────────────────────────────────┘
```

## 2. Why rects, not a nested flex tree

The idiomatic CSS answer — nested `<div>`s with `flex` — is wrong here, for a
reason specific to xterm:

1. **Reparenting kills terminals.** Every structural change (split, close,
   move a tab to another group) would move a mounted xterm to a new parent.
   That disposes/reflows it: lost scroll position, flicker, and in the worst
   case a full remount. The `<For>` object-identity hazard already documented
   in `main.tsx` is the same family of bug, one level up.
2. **The current code is already rect-shaped.** Tab hosts render
   `position:absolute; inset:0` with `display` toggled. Splits change `inset:0`
   into a computed rect; nothing else about the host changes.
3. **Resize plumbing is free.** `<Terminal>` self-observes its host
   (`web/lib/src/terminal.tsx:856`): ResizeObserver → `safeFit()` → `onResize`
   → BE resize. Change the rect, the pty learns its new grid. `safeFit`
   already skips degenerate sizes (<10px), which is the guard a collapsing
   pane needs.
4. **Cross-group tab moves become free.** Moving a tab between groups is a
   pure tree edit — the terminal's DOM node does not move at all, so there is
   no state to preserve. This is the single strongest argument for the model.

## 3. Data model

```ts
// apps/term/fe/src/layout.ts
export type Group = { kind: 'group'; tabs: number[]; active: number };  // channel ids
export type Split = {
  kind: 'split';
  dir: 'row' | 'col';        // row = children side by side; col = stacked
  children: LayoutNode[];
  sizes: number[];           // fractions of the split's inner span, sum 1
};
export type LayoutNode = Group | Split;
```

N-ary, not binary. Three side-by-side groups stay **one** split with even
fractions, so "equalize" is trivial and repeated splitting never builds a
lopsided binary spine where dragging one divider visually moves two
boundaries. The cost is small: a divider adjusts only its two neighbouring
fractions.

**Invariants** (`normalize()` repairs all of them; every mutation returns a
normalized tree):

- `sizes.length === children.length`, all `> 0`, summing to 1 (±1e-6).
- No split with fewer than 2 children — a 1-child split collapses into it.
- No empty group — an empty group is removed and its space redistributed.
- A split never directly contains a split of the same `dir` — it is flattened
  into the parent, absorbing its fraction. This is what keeps repeated splits
  n-ary instead of degenerating to a binary chain.
- Every channel id appears exactly once in the whole tree.
- `group.active` is a member of `group.tabs`.

**Paths** address nodes: `'0'` is the root, `'0.1.0'` is child 0 of child 1 of
the root. Paths are recomputed per render and never persisted (they move when
the tree changes); persistence and identity always use channel ids.

## 4. Kernel API

Pure, synchronous, no Solid, no DOM — so it unit-tests under plain
`node:test` (no `--conditions=browser` needed; that constraint applies only to
reactive component tests).

```ts
layout(tree, rect, opts) -> { groups: PlacedGroup[]; dividers: PlacedDivider[] }
splitGroup(tree, path, dir, channelID)     // new tab lands in the new group
closeTab(tree, channelID)                   // prune + collapse + redistribute
moveTab(tree, channelID, toPath, index)     // cross-group drag
resizeSplit(tree, path, i, deltaFrac, min)  // divider drag
equalize(tree, path)
focusNeighbor(groups, fromPath, dir)        // geometric, not tree-order
normalize(tree)
```

`layout()` subtracts `GUTTER` (4px) per divider from the split's span before
distributing fractions, and hands each group a rect whose top `STRIP_HEIGHT`
(24px) belongs to the strip. `focusNeighbor` picks by geometry — the pane
whose rect overlaps the source's centre line furthest in the requested
direction — so `Ctrl+Shift+→` does what the eye expects across a nested tree,
which tree-order traversal does not.

**Minimum size** is expressed in cells and converted once: a pane must keep
≥ 20 cols × 3 rows of usable grid. Below that a split is refused (the command
no-ops with a status-bar note) rather than creating an unusable pane, and a
divider drag clamps.

**Zoom is view state, not tree state** — a `zoomPath` signal. The zoomed group
takes the whole stage rect; every other group goes `display:none`. Nothing in
the tree changes, so unzoom is exact and zoom survives nothing (deliberately —
it does not persist).

## 5. Controls

One `layout-actions.ts` command layer; all three surfaces call it, so there is
exactly one implementation of "split the focused group".

**Group strip** (24px, always visible — hover-reveal would defeat the point of
discoverable controls): the group's tabs, then `+` (new tab **in this group**),
split-right, split-down, zoom. Icons from `lucide-solid`, already imported by
`main.tsx`. The close `×` renders on the active tab, and on hover for the
others — four always-on close buttons in a 24px strip is noise, and in a narrow
pane it eats the label.

**`Split` menubar menu** — a sixth button after `Tab`, same `openMenuFor`
pattern. `MenuItem` already has `disabled` and a `trailing` slot, so greying
out and shortcut hints need no new component work:

| Item | Shortcut | Disabled when |
| --- | --- | --- |
| Split Right | `Ctrl+Shift+D` | pane would fall under the minimum |
| Split Down | `Ctrl+Shift+E` | as above |
| Zoom Pane | `Ctrl+Shift+Z` | one group |
| Equalize | — | one group |
| Next Pane | `Ctrl+Shift+→` | one group |
| Close Pane | `Ctrl+Shift+W` | one group (falls back to Close Tab) |

**Keys** go through the existing `onTermKey` (`main.tsx:687`), returning
`false` so they never reach the pty. `Ctrl+Shift+<letter>` has no distinct
control code, so nothing is stolen from the shell or from an agent.

`Ctrl+Shift+W` **changes meaning**: it closes the pane, and closes the tab only
when it is the last one in its group. This is a deliberate change to an
existing binding; note it in the release notes.

**Pane context menu** (M2): Shift+right-click is owned by `<Terminal>`
(`terminal.tsx:914`) and renders a fixed Copy/Paste menu; plain right-click is
the direct copy/paste gesture and must not change. Adding split verbs there
needs an optional `menuExtras` prop on `<Terminal>` — additive, no behaviour
change for other consumers.

## 6. Interaction details

**Focus.** `focusPath` signal + `group.active` in the tree. Click (mousedown)
on a group focuses it and calls `apis.get(channelID)?.focus()`. The focus ring
is an **inset box-shadow**, never a border — a border changes the content box,
which refits the grid underneath it.

**Divider drag** commits on release: dragging moves a lightweight overlay and
`resizeSplit` applies once on mouseup. Refitting every pane on every mousemove
is an xterm reflow storm plus a resize frame per pty per tick. Live resize can
return later behind a rAF throttle if release-only feels dead — the existing
`Splitter`'s `onChange`/`onCommit` shape already anticipates both.

**Splitter component.** `@wash/ui`'s `Splitter` is percentage-of-one-container,
built for edit's single 2-way split. The tree-driven dividers are term-local
until a third consumer appears (no premature abstraction).

**Tab drag.** Reorder-within-strip already exists (`dragId`/`dropTarget`).
M2 extends it to drop on another group's strip. M3 adds edge drop-zones on the
content area: dropping on the left/right/top/bottom quarter of a pane splits
that group in that direction and moves the tab into the new one.

## 7. Persistence

`PersistedState` (FE-owned; the BE stores it opaquely) gains a version and a
tree. The flat `tabs` array stays as the per-channel inventory — shell, color
tag, modes are already keyed by channel and are orthogonal to placement:

```ts
interface PersistedState {
  v?: 2;                    // absent ⇒ v1, migrate
  tabs?: PersistedTabRow[]; // unchanged: channel_id, shell, modes, color
  layout?: PersistedNode;   // absent ⇒ single group over `tabs`, active = `active`
  active?: number;          // v1 only; v2 keeps active per group
  font_id?: string; font_size?: number; theme_id?: string; smart_paste?: SmartPaste;
}
```

**Migration** is total and lossless: a v1 blob becomes one group whose `tabs`
is the old order and whose `active` is the old `active`.

**Restore is the risky part**, not the schema. The existing `pending`-tab
mechanism holds restored rows until the `sessions` reply arrives; with a tree,
a persisted leaf whose channel did not come back must be pruned and its space
redistributed. Concretely: build the tree from the blob → mark every leaf tab
pending → on `sessions`, drop tabs with no live channel → `normalize()` (which
collapses emptied groups and rebalances) → adopt any live channel the blob
never mentioned into the focused group. That last case matters: a tab created
by another surface (`exec_tab` from agentd, §AGENT_TERM M7) must land
somewhere.

## 8. Agent integration

The M1–M7 agent work is per channel already, so this is mostly aggregation:

- The **status bar** describes the **focused pane** — shell, grid, agent clause.
- A **tab in a strip** shows its own dot, as today.
- A **group strip** with the agent tab scrolled out of view, and any collapsed
  representation, aggregates: **needs-input beats working beats done**. A
  blocked agent is never invisible because its tab is not active.
- Roster rows (`agentd`) are keyed by channel and unaffected by placement.

## 9. Testing

**Unit — the kernel** (`layout.test.ts`, plain `node:test`): rect assignment
including gutters; every invariant in §3 after every mutation; split refusal
under the minimum; close-collapse and redistribution; same-dir flattening;
`focusNeighbor` geometry across a nested tree (the case tree-order gets wrong);
`moveTab` across groups; v1→v2 migration; restore-with-missing-channels prune.

**e2e** (`term-split.spec.ts`, the repo pattern):

- split right → two `term-host` elements with disjoint rects, both ~half width;
- type in each, assert independent output (catches a mis-wired active tab);
- split of a split → three panes with the expected geometry;
- drag a divider → both panes report new cols to the BE (assert on the
  router log, per the house e2e pattern);
- close a pane → sibling fills the space, `normalize` collapsed the split;
- reload → layout restores; kill a channel out of band → prune path;
- `Ctrl+Shift+D`/`E`/`W`/`Z` and the equivalent menu items and strip buttons
  all drive the same actions.

## 10. Milestones

- **M1 — the tree.** Kernel + rect renderer + group strips + `Split` menu +
  strip buttons + keybindings + persistence/migration. Fixed 50/50 splits, no
  divider drag, no zoom. Acceptance: split right and down, nest one inside the
  other, type in every pane, close panes back to one, reload and get the same
  layout.
- **M2 — direct manipulation.** Draggable dividers (commit-on-release), zoom,
  equalize, geometric focus keys, drag a tab between strips, pane context menu
  (`menuExtras` on `<Terminal>`).
- **M3 — drop-zone splitting.** Drag a tab to a pane edge to split there; drag
  a pane out to its own window.
- **M4 — layouts as objects.** Named/preset layouts, and the agent tie-in:
  "open Claude alongside this shell" = split-right + `exec_tab`.

Each milestone ships standalone value; M1 is the one that changes the product.

## 11. Non-goals / later

- **Workspaces** (a window-level bar above the tree). Deliberately out; the
  versioned root keeps it additive if it earns existence.
- **Broadcast input** (type into all panes at once). Real tmux users want it;
  nothing in this design blocks it, and it belongs after M2.
- **Per-pane title strips.** See the decisions list — the group strip already
  names the pane.
- **Cross-window pane moves** beyond M3's "drag out to a new window".
- **Layout sync across reconnect from a *different* browser.** The persisted
  blob already handles reconnect; two live shells editing one layout
  concurrently is a router-level question, not a term one.
