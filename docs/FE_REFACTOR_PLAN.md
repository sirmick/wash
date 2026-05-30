# FE monolith refactor — design + commit plan

Target: the giant `App()` closures in `apps/fm/fe/src/main.tsx` (2661 lines,
~1718 in the closure: 34 signals/stores + 86 handlers) and
`apps/edit/fe/src/main.tsx` (3017 lines), which also duplicate a whole
file-tree subsystem between them.

This is a design + sequenced plan. **Not yet executed.** Decision points
that need your call are flagged with **[DECIDE]**.

---

## 1. What `App` actually is

`App` is the entire file-manager client mounted inside the `wash-app-fm`
custom element. It owns four concerns fused into one closure:

1. **Outbound protocol** — `send(msg)` → `window.wash.sendAppMsg(instance, msg)`
   → WS → router → Go BE. Plus `sendWithReply`: mints `f-<n>`, stashes a
   resolver in `pendingReplies`, returns a promise.
2. **Inbound protocol** — the BE's messages arrive as `wash:msg` CustomEvents
   on the host element; `handleBE` is the dispatcher. It (a) resolves
   correlated replies by echoed `id`, and (b) `switch`es on uncorrelated
   pushes (`list_ok`, `read_ok`, `fs_event`, …). `fs_event` is a genuine
   server push: the BE watches dirs the FE subscribed to and notifies on
   disk change.
3. **State** — 34 signals/stores (path, listings, expanded, selection,
   sort, clipboard, menus, overlays, …).
4. **Controller + view wiring** — 86 handlers translating user intent into
   requests, plus `onMount` (host-event subscription, global keyboard,
   persistence via `wash:state`) and the JSX.

The view layer is already fine: 16 module-level components live *below* the
closure (`TreeRow`, `PreviewPane`, `InfoSection`, `ContextMenu`, …) plus
pure helpers (`joinPath`, `humanSize`, `octalPerm`). The monolith is the
**logic core**, not the views.

```
            wash:msg (CustomEvent on host)
   BE ───────────────────────────────────►  handleBE ─► setState ─► Solid re-renders
       ◄───────────────────────────────────  send()   ◄─ handlers ◄─ user input
            sendAppMsg → WebSocket             ▲
                                               └─ sendWithReply / pendingReplies
```

## 2. What `edit` duplicates (verified)

`edit` independently reimplements, with near-identical bodies:
`pendingReplies` + `sendWithReply`, `handleBE` reply-correlation skeleton,
`readDragPaths`, `onRowDragOver/Drop`, `onListDragOver/Drop`, `commitMove`,
`startRename`/`cancelRename`, `watching` + `sendFsWatch/Unwatch`,
`scheduleRefresh`, `walk`, `toggleExpand`, `followSymlink`, `dirOfSelection`.
(`edit` even has comments like "mirrors fm's dirOfSelection".) Per
[[no premature service]] + [[wash UI lib deferred]], the second consumer is
the trigger to extract — that condition is now met.

---

## 3. Testability — the central lever

**Repo precedent:** `web/shell/src/*.test.ts` use `import {test} from 'node:test'`
+ `node:assert`, run via `npx tsx --test`. No DOM, no jsdom, no vitest. So:

- **Pure logic (no DOM, no Solid)** → trivially unit-testable today, the
  cheapest and fastest tier. Maximize how much code lands here.
- **Solid stores without DOM** → `createSignal`/`createStore` run fine under
  Node inside `createRoot(dispose => …)`; only `render()` needs a DOM. So
  controllers built on stores ARE unit-testable, just with a `createRoot`
  wrapper and a fake bus.
- **Real DOM / layout / focus / native drag** → must stay in Playwright e2e.

What this buys: the protocol-correlation, debounce, drag-path parsing,
sort/filter, and path helpers — currently exercised *only* through the
browser e2e suite — move into millisecond Node tests. e2e shrinks to true
integration (focus management, real drag gestures, splitter layout,
cross-window clipboard sync).

### Two testability tiers for controllers — **[DECIDE]**

The nav/tree/mutation/clipboard controllers manipulate state. Two ways to
build them, trading indirection for test purity:

