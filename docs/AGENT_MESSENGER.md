# The agent app as a messenger — one list, one window, one vocabulary

Status: **plan of record** (2026-08-24), 0.15 scope. **M5 is done**
(shipped 2026-08-24, ahead of M1 — its three defects were wrong today,
independent of any merge). This is the design
doc [AGENT_UX.md](AGENT_UX.md) §6 said phase Next must have before any
code. It settles the noun, the window count, the two lists, and the words
for a session's state.

Related: [AGENT_UX.md](AGENT_UX.md) (the audit and phase Now, all shipped
in 0.14.0 — this doc continues it), [AGENT_APP.md](AGENT_APP.md) §9 (the
three surfaces table this amends), [SIDEBAR.md](SIDEBAR.md) (the
relocation that made the app the control surface), TODO.md §Apps / UX.

---

## 1. The defect, precisely

0.14.0 fixed how you *get* to a session. It did not fix what a session
*is*, and the two questions are only separable up to a point.

Concretely, after phase Now the app still contradicts itself three ways:

1. **The window count is set by whoever happened to click.** A door
   raises the window you have; the start menu opens another; "New
   session" opens another; a detached session reattaching opens another;
   a keyed toast may open another. Nothing is wrong with any single one
   of those — but the number of Agent windows you end up with is a
   function of your click history, not of anything you decided. The app
   still declares `InstancingMulti`, and every one of those paths honours
   it faithfully.

2. **Two lists that are the same list.** The sessions pane shows what is
   running. The History panel shows what ran. They have different row
   components, different verbs, different sort orders, and one search box
   that only exists on one of them — and agentd already stamps every
   stored session with `Live` / `Detached` / `RowKey` (`rosterIndex`),
   which is the join being performed and then thrown away at the FE
   boundary. A conversation from an hour ago and a conversation from
   Tuesday are the same kind of thing.

3. **A session's state is said four different ways.** The roster dot, the
   rail summary, the terminal tab dot and the taskbar pill each map agent
   state to colour and words independently. §6 fixes the vocabulary; the
   reason it is in this doc rather than a tidy-up ticket is that merging
   the two lists forces the question — a row that can be live or stored
   needs one state vocabulary that covers both.

The through-line is the same one AGENT_UX.md §1 named and only half
answered: **the design still has not committed to sessions being the
noun.** Phase Now made every door lead to a session. Phase Next makes the
app itself be about sessions rather than about windows that contain them.

## 2. What 0.14.0 already settled (so this is not greenfield)

Half of AGENT_UX.md §6's bullet list shipped early, because you asked for
it directly while testing. Recording it here so the remaining work is
honest:

- **The sessions pane is already permanent furniture.** It is not a flap
  behind a toggle any more: always rendered, a `<Splitter>` divider like
  wash-edit and wash-fm, width persisted per window by the BE. The
  "▶ Roster" toggle and its three auto-open rules are gone.
- **Doors already focus rather than spawn.** `window.wash.focusOrLaunch`
  raises an existing window on that host and only launches when there is
  none, and the keyed path (`wash.focus` → agentd) resolves a *session*
  to a window.
- **The launcher already defaults** to your last-used agent and folder.
- **An empty window already shows the list**, so opening the app with a
  detached session no longer presents a blank form.

What is left is exactly the part that needs a design rather than a patch:
the window count, the list merge, and the vocabulary.

## 3. The principle: the list is the app, a window is a viewport

**The conversation list is the application.** It is what the app is
*about*, it is always on screen, and it is the only thing that decides
what you are looking at. A window is a viewport onto it, and the default
number of viewports is one per host.

Three consequences, each of which kills a current behaviour:

- **Opening the app never creates a second window** unless you explicitly
  ask for one. "Explicitly" means a verb that says so — *Open in new
  window* — and nothing else.
- **"New session" is compose, not spawn.** It points *this* window's
  detail pane at the launcher while the list keeps showing your live
  sessions. It is the same gesture as clicking a row, aimed at a session
  that does not exist yet.
