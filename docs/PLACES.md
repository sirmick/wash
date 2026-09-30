# Places — the other apps, bound to this one

Status: **built, not yet committed** (2026-09-30). M1–M3 are in the working
tree; §7 records where the build departed from the design and why. Supersedes
the path-matching draft of the same date (see §3 for why that was dropped).

Related: [ARCHITECTURE.md](ARCHITECTURE.md) (app vs chrome),
[AGENT_UX.md](AGENT_UX.md) §N1 (the door primitive and the raise doctrine), §N5
(history-derived defaults), [AGENT_APP.md](AGENT_APP.md),
[SIDEBAR.md](SIDEBAR.md) §1 (the `sendAppMsgTo` origin seam this must not
repeat), [INTERACTION.md](INTERACTION.md) (state that is not carried by colour
alone).

---

## 1. From the user's point of view

Agent, Files, Editor and Terminal each grow a small cluster of icons — one for
each of the other three — in their menu bar. They are the apps people use
together, so they can reach each other directly.

**The first click makes a pair.** You are in an agent working in `~/wash`. You
click the terminal icon. A terminal opens in `~/wash`, and the two are now
**bound**: that agent's terminal icon fills in, and over in the terminal the
agent icon fills in too. The pair is visible from both ends.

**Every click after that is a door.** Click the terminal icon again and you do
not get a second terminal — you get *that* terminal, raised and focused,
wherever it is on the desktop. Click the agent icon from the terminal and you
land back in the conversation. Two windows, one round trip, no hunting.

**They form a group, and the group has a colour.** Those two windows are now a
group, and each one's icon bar picks up the same subtle tint. Open Files from
either of them and it joins the same group, same tint — up to four windows,
one of each app, recognisable as belonging together from across the desktop
without reading anything. Start a second agent and it gets its own group in a
different colour.

**A group is exclusive and permanent.** Nothing steals a member. A different
agent that wants a terminal gets its own. There is no unbind gesture; a window
leaves only by closing, at which point the others free that slot and the next
click opens a fresh one.

**The binding follows the window, not the folder.** If you `cd /tmp` in that
terminal, it is still *your agent's terminal*. The icon still goes there. This
is deliberate — it is the terminal you have been working in, and people wander.
The tooltip names the window on the other end so you always know where a click
will land.

**Clicking the agent icon starts a real session.** From Files, Editor or
Terminal, an unbound agent icon opens an agent in that folder on **your default
catalog** — the one you picked in the Agents window — rather than asking you to
fill in the launcher again. You set that default once; it is the same default
the Editor's "new agent here" uses, so "start an agent" means one thing
everywhere. The tooltip names it, so nothing is a surprise.

### 1.1 Why this shape

The obvious alternative was to match on the folder: "find a terminal already at
`~/wash` and show that one." It reads well and it is worse to live with.

- It has to answer questions with no good answer. Exact path or any ancestor?
  A terminal tracks a cwd *per tab* — does a matching tab count? Two candidates,
  which one? Every answer is a guess about intent.
- It silently changes what a click does as you navigate. The same icon opens
  different windows at different times, for reasons that are invisible.
- It needs the system to know every app's current folder, everywhere, all the
  time (§3).

Binding answers all of it with one rule the user sets themselves by clicking.
The cost is that the *first* click may open a second terminal when you already
had one that would have done — accepted deliberately, as better than N
terminals you cannot tell apart.

---

## 2. What already exists

**The pattern is built, once, for one pair.** Agent↔Editor is a complete
working binding, and generalising it is most of this feature:

| Piece | Where |
|---|---|
| forward handle | `editor.instance` — `apps/ai/be/editor.go:55-63` |
| spawn-in-flight coalescing (a double click opens one editor, last click wins) | `spawning`/`pending` — `editor.go:86-96` |
| adoption of the spawned instance | `onSpawnResult` — `editor.go:117-142` |
| staleness on close | `onInstanceGone` — `editor.go:147-154`, SDK hook at `pkg/sdk/dispatch.go:185-192` |
| **back-binding**, from the router-attested sender | `owner = from.InstanceID` — `apps/edit/be/agent.go:285-289` |
| pushing the binding to the FE so it can render it | `tellFEOwner` — `agent.go:292` |
| the return trip | `agent.show_owner` — `agent.go:307-317` |