- **Option A — Solid-store controllers.** `createNav(state, bus)` reads/writes
  Solid stores directly. Tested under `createRoot` with a fake bus. Less
  code, idiomatic Solid, but tests need the Solid runtime and these can't be
  shared without "shared Solid" (which [[wash UI lib deferred]] cautions
  against).
- **Option B — framework-free state machines.** Controllers operate on an
  injected `{get, set}` state interface, not Solid directly; fm/edit each
  supply a thin Solid-backed adapter. Controllers become pure → testable
  with plain `node:test` (no `createRoot`) AND shareable without dragging
  Solid into the shared package. More upfront indirection.

Recommendation: **B for the framework-free pieces that get shared** (bus,
watch, dnd-parse, helpers — these have no reason to touch Solid), **A for
app-specific controllers** that stay in-app (fm's clipboard, edit's tab
model). This keeps the shared surface Solid-free and maximizes the cheap
test tier, without over-abstracting app-local logic.

### Shared-code placement — **[DECIDE]**

The framework-free extractions (bus, watch, dnd-parse, fs path helpers) need
a home both apps import. Options:
- `@wash/ui` subpath (e.g. `@wash/ui/fs-client`) — one shared package, but
  it currently holds UI primitives; logic there blurs its purpose.
- new `@wash/fs-client` workspace package — clean separation, one more
  package to wire into the build.

Recommendation: **new `@wash/fs-client`** — it's app logic, not UI, and the
build cost is one `package.json` + the existing externalize pattern.

---

## 4. Target architecture (fm)

```
apps/fm/fe/src/
  main.tsx            // App: instantiate state+controllers, onMount wiring, <Layout/>
  state.ts            // createFmState(): the stores, collapsed from 34 signals
  controllers/
    nav.ts            // selectPath, navigateTo, go{Home,Back,Forward,Up}, expandPath, findEntry
    mutations.ts      // rename, new file/folder, delete, chmod/chown  (+ withReplacePrompt)
    clipboard.ts      // copy/cut/paste, commitBulkCopy
    derived.ts        // sortedFiltered, treeRoot, visibleRows, statusBar (memos)
  view/               // (optional) split the JSX out of main.tsx
    Toolbar.tsx, Tree.tsx, ... + the existing TreeRow/PreviewPane/etc.

@wash/fs-client/src/   // SHARED, framework-free, node:test-covered
  bus.ts              // request/reply correlation, timeout, dispatch
  watch.ts            // watching set + scheduleRefresh debounce (injected clock)
  dnd.ts              // readDragPaths + accept/decide logic (DataTransfer-shaped input)
  paths.ts            // joinPath, parentPath, baseName, humanSize, octalPerm, formatDate
  sort.ts             // sortedFiltered comparator (pure)
```

`bus.ts` shape (framework-free, the keystone):
```ts
export interface Bus {
  send(msg: unknown): void;
  request(req: object, timeoutMs?: number): Promise<BEMessage>;
  dispatch(msg: BEMessage): void;          // call from wash:msg handler
  onPush(handler: (m: BEMessage) => void): void;  // uncorrelated messages
}
export function createBus(send: (m: unknown) => void, clock?: Clock): Bus
```
Pure: inject `send` + a clock; no DOM, no Solid. The `App` wires
`host.addEventListener('wash:msg', e => bus.dispatch(e.detail))` and registers
the app's push-switch via `bus.onPush`.

---

## 5. Commit-by-commit plan

Principle: **every commit builds and keeps the fm e2e suite green.** Each
extraction is cut-and-wire (move code, add a param), not a logic change.
Run `tsc` + the relevant e2e spec after each. New Node tests are added in
the same commit as the code they cover.

### Phase 0 — test infra
1. **Wire FE unit tests into the harness.** Add a `test:fe` step to
   `test.sh`/`Makefile` that runs `npx tsx --test` across `web/**` and
   `apps/**/fe/**` `*.test.ts`. Establishes the lane the precedent files
   already assume. (No product code touched.)

### Phase 1 — pure helpers (lowest risk, immediate coverage)
2. **Extract fm path/format helpers** → `@wash/fs-client/src/paths.ts`
   (or in-app `lib/paths.ts` first if deferring the package **[DECIDE]**)
   + `paths.test.ts`. Re-point fm imports. No behavior change.
