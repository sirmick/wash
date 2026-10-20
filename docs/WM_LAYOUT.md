# Window layouts (panels) — design + implementation plan

Goal, in one line: **any viewport cell can be given a layout — `r3`, a 2×2,
25/50/25 — and the windows in it tile into its panels, with draggable
dividers, while every other cell stays the floating desktop it is today.**

Decisions already made (discussion 2026-10-20):

- **Opt-in per viewport cell.** wash stays a floating WM ("chosen over
  tiling", `docs/ARCHITECTURE.md`). A layout is a property of one of the
  nine pager cells; a cell without one behaves exactly as now.
- **One kernel with wash-term.** The splits — paths, flattening, fractions,
  rects, dividers, geometric focus — are `@wash/ui/split-tree`, extracted
  from term's split panes. Term's leaf is a tab group; the WM's is a slot.
- **One syntax, and nobody has to type it.** Presets, divider drags and
  saved layouts all go through `parseLayout`/`formatLayout`. People pick
  presets from a menu and drag dividers; the string is the stored form and
  what a power user can type in Settings.
- **Real geometry, not CSS.** Tiled windows stay in state `normal` and get
  real `window.move`/`window.resize`, so backends and `wash-display` guests
  learn their size. (Maximize is CSS-only and never tells the app; tiling
  must not copy that.)
- **Live, not one-shot.** Closing a window, toggling the sidebar or
  resizing the browser re-places the panels. Empty panels stay — they are
  drop targets.

## 1. Architecture

```
web/lib/src/
  split-tree.ts        the kernel: Split<L>, normalize, place, resizeSplit,
                       focusNeighbor, parseLayout/formatLayout (unit-tested)
apps/term/fe/src/
  layout.ts            term's leaf (tab Group) + tab ops over the kernel
web/shell/src/
  tiling.ts            (M2) cell layouts, slot assignment, work area → rects
  viewport-math.ts     work-area helper lives beside the other rect maths
```

The kernel is imported by subpath (`@wash/ui/split-tree`), not the package
index: the index pulls in Solid components, and `node --test` must be able
to load the kernel and everything built on it without them.

## 2. The kernel

`Node<L> = L | Split<L>`. A split is `{kind:'split', dir, children, sizes}`
with `dir` `row` (side by side) or `col` (stacked) and `sizes` fractions of
the span less gutters, summing to 1. A leaf is any object whose `kind` is not
`'split'` — term's groups keep `kind:'group'`, so its persisted blob did not
change.

`normalize(node, ops)` repairs every split invariant (positive fractions
summing to 1, ≥2 children, no same-direction nesting — flattened into the
parent, keeping proportions). `LeafOps` is what differs per caller:

| | term (`GROUP_OPS`) | WM (`SLOT_OPS`) |
|---|---|---|
| `keep` | drop emptied groups, siblings absorb the space | omitted — empty slots survive |
| `repair` | `active` must be one of `tabs` | omitted |
| `empty` | an empty group | a slot |

`place(tree, rect, gutter)` gives leaf rects and divider rects (with the span
a drag divides). Callers cut their own chrome from a leaf rect: term takes
its tab strip and status bar off; the WM takes the 28px title bar and 1px
borders off to get the content size it sends as `window.resize`.

## 3. Syntax

```
node := "1"                         one panel
      | ("r" | "c") N               N equal panels: r = a row (side by side),
                                    c = a column (stacked)
      | ("r" | "c") "[" item+ "]"   items separated by whitespace
item := node ["@" pct]              percent of the parent, decimals allowed
```

| Preset | String |
|---|---|
| Single | `1` |
| Halves | `r2` |
| Thirds (1-1-1) | `r3` |
| 25/50/25 | `r[1@25 1 1@25]` |
| 2×2 | `r[c2 c2]` |
| 3×3 | `r[c3 c3 c3]` |
| Main + stack | `r[1 c2]` |
| Top + two | `c[1 r2]` |

`r`/`c`, not `h`/`v`: vim and tmux call side-by-side a *vertical* split, so
either reading of `v` surprises half the audience. `r`/`c` match the kernel
and CSS flex.

**Sizes never fail.** Unsized items share what the sized ones leave; a
split whose sizes do not come to 100 is scaled until they do (with unsized
items present and nothing left, every item counts as an even share first).
Only a malformed string is an error (`LayoutSyntaxError`, with the offset).

**One canonical form.** `formatLayout` writes sizes to 0.1%, omits them when
a split is even, and writes `rN`/`cN` for an even split of bare panels;
normalize has already flattened same-direction nesting. So two trees that
look the same format the same, and "is this cell still on the 2×2 preset"
is a string compare against `formatLayout(parseLayout(preset))`. Divider
drags snap to 0.1% so the stored string is exactly what is on screen.

Deliberately not in the syntax (yet): fixed pixels (`1@400px`) — additive
later as a unit suffix; it fits the "unsized gets the rest" rule unchanged.

## 4. Window model (M2)

- **Shape and occupancy are separate.** A cell's layout is
  `{shape: string, slots: (WindowKey | null)[]}` — slot *i* is the *i*-th
  leaf in tree order (`leafPaths`). Windows move between slots far more
  often than the shape changes. `WindowKey` is `(origin, windowID)`:
  windows come from several hosts.
- **Applying a layout** fills slots by most-recent focus among the cell's
  windows. Maximized/minimized windows are restored first (the router
  ignores moves for them).
- **More windows than slots:** the extras stay floating. **Fewer:** the
  remaining slots stay empty and render as drop targets.
- **Moving into a slot swaps** with its occupant (an empty slot is a plain
  move). The shape never changes as a side effect — that is what keeping
  shape and occupancy separate buys.
- **Membership follows the centre, whatever moved it.** A window belongs to
  the cell holding its centre (`viewportForRect`), and "send to viewport"
  is only a move by whole screens (`sendToViewportRect`). So tiling reacts
  to *a window's cell changed* — menu, drag across a cell edge, an app
  moving its own window — not to the menu item:
  - **Leaving a tiled cell** empties its slot and returns the window to
    its pre-tile floating rect. That rect is recorded when it enters a
    slot, in the cell's layout blob — not the router's `Restore*`, which
    belongs to maximize.
  - **Arriving in a tiled cell** takes the first empty slot in tree order;
    with none free it floats, as the overflow rule says.
- **A window leaves its slot** when it is closed, minimized, dragged out by
  its title bar, or moved to another cell. Its slot goes empty; the shape
  does not change.
- **Minimum sizes win over fractions.** A divider drag clamps at the
  window's `minW/minH` (`minFractionFor`); a layout whose slots would be
  smaller than a window's minimum leaves that window floating.

## 5. Work area

The usable area is hard-coded today: the 40px taskbar in several places,
the sidebar via `--wash-reserved-right`. M2 starts by adding one helper —
`workArea(cell, screen, reservedRight)` beside `viewport-math.ts` — and
moving the existing callers (`moveHome`, `fitLeftOf`, maximize's CSS) onto
it. Tiling re-places a cell when that area changes.

## 6. Storage

Layouts are stored by the router so every browser on a session agrees,
using the session's existing app-state blobs keyed by cell — no wire
change. Geometry still flows only through `window.move`/`window.resize`.

**The head places; anyone edits.** Screen size is per browser but the plane
is shared, so two tabs re-placing the same windows from different screen
sizes would fight. wash already has the answer: the router's foreground
**head** shell (`router.go` `headShell`; `shell.head` /
`shell.superseded` / `shell.claim_head`) is the one driver of terminal
channels (`docs/PTY_ROBUST.md` Fix A), and `wash-display`'s single output
is sized by it (`republishDisplayState`). Tiling follows the same rule:

- The layout *data* — shape, slot assignment, pre-tile rects — may be
  edited by any tab.
- Only the head *computes geometry* and sends the moves/resizes. It
  re-places a cell when that cell's data changes (from any tab), when its
  own work area changes, and when it **gains the head** — so "Use here" on
  a laptop reflows the panels to the laptop, as the display does.
- A non-head tab renders what the router echoes. The one local exception is
  a divider drag in progress, which moves windows live for feedback and
  commits fractions to the data on release.
- Head is a local-router concept, so the local head places every window in
  a tiled cell, remote-host windows included.

## 7. Controls

- **Layout menu**, one menu reachable from three places: right-click the
  desktop (the current cell — but a fully tiled cell has no desktop left
  to click), right-click empty taskbar (the current cell), right-click a
  pager cell (that cell, without going there). Presets with diagrams, the
  current one ticked; Float (no layout); Even out panels; Edit layout….
  The taskbar and pager live in the session app, so this goes through a
  `window.wash.setCellLayout(cell, shape)` API.
- Title-bar context menu (`window.tsx`, shared with the taskbar pill's
  right-click): **Move to panel ▸** (a mini diagram, click a slot),
  **Swap with main**, **Float**.
- Dividers: an overlay in the shell over the cell's gutters (term's drag
  pattern, `apps/term/fe/src/main.tsx` ~750–790, copied rather than shared:
  the shell's dividers sit above windows, not inside one app).
- Keys (Super stays untouched):
  - Ctrl+Alt+Arrow already pans the viewport. In a tiled cell it moves
    focus to the neighbouring panel by geometry (`focusNeighbor`) and pans
    only when there is no panel further that way — i3's focus-across-
    outputs. Floating cells are unchanged.
  - Ctrl+Alt+Shift+Arrow swaps the window with that neighbour. Free in the
    shell today, but some host desktops grab it (GNOME: move to
    workspace), so the menu and drag remain the dependable paths.
- Drag a window's title bar over a tiled cell: the slot under the pointer
  highlights; drop swaps with its occupant. Shift-drop, or dropping
  outside the cell, floats it.
- Later: Rotate windows (every window one slot along).
- Settings: the cell's layout string, editable, errors shown with offset.

## 8. Milestones

1. **Kernel extraction** — done. `web/lib/src/split-tree.ts`; term's
   `layout.ts` is its leaf layer, and its 35 tests pass unchanged.
2. **Syntax** — done. `parseLayout`/`formatLayout` in the kernel, tested in
   `web/lib/src/split-tree.test.ts`.
3. **Work area helper** and its callers.
4. **Arrange (one cell, live)** — menu, slot assignment, re-placement on
   close/resize/sidebar, storage.
5. **Dividers, drag-to-slot, focus keys.**
6. Later: term gets **Layout ▸** presets for its own splits from the same
   syntax; fixed-px sizes; per-cell layout in the pager thumbnails.

## 9. Open questions

- Gutter between tiled windows: 0 (borders touch) or a few px of wallpaper
  that doubles as the divider's grab area.
- Whether "Arrange" on a cell that already has a layout keeps window
  placement (reshape) or refills by focus order.