- **The taskbar stops being the session switcher.** This is the loudest
  change and it is deliberate: `apps/ai/be/app.go` has said "the taskbar
  is still the window switcher" since M4, and that sentence is what the
  messenger frame retires. The list switches sessions; the taskbar
  switches *applications*, which is what it does for every other app.

### 3.1 What this costs, and why it is still right

The taskbar change has a real price. Today, three agent windows means
three pills, so "which of my agents is asking for me" is answerable from
the taskbar alone. With one window it is one pill, and N6's attention
flag degrades from "*this* session needs you" to "*something in here*
needs you".

That is a genuine regression in one specific case and the design accepts
it, for two reasons. The rail already answers "which one, on which host"
better than a row of identically-named pills ever did — that is what
SIDEBAR M1–M2 built. And the pill was only ever legible when you had two
or three sessions; at six it was a row of "Agent, Agent, Agent" and the
list is strictly better. §5 M4 gives the pill a count so it degrades to
"two need you" rather than to a bare dot.

### 3.2 What does not change

- Permission questions stay answerable in the rail (SIDEBAR §3.2(8)).
- The rail stays awareness-only: it merges hosts, the app does not
  (AGENT_UX §7). One window per **host**, not one window total.
- Multi-window stays possible (UC6). It stops being the accident.

## 4. The mechanism: what `Instancing` actually buys, and what it breaks

The first draft of this section was wrong, and the correction is worth
keeping because it changes the plan. I had read `EvtSpawnRequest` as
bypassing instancing; it does not — `handleSpawnRequest` → `spawnChild` →
**`launchOrRaise`** (`internal/router/app_session.go:1081`), the same
helper `shell.launch` uses.

**So instancing is honoured on every path that matters:**

| Path | Used by | Honours `Instancing`? |
|---|---|---|
| `shell.launch` → `launchOrRaise` | rail doors, `focusOrLaunch`, `launchOn` (incl. remote hosts) | yes |
| `EvtSpawnRequest` → `spawnChild` → `launchOrRaise` | start menu (via session BE), agentd's reattach / resume / focus | yes |
| control socket → `controlLaunch` | `wash launch` | yes |
| `spawnForOpen` (`--open`), `prepare_spawn` (wash-priv), autoboot | file open-routing, privilege escalation, boot | **no** — these are the escape hatches |

`launchOrRaise` dedups on `Instancing != multi`, matching **by app id
only**, and raises the instance's *primary* window. Per-router, so
"one per host" comes free — B's router scans B's own table, and the
remote path needs no origin concept at all.

**Decision: `com.wash.ai` becomes `singleton`, not `single`.** Both make
`launchOrRaise` raise instead of spawn. Singleton additionally makes
`{app_id: "com.wash.ai"}` a legal recipient (`resolveRecipient` refuses
app-id addressing for anything else), and that is worth more than it
sounds — see below.

### 4.1 The thing that actually breaks: agentd's positional attach queue

This is the real work, and it is not a launch problem.

agentd opens a window onto a session in two steps: push the session key
onto a FIFO (`pendingAttach`), then `SpawnRequest(aiAppID)`; when the
spawn reply arrives, `onSpawnResult` pops the oldest key and sends
`{kind:"attach", key}` to the returned instance id. It is positional —
"spawn replies arrive in the order they were requested" — and it has
three producers (reattach, resume, focus).

Under a raise, `EvtSpawnOk` carries the **existing** instance id. So the
attach lands on the window you are already looking at and re-points it.
Note `attach` has no same-key guard, unlike `select`
(`apps/ai/be/app.go:369`), so it will happily re-subscribe to the session
it is already showing.

Re-pointing the one window *is* what the messenger model wants. The
problem is that it would happen by accident, through a queue whose
correctness argument ("replies arrive in order") stops being true the
moment a reply can be synchronous.