3. **Extract the sort/filter comparator** → `sort.ts` + `sort.test.ts`
   (covers hidden-file filtering, each SortKey, dir-before-file). This logic
   is currently only reachable through e2e column-sorting tests.

### Phase 2 — the bus (the keystone, framework-free)
4. **Extract `createBus`** → `bus.ts` + `bus.test.ts` (fake send + fake
   clock: correlation by id, timeout synthesises `timeout_err`, uncorrelated
   → `onPush`, no resolver leak). fm's `App` uses `bus.request`/`bus.dispatch`;
   its push-`switch` registers via `onPush`. Delete fm's inline
   `sendWithReply`/`pendingReplies`.

### Phase 3 — watch/debounce + dnd parsing
5. **Extract `createWatch`** (watching set + `scheduleRefresh` debounce,
   injected clock) → `watch.ts` + `watch.test.ts` (coalescing, skip-unexpanded
   rule). Wire fm to it.
6. **Extract `readDragPaths` + drop-accept logic** → `dnd.ts` + `dnd.test.ts`
   (fake DataTransfer object). Thin DOM event handlers stay in `App`/views and
   call into it.

### Phase 4 — state + controllers (fm)
7. **Introduce `createFmState()`** — collapse the 34 signals into grouped
   stores. Pure mechanical; `App` reads the same values. No new tests (state
   itself is trivial); verify by build + full fm e2e.
8. **Extract `createNav`** (Option A or B per **[DECIDE]**) + `nav.test.ts`
   (history push/back/forward, expandPath ancestor walk, selectPath emits
   the right `list`).
9. **Extract `createMutations`** + tests (rename/new/delete/chmod/chown emit
   correct messages; `withReplacePrompt` retry-on-exists logic).
10. **Extract `createClipboard`** + tests (copy/cut/paste target resolution,
    cross-window clipboard payload shape).
11. **Extract `createDerived`** (the memos) — verify under `createRoot`.

### Phase 5 — App slims to wiring
12. **Reduce `App` to:** instantiate state + controllers, `onMount`
    (host-event subscription, keyboard map, persistence), render `<Layout/>`.
    Optionally split the JSX into `view/` sub-components (Toolbar, Tree,
    InfoPane) — these stay e2e-covered. Target: `App` well under ~300 lines.

### Phase 6 — de-duplicate edit against the shared package
13. **Point `edit` at `@wash/fs-client`** for bus + watch + dnd + paths;
    delete edit's copies. The shared tests now cover what edit's e2e used to
    be the only guard for.
14. **Apply the state+controller split to `edit`** (its own `createEditState`
    with the tab/dirty model; reuse shared bus/watch). Mirrors fm's structure
    so the two apps finally share shape, not just copy-paste.

### Phase 7 — optional, later
15. Consider `session`/`top` (next-largest closures) with the same playbook
    if the pattern proves out. Out of scope for this plan.

---

## 6. Risk & verification

- **Per-commit gates:** `tsc --noEmit` in the touched package, `npx tsx --test`
  for new unit tests, and the fm (then edit) Playwright specs
  (`fm-shortcuts-clipboard`, `fm-columns`, `fm-watch`, `fm-replace`,
  `single-file`). A regression is contained to one small commit.
- **Known e2e flake:** [[wash sidebar e2e order]] — 3 fm-related specs pass
  alone but fail in the full suite (not root-caused). Run fm specs in
  isolation when validating these commits so the refactor isn't blamed for a
  pre-existing ordering issue. Also watch [[e2e orphan accumulation]]
  (interrupted runs leak children → inotify exhaustion → silent fs.watch
  break); check process count before blaming code.
- **Behaviour-preserving:** no commit changes wire messages or UX; each is a
  move + parameterize. The push-`switch` bodies move verbatim into `onPush`.
- **Net effect on the test pyramid:** protocol/debounce/dnd-parse/sort/path
  logic shifts from slow browser e2e into fast Node tests; e2e keeps only
  what genuinely needs a browser.
```