The back-binding is the important one: `HandleFromVoid` gives the receiver a
**router-attested** `from`, so the pair is established by the bind message
itself. No handshake, no registry.

**The verbs mostly exist too.** The agent BE already implements all three
(`apps/ai/be/app.go:409-430`); they are only surfaced in a window menu. Files
has Terminal and Editor as toolbar buttons (`apps/fm/fe/src/main.tsx:3060-3085`)
— the reference example. Editor has Files and Terminal in menus. Terminal has
none.

**Underneath everything**: `EvtSpawnRequest{app_id, open}` →
`--open <path>` → `Conn.LaunchOpenPath()`. Caller side
`Conn.SpawnRequestOpen(appID, path)` (`pkg/sdk/outbound.go:330-338`), gated on
`sdk.CapSpawn`.

---

## 3. What binding does *not* need

The path-matching draft required a new location index: an `EvtAppLocation`
event, a `CapLocation` capability, a router-side `instance → paths` map, a
`Reuse` flag on `EvtSpawnRequest`, a change to `spawnChild`'s bypass of
`launchOrRaise`, and a FE→BE cwd push in Files and Editor (whose cwd is FE-only
— `viewDir()` at `apps/fm/fe/src/main.tsx:1186-1193`, `root()` at
`apps/edit/fe/src/main.tsx:364`).

**Binding needs none of it.** An app only ever needs *its own* folder, to spawn
the first target. It never asks where another app is. All four already know
their own cwd, so nothing new crosses the wire but the bind message.

No router change is required at all.

---

## 4. Design

### 4.1 State — a group, not a pair

A window belongs to at most one **group**, and a group holds at most one window
of each app:

```go
type group struct {
    id      string               // minted by whoever binds first
    members map[appID]instanceID // at most one per app
}
```

Held in process, so it dies with the window — which is correct, and needs no
persistence. (Agent *sessions* outlive their windows for Resume, so a resumed
session comes back unbound. Accepted.)

**Why a group rather than a table of pairs.** The first draft gave each window
one slot per peer app and called bindings "strictly pairwise, not transitive" —
so an agent bound to a terminal, and that terminal bound to Files, left Files
with no way back to the agent. That is hard to explain and harder to draw: it
is also incompatible with tinting the bar per group (§4.4), because "group"
would have no referent. Making the group the unit resolves both. Files reaches
the agent because they are in the same group, and the tint has something to
colour.

The group also makes exclusivity easy to state: **a window is in at most one
group, and a group has at most one window per app.** Those two sentences are
the whole model.

### 4.2 Click

Clicking the icon for app `X`:

- **X is in my group** → `SendAppMsgTo({InstanceID}, places.show{…})`. The
  receiver raises **itself** with `c.Raise()`.
- **not in my group** → `SpawnRequestOpen(X, cwd)`, adopt in `OnSpawnResult`,
  then send `places.show`. A click while a spawn is in flight queues rather
  than spawning twice, and the later click's payload wins. A window with no
  group yet mints one first.

The folder comes **from the FE at click time**, confined by the BE, as every
existing open-with verb does — except the Terminal, whose BE already records
each tab's cwd (`cwdOf()`): its FE names only the front tab. Built as
`internal/places`; each app forwards `OnSpawnResult`/`OnInstanceGone` to it.

This keeps the standing doctrine: **an app may raise only its own window**
(`pkg/sdk/outbound.go`, `apps/agentd/be/focus.go`). Files never raises a
Terminal; it asks the Terminal to raise itself.

#### Convergence — the part the first build got wrong

`places.show` carries the whole roster, so a *joiner* learns every member in
one message. That alone does **not** converge: Agent opens Terminal, then
Agent opens Files — Files learns the Terminal, but the Terminal never learns
Files, and clicking Files from the Terminal opens a second one into the same
group. So there is a second message, **`places.sync`** — the same payload,
no raise — and one rule: **any window whose roster grows re-broadcasts it.**