**So: delete the FIFO.** With `singleton`, agentd addresses the window
directly — `SendAppMsgTo({app_id: "com.wash.ai"}, {kind:"show", key})` —
and the router spawns it on first reference if it is not running
(`resolveRecipient` does exactly that for singletons). One message, no
queue, no positional matching, and the same code path whether the window
existed or not. The three producers collapse into one call.

### 4.2 The pop-out needs a bypass, and it must be a second *instance*

Once both launch paths dedup, *Open in new window* needs a way to say
"another one, deliberately" — a `force` flag on the spawn request. That
is the one wire addition in this plan.

It must create a second **instance**, not a second window of the same
instance, because ai's backend is written for exactly one: `session` is a
package-level struct ("One window, one session — hence a package-level
value rather than a map", `apps/ai/be/app.go:181`) and closing a window
calls `os.Exit(0)` (`finishClose`, `app.go:681`). One process serving N
windows would need a per-window session map and a close path that does
not kill its siblings — a much larger change, for a feature (side-by-side)
that a second process serves perfectly well.

The cost of a second instance under `singleton` is that it is a
*deliberate* violation of the manifest's own claim. That is acceptable
only if it is rare, explicit and user-initiated — which is exactly what
*Open in new window* is — and it should be logged when it happens.

## 5. Milestones

Ordered so the risky structural change (M2) lands after the cheap merge
(M1) has validated that one row component can serve both lists.

### M1 — one list, live and stored

Merge the History panel into the sessions pane. One row component, one
sort, one search box.

- **Rows:** live sessions first (agentd's `statePriority` already sorts
  needs-input above working above done), then stored ones newest-first.
  A stored row is a row state, not a different widget — the "unfinished"
  marker survives as one.
- **Search:** one box filters both. Live rows match on metadata; stored
  rows go through `agent_history`, which since 0.14.0 is a trigram-indexed
  full-text search over transcripts. Typing narrows the whole corpus, not
  just the visible part.
- **Verbs stay key-addressed** and per row state, which `historyAction`
  already models: focus a live one, reattach a detached one, resume a
  finished one.
- **Default depth:** live sessions plus recent stored ones, not the whole
  archive. The archive is what search is for.

**What the merge actually costs**, from reading both sides rather than
assuming they are symmetrical:

- **The stable core is small and real:** `key`/`row_key` (the same value
  under two names), `agent`, `title`, `dir`/`cwd`, `live`, `detached`.
  One component can carry that.
- **The click predicate already unifies.** `historyAction` (resume /
  reattach / focus / none) is a strict superset of the roster's
  `detached ? reattach : activate`. Keep `historyAction`; delete the
  other.
- **Time is the hard part, and it is a live footgun.** There are *three*
  representations of "when": `since_ms` (elapsed in the current state),
  `last_seen` (unix **seconds**, on the `recent` list), and
  `started_ms`/`ended_ms` (epoch **milliseconds**). There are also two
  `fmtAgo` functions with identical output and incompatible inputs —
  `agent-roster.tsx` takes seconds, `HistoryPanel.tsx` takes
  milliseconds. Merging the rows without first merging the clocks is a
  1000× error waiting to render. **Do the clocks first**, as their own
  commit, with tests.
- **A live row with no `session_id` cannot be joined.** `rosterIndex` is
  keyed by the agent's own session id, so a T0-detected row without one
  is invisible to the join. Decide explicitly whether such a row appears
  once (live only) or risks appearing twice.
- **"running" means two different things in the two lists** — a *state*
  in the roster (detected, not in a turn) and a *liveness word* in
  History ("running — go to it", said of a row whose state may be
  `working` or `done`). A naive merge renders "running — go to it" beside
  a dot that says `working`. This is why M5 is in the same plan and not a
  follow-up: the merge forces the vocabulary.

*Risk:* if that reconciliation turns ugly, it is telling us the lists are
not the same list, and M2 should not proceed on the assumption that they
are.

### M2 — one window per host

The structural change. `com.wash.ai` becomes `singleton`, agentd's
positional attach queue is deleted in favour of app-id addressing, and
the explicit pop-out lands with its `force` flag (§4).

Order within the milestone matters: **delete the FIFO first**, while the
app is still `multi` and a spawn really is a spawn. `{app_id}` addressing
of a non-singleton is refused by `resolveRecipient`, so that step has to
be paired with the manifest flip — but the `show`-verb refactor on both
sides can land and be tested before it.

Then, in the same change:

- ai's "New session" button stops calling `launchOn` — under a raise it
  would be a no-op on itself. It becomes M3's in-place compose, so M2 and
  M3 ship together or M2 ships a dead button.
- *Open in new window* on a row, and nowhere else.
- **Window restore checked, not assumed:** a reload must bring back one
  window on the session it was showing, and must not produce a second.
  The FE path is a remount, not a re-creation — the router keeps windows
  and app processes alive across a browser reload — so the risk is in
  ai's own `restore` handler, which compares the persisted key against
  its package-level `session.key`.

*What breaks, by name:* `e2e/tests/agent-roster-pane.spec.ts` opens a
second window via the start menu at lines 67 and 111, and its test *"New
session opens another window rather than hijacking this one"* (line 187)
is the written spec of the behaviour being removed. That test should be
rewritten to assert the opposite — one window, re-pointed — in the same
commit, not deleted quietly.

*Risk:* this is the milestone that can break "two agents side by side".
The pop-out must land with it, not after it. And `prepare_spawn` /
`spawnForOpen` still bypass instancing, so a privileged spawn or an
`--open` route could still produce a second ai; neither is reachable for
this app today, but the invariant is asserted, not enforced.

### M3 — compose in place

"New session" stops opening a window and points this window's detail pane
at the launcher.

The blocker is real and specific: the window's BE keeps `session.key` and
its transcript subscription, so clearing the view locally would leave it
streaming the old session's events into what looks like a fresh one —
which is exactly why 0.14.0's button opens a window instead. The fix is a
`view_none` verb that sends agentd `transcript_unsubscribe` (the handler
already exists, `apps/agentd/be/transcript.go:525`) and clears the key,
with `persistSessionView` recording the empty view so a reload agrees.

*Risk:* unsubscribing is not detaching. The session must keep running and
keep its row; only this window's view is released.

### M4 — the taskbar tells the truth about one window

With one window, the pill carries a count rather than a boolean: "2 need
you". N6's attention flag stays the mechanism; what changes is that
wash-ai computes the number from the roster rather than from its own
session, and the pill renders it.

### M5 — one status vocabulary — **DONE** 2026-08-24

*As built:* `web/lib/src/agent-status.ts` is the single source; the three
old switch statements are gone and `stateColor`/`stateLabel` survive as
thin adapters (public @wash/ui surface). All three defects fixed and
pinned by tests. One deviation: `awareness.ts` keeps string literals
rather than importing the predicates — it is dependency-free so it can
run under plain `node --test`, and @wash/ui's index uses bundler-style
imports node cannot resolve. `awareness-vocabulary.ctest.ts` imports both
sides and fails the build if they drift, which is the guarantee the
import would have given.

One table, every renderer, no disagreements. Worth doing early rather
than last if M1 slips: two of the defects below are wrong *today*,
independent of any merge.



| State | Colour | Word | Where |
|---|---|---|---|
| needs-input | accent + pulse | "needs you" | list row, rail, taskbar, term tab |
| working | neutral spinner | "working" | same |
| done | dim check | "done" | same |
| failed | red | "failed · <reason>" | same — today this renders as `done`, green |
| detached | dim, no pulse | "running, no window" | list row, rail |
| stale | grey | "not responding" | list row, rail |
| stored | none | (ended, with elapsed) | list row only |

The existing maps (`stateColor` / `stateLabel` in
`web/lib/src/agent-roster.tsx`, the rail's `awareness.ts` summaries, the
term tab dot, `agent-session.tsx`'s status line) collapse onto this one.
Anything that cannot be said in these rows is a state we should not be
rendering.

**This is not a tidy-up: the drift is hiding three real defects.**

- **The rail counts a dead agent as a working one.** `agentSummary`
  computes `working` as `rows.filter(r => r.state !== 'needs-input')`, so
  `stale` — a terminal that stopped reporting — and `done` both inflate
  "N agents working", in the rail and in the AgentOpen door label. The
  rail's headline number is wrong in exactly the situation you would want
  it to be right.
- **A failed session renders green.** An adapter error ends a session as
  `done` with `reason: error`, and every surface paints `done` green.
  `accentRed` appears in agent colouring only for a failed *tool call*,
  never for a failed session. The table above needs a **failed** row, or
  failure stays invisible.
- **`stale` cannot be expressed at all in wash-term.** Its `AgentStatus`
  type is `'running' | 'working' | 'needs-input' | 'done'`, so a
  not-responding agent in a terminal tab is indistinguishable from a
  running one.

Two smaller ones worth fixing in the same pass: `working` has no colour
in the Agent window's own status line (it swaps the dot for a spinner, so
the roster's loudest signal is absent from the app itself), and
`accentAmber` currently means four unrelated things — needs-input,
"reattach available", "unfinished transcript", and "unread warn/error
notification". On a merged list a detached row and a blocked row would
both be amber for different reasons, which is precisely the confusion M1
would otherwise ship.

## 6. What could go wrong

- **The list becomes a file manager.** A conversation list with sort
  options, columns and filters is a different, worse product. If M1
  starts growing controls, the answer is search, not chrome.
- **One window makes side-by-side feel punished.** Watch for the pop-out
  being two clicks deep or its window behaving like a second-class one.
  If comparing two agents gets harder than it is today, M2 has failed
  even if every test passes.
- **Compose-in-place loses work.** M3 releases a view while a turn may be
  streaming. The session keeps running by construction, but the *user*
  must be able to tell that — the row it left behind has to visibly still
  be live.
- **The instancing change leaks.** Teaching the launcher path to raise
  affects every single-instance app in the tree. That is the point, but
  it needs its own regression pass, not just agent specs.

## 7. Testing

Per the house rule, both halves — FE state and the router log or the
filesystem:

- **M1:** a component test that one row component renders live and stored
  rows with the right verbs; an e2e that a word said in a finished
  session finds it in the same list that shows the running ones.
- **M2:** an e2e that opening the app twice, from the start menu and from
  the rail, yields exactly one window — and that *Open in new window*
  yields exactly two. Plus a reload that restores one window on the
  session it was showing.
- **M3:** an e2e that compose-in-place leaves the previous session running
  (its row still live in the list, agentd never logs a detach) and that
  the new session's transcript contains none of the old one's events.
- **M4/M5:** component tests over the vocabulary table, including the
  three defects by name — a `stale` row must not be counted as working by
  `agentSummary`, a session that ended with `reason: error` must not
  render green, and wash-term must be able to express `stale` at all. A
  screenshot pass is the honest check for "legible at three sizes".

## 8. Non-goals

- No change to permission policy or the ask ladder — AGENT_UX §4 stands.
- No cross-host merging inside the app. One window per host; the rail is
  where hosts merge.
- No fork verb (HISTORY's reasoning is unchanged).
- No one-process-many-windows. `single` in the wire means "one process
  serves many windows" and that is explicitly not what this plans for:
  ai's backend keeps one session per process, and the pop-out is a second
  process. Revisit only if side-by-side needs shared state.
- No archive management UI — no bulk delete, no retention settings. The
  store grows unbounded today and that is a separate, honest problem
  (`SessionMeta.Bytes` exists for whoever picks it up).
