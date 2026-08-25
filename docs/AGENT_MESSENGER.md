# The agent app as a messenger — one list, one window, one vocabulary

Status: **plan of record** (2026-08-24), 0.15 scope. This is the design
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
   function of your click history, not of anything you decided.

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

## 4. The mechanism problem: who decides a window exists

This is the part that must not be designed by assumption, because the
router does not behave the way "set `Instancing` and be done" assumes.

**Instancing is honoured on one of the two spawn paths.**

| Path | Used by | Honours `Instancing`? |
|---|---|---|
| `shell.launch` → `handleLaunch` → `launchOrRaise` | rail doors, wash-connect, `focusOrLaunch` | **yes** |
| `EvtSpawnRequest` → `handleSpawnRequest` → `spawnChild` | the start menu (via the session BE), **agentd's reattach / resume / focus** | **no** — it spawns unconditionally after a capability check (`internal/router/app_session.go:920-935`) |

So flipping `com.wash.ai` to `InstancingSingle` would change the doors —
which already behave — and leave every path that actually multiplies
windows untouched. The manifest is the wrong lever.

**Decision: the policy lives where the knowledge is.** Two owners, split
along what each can actually know:

- **agentd owns "does this session need a window?"** It is the only party
  that knows whether a session has one (`transcriptWatchers`), lost one,
  or never had one — this is the same lesson N1 taught when its keyed
  half turned out to belong in agentd rather than the shell. Its three
  `SpawnRequest(aiAppID)` sites (reattach, resume, focus) collapse into
  one helper that raises an existing window and points it at the session,
  and spawns only when there is none.
- **The router owns "is this app single-window?"** for the generic case,
  by teaching the *launcher* path what the door path already knows: the
  session BE's `{"action":"launch"}` should raise a running
  single-instance app rather than spawn a second. That is a small generic
  fix — every single-window app gets it, not just this one — and it is
  what makes the start menu stop being a hole in the policy.

**The pop-out needs an explicit bypass.** Once both paths are
instancing-aware, *Open in new window* needs a way to say "another one,
deliberately". A flag on the spawn request (`EvtSpawnRequest.Extra` /
an explicit `force`) is the honest shape: the caller states intent rather
than the router guessing from context. This is the one wire addition in
the plan and it should be scoped as narrowly as that sentence.

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

The structural change. Both spawn paths become instancing-aware (§4),
`com.wash.ai` becomes single-instance, and the explicit pop-out lands
with its bypass flag.

- agentd's three spawn sites collapse into one raise-or-open helper.
- The session BE's launch action raises a running single-instance app.
- *Open in new window* on a row, and only there.
- **Window restore** must be checked, not assumed: a reload must bring
  back the one window pointing at the session it was showing (the BE
  already persists `session_key` and `split_pct`), and must not produce a
  second window in the process.

*Risk:* this is the milestone that can break "I had two agents side by
side". The pop-out must land in the same change, not after it.

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

### M5 — one status vocabulary

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
- No archive management UI — no bulk delete, no retention settings. The
  store grows unbounded today and that is a separate, honest problem
  (`SessionMeta.Bytes` exists for whoever picks it up).