"Whoever adds a member tells the others" is *not* enough, and the tests prove
it: it passes every sequential case and fails when two members add different
apps at once, because each broadcasts a roster missing the other's newcomer.
The grow-rebroadcast rule terminates because growth is monotone: a slot only
ever moves to a **lower** instance id (the older window), and an instance seen
closing is never adopted again. Without that order, "grows" is just "differs",
and two members spawning the **same** app at the same instant flip the slot
between the two windows forever (a test reproduced it). Now both clicks open a
window, the group keeps the older one, and the younger — told directly, since
no roster names it any more — drops out, still showing what its click asked
for, untinted. The session key/title has one author, the Agent: others take a
relayed copy only while they hold none, or straight from the Agent, since two
stale relayed titles crossing a rename would flip the same way.

#### Raising something you cannot see

An app raising itself sets focus and z, but the shell pans its camera only for
its *own* clicks — so a raised window one viewport cell over came forward
invisibly. The router now sends **`window.reveal`** from `Router.raiseWindow`,
which only non-user raises reach (an app's self-raise, `launchOrRaise`); a
user's focus click goes through `handleWindowFocus` and never reveals, which
is what keeps a reload — which refocuses every window as it mounts — from
panning. The shell pans only if **none** of the window is on screen. This also
fixed the Agent's "show editor" and notification clicks, which had the same
bug before Places existed.

### 4.3 Exclusivity

A `places.show` is refused when the receiver is **already in a different live
group**. Enforcing on the receiving side matters — the asking side cannot know
the target is taken. The Editor's older `editor.show` path got the same rule
(it was last-writer-wins), and a bug surfaced doing it: the Editor never
cleared its owning Agent when that window closed, so with an exclusive claim
nothing could ever claim it again.

So a second agent that wants a terminal gets its own, and the first pair is
never stolen. A window leaves its group only by closing; the others drop it on
`onInstanceGone` and the slot is free for the next click.

Which cwd seeds a spawn, per app:

| App | Folder it offers |
|---|---|
| Terminal | the **front tab's** cwd — `cwdOf()`, the BE's own record |
| Files | `viewDir()` |
| Editor | `root()` (project root) |
| Agent | `session.cwd` |

### 4.4 The icons

There is **no titlebar slot**. `FloatingWindow`
(`web/shell/src/window.tsx:453-546`) hardcodes minimize/maximize/close,
`WindowProps` is `{win, onClose}` with no children, and `TITLEBAR_H` is a
constant the geometry math depends on. Adding an app-extensible chrome slot is
far more than this feature warrants and would put app-controlled content in
shell-trusted chrome.

So the cluster lives in each app's own menu bar, where Files already puts these
buttons. New shared component `web/lib/src/places-bar.tsx`:

- one ghost icon button per other app; glyphs already in the sprite
  (`web/shell/build-icons.mjs:20-68`): `terminal`, `folder`, `file-pen`, `bot`;
- **bound** renders filled; **unbound** renders outline and muted. Fill, not
  colour — the bar's colour is spent on the group (below), so bound-ness needs
  a cue that does not compete with it, and colour-only state fails for
  colourblind users anyway ([INTERACTION.md](INTERACTION.md));
- tooltip names the far end when bound (Editor already does this:
  `Show the Agent window · ${a().title}`), and when unbound says what a click
  will make — including, for the agent, which catalog and model.

#### The group tint

The bar itself carries a **subtle background tint, one colour per group**, so
the windows that belong together are recognisable at a glance across the
desktop without reading a tooltip. All four members of a group show the same
tint; a window in no group shows none.

The colour is **derived, not allocated**: a stable hash of the group id indexes
a fixed palette, so every member computes the same tint from the group it
already carries — no registry, no assignment message, and no way for two
members to disagree. Two groups can collide on a colour once there are more
groups than palette entries; for a background hint that is acceptable, and
unavoidable with any finite palette.

Constraints on the palette: tints must stay legible behind the menu bar's own
text in **both themes**, so these are low-alpha washes over `bgInset` rather
than saturated fills, and they must not read as the app accent colours already
in use (`#6dc878`, `#6090e0`, `#e0b060`, violet) or the tint will look like a
statement about which app it is. It is decoration for recognition, never the
only carrier of state: bound-ness is the icon fill, identity is the tooltip.

### 4.5 The agent's launch defaults

"Open an agent here, with my usual setup" does not work today, and the reason is
worth fixing on its own account.

- **`--open` does nothing for `com.wash.ai`.** It declares no `Opens:` and never
  calls `LaunchOpenPath()` — the only mention is the comment at
  `apps/ai/be/app.go:171` that the SDK's argv must pass through unscathed. A
  `SpawnRequestOpen` at it yields a window with no session. It takes `--cwd` or
  a bare positional dir instead (`parseFlags`, `app.go:176-207`).
- **A bare dir picks the wrong thing.** `onReady` (`app.go:340-346`) autostarts
  with `firstAvailableAgent()` (`app.go:211-218`) — the first available
  *adapter*, with no catalog, model, mode or yolo. So `wash ai ~/foo` from the
  CLI already ignores your usual setup. That is a latent bug, not something
  Places introduces.

And there is no stored "current model" to inherit. Today the last-used catalog
and folder are **derived from session history** by
`defaultCatalog(catalogs, recent)` / `defaultCwd(recent)`
(`apps/ai/fe/src/default-catalog.ts:17,37`), deliberately so that "nothing can
disagree with it" (`default-catalog.ts:6-9`). Only permissions mode and yolo are
actually persisted, in `~/.config/wash/agents.json` under `launch`
(`agentpolicy.LaunchPrefs`).

That derivation is FE-side, which is the blocker: Places and the Editor's
"new agent here" both start from a BE, and neither should be reaching into the
agent app's frontend to work out a catalog.

**Fix: give the default a home the BE can read.** Extend `LaunchPrefs` with the
catalog and model, beside the mode and yolo it already holds:

```go
type LaunchPrefs struct {
    Catalog string            `json:"catalog,omitempty"` // new
    Model   string            `json:"model,omitempty"`   // new — slot name or model id
    Mode    map[string]string `json:"mode,omitempty"`
    Yolo    bool              `json:"yolo,omitempty"`
}
```

This is the struct's existing job — its doc comment already reads "what the
Agents window's launcher starts a session with unless changed for that one
start" — and it is already persisted, already published in the roster
(`publishLaunch`, `apps/agentd/be/catalogs.go:345`), and already has a setter
(`setLaunch`, `:326`, via `agent_set_launch`). It exists in two shapes,
`agentpolicy.LaunchPrefs` (storage) and `agentproto.LaunchPrefs` (wire); both
grow the fields.

**Resolution order** for a start request, most specific first:

1. what the request names (`req.Catalog` / `req.Agent`) — unchanged;
2. the stored default catalog + model;
3. history-derived (`defaultCatalog`), so anyone who never sets a default keeps
   today's behaviour;
4. `firstAvailableAgent()` as the last resort.

Step 2 is new and slots in at `startProfile`'s `default:` branch
(`catalogs.go:263`), which currently returns `errors.New("choose a catalog, or
an agent")`. With a stored default, **`AgentStart{Cwd}` alone becomes a valid
request** — which is exactly what Places and `✦` want to send, and why this
belongs in agentd rather than in each caller.

There is a real argument for dropping step 3 once step 2 exists, on the
original "nothing can disagree with it" grounds. Keeping it costs one fallback
and avoids changing behaviour for users who never open the setting; worth
revisiting after M2 lands rather than deciding now.

**Where the user sets it.** The manager already has a Catalog tab
(`agents-tab-catalog`, `apps/ai/fe/src/main.tsx:1136`) where catalogs are
configured — a "Default" picker belongs there. The Launcher's catalog/model
selects then pre-fill from it, exactly as its Permissions row already edits the
global default in place (`Launcher.tsx:159-164`).

**Gotcha worth not rediscovering**: `setLaunch` currently nils the whole
`Launch` block when `Mode` is empty and `Yolo` false (`catalogs.go:335-338`).
Adding fields without widening that emptiness check would silently discard a
default-catalog-only setting. Validation also needs to reject an unknown
catalog id, as it already does for an unknown adapter.

**Not inherited**: workspace members are deliberately specified per member
(`memberSettings`, `apps/agentd/be/workspace.go:485-508`) and must not pick up
the user's default. Nor does an explicit `wash ai --agent X`.

No confirmation gate is added. There is no cost or budget concept anywhere in
agentd, and Editor's `✦` button (`apps/edit/fe/src/main.tsx:3998-4012`) already
starts a session in one click. The Launcher's one-line summary of what will run
is honoured in the tooltip instead of a dialog.

---

## 5. Milestones

- **M1 — the binding seam.** `internal/places`: `places.show` and
  `places.sync`, exclusivity on receive (§4.3), all four apps wired BE-side,
  `window.reveal` so a raise is visible.
- **M2 — a default catalog, and the launch path that reads it.** §4.5:
  `LaunchPrefs` grows `Catalog`/`Model`; a Default picker in the Catalog tab;
  `startProfile` resolves a catalog-less request against it so `AgentStart{Cwd}`
  is valid; `LaunchOpenPath` honoured by `com.wash.ai`. Stands alone — it fixes
  `wash ai <dir>` and gives `✦` a real default.
- **M3 — `PlacesBar`** (`web/lib/src/places-bar.tsx`). The shared component
  with bound/unbound fill and the derived group tint, in all four menu bars.

M1 before M3 deliberately: it makes today's affordances stop duplicating before
twelve more icons are added.

### What M3 consolidated, and what it deliberately did not

- **Files**: the toolbar's "open terminal / editor here" buttons became the
  bar. The row context menu keeps its verbs — they act on the *clicked*
  folder, not the viewed one.
- **Agent**: the top-right "Editor" button became the bar, and the window
  menu's terminal / file manager items became "Show terminal" / "Show file
  manager", the Places verbs. The Agents *manager's* row verbs still open
  fresh windows: a row is any session, not this window's group.
- **The Agent's editor is the group's editor.** `apps/ai/be/editor.go` kept its
  own instance, spawn queue and adoption; left alone, the old "Editor" button
  and the new Editor icon would have led to two different editors. It now
  delegates to the group, and the session key and title travel with the
  roster (§7) so the Editor still titles itself after the conversation and
  can reopen it by key.
- **`✦` stays.** An earlier draft of this document said it should go; that was
  a misreading. `✦` starts an agent session **in a tab inside the Editor**,
  which is a different thing from the Places agent icon's separate Agent
  window. It now offers the default catalog first, above the adapter list,
  as do the Editor's Terminal menu and "send selection to agent".

---

## 6. Open questions

1. **Does Places cross hosts?** "Open a terminal here" when the folder is on
   host B should plainly open it *on* B. That needs the bind addressed by
   origin — and `sendAppMsgTo` is **not** origin-aware
   ([SIDEBAR.md](SIDEBAR.md) §1), the same seam that leaves eight of eleven
   sidebar widgets local-only. Either fix it here or decline the cross-host
   case explicitly; do not leave it silently wrong.
2. **Is a binding worth showing anywhere but the two windows?** With four apps
   the pairs form an invisible graph. The taskbar or pager could mark a bound
   pair. Probably not for v1, but it is the obvious first complaint.
3. **Terminal's per-tab cwd** (§4.3) settles which folder seeds a spawn, but
   not whether a bound Terminal should switch to the tab the agent cares about
   when raised. Raising and *not* switching seems right — switching can yank
   you off a running command — but it is worth stating.

---

## 7. As built — departures from the design above

- **Membership needed a second message** (`places.sync`) and the
  grow-rebroadcast rule. §4.2 has the detail; the first build's tests all
  passed because none involved more than one message. The tests now run the
  protocol as several windows exchanging messages through an in-memory
  router, which is also why `internal/places` takes a small `Conn` interface
  rather than `*sdk.Conn`.
- **The group carries the Agent's session** (key and title), propagated like
  membership and cleared when the Agent window leaves. Needed once the Agent's
  editor became the group's editor: the Editor learns its conversation from
  the group, including when it joined through the Terminal and never heard
  from the Agent directly.
- **`window.reveal`** (§4.2) was not in the design; the design promised
  "wherever it is on the desktop" without checking that a raise was visible.
- **A window alone reports no group**, even while it holds an id for a spawn
  in flight, so it is never tinted by itself. And a group is kept alive while
  a spawn is in flight even if every other member closes — the newcomer was
  promised that id.
- **The tint palette** is six theme accents — cyan, magenta, orange, indigo,
  pink, lime — excluding the four app accents and red, as 16% washes
  (`color-mix`, the house pattern). The hash is FNV-1a. Bound icons use a 30%
  translucent fill: an opaque fill swallows the strokes these glyphs are drawn
  with (the terminal's prompt, the bot's eyes).
- **Not done:** the agent icon's tooltip in Files and Terminal does not name
  the default catalog, because those apps do not receive agentd's state (the
  Editor does, and shows it). Cross-host (§6.1) remains local-only.
