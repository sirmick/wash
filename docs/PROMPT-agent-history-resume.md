# Session prompt — getting a dropped agent session back

Goal, in one line: **the History menu must offer the session you just lost, and
a session that comes back must come back whole.** Today it hides the first and
strips the settings off the second.

Paste this as the opening prompt for a dedicated session. It is self-contained.
Companion to `docs/PROMPT-session-lifetime.md`, which covers *why* sessions drop
in the first place. This doc is about recovering from a drop, and is independent
of that work — do not wait for it.

---

## Reported symptoms

> When an agent session drops I tend to use the history to get it back. Two
> issues: the history **menu** doesn't seem to bring it up (but the history
> **pane** does), and once it's back the "session" menu has no settings — no
> model, no thinking level.

Both reproduce. They are unrelated to each other, and neither is in the resume
itself: the menu and the panel send the **identical** message.

```tsx
// apps/ai/fe/src/main.tsx:488  (menu)
onClick={() => { close(); send({ kind: 'resume', session_id: s.session_id }); }}

// apps/ai/fe/src/main.tsx:570  (panel)
onResume={(s) => { setHistoryOpen(false); send({ kind: 'resume', session_id: s.session_id }); }}
```

The bug is entirely in **what gets listed**, and **what comes back**.

## Bug 1 — the menu and the panel read different stores

| | History **menu** | History **panel** |
|---|---|---|
| source | `roster().recent` → `publishHistory()` | `agent_history` → `historyQuery()` |
| store | in-memory `history`, persisted to `$XDG_STATE_HOME/wash/agent-sessions.json` | transcript index, `transcript_store.go:842` |
| cap | **20** (`history.go:32`) | **200** (`service.go:341`) |
| populated by | `rememberSession`, roster sightings only | transcript files on disk |
| filters live? | **yes** — `.filter((s) => !s.live)` (`main.tsx:248`) | no |

**The live filter is the cause.** `publishHistory` (`history.go:113-127`) marks
`Live` for any session id present in `rows`, and a **detached session still has a
row** — `agent_detach` sets `h.detached = true` and closes the window but never
retires the row (`acp.go:652-660`; the row is still published with
`r.Detached` at `acp.go:238`). So a session that is detached, held, or was
auto-resumed at agentd startup is marked live and disappears from the menu —
which is exactly the state you are trying to recover from.

The intent behind the filter is sound and documented at `history.go:53-56`:
offering to resume a session that is already running would be offering to
duplicate it. The error is treating **live** and **reachable** as the same thing.
A detached session is not something to resume; it is something to *reattach* to.
The menu has no reattach verb, so it shows nothing at all.

Eviction was ruled out as the cause here — on the reporting machine both
sessions were present as entries 1 and 2 of the store. But the store is worth
looking at anyway:

- saturated at **20/20**, spanning **344 hours** (~14 days)
- **11 of the 20 slots** are codex throwaways: `say something` five times,
  plus a `fake-ses` / "Fake conversation" test fixture

So real work is competing with test noise for a 20-slot window. Not today's bug,
but it will be someone's next week.

### Latent third bug, falls out of the same table

The panel does **not** filter live sessions. Resuming an already-live session
from the panel can therefore duplicate it — the precise outcome the menu's filter
exists to prevent. Whatever guard the menu grows in M1 has to apply to the panel
too, or the two views stay inconsistent in the opposite direction.

## Bug 2 — the resume path never asks for the settings

`startHosted` captures the agent's settings block; `resumeHosted` does not.

```go
// apps/agentd/be/adapters.go:154,159 — the session/new path
h.applyModes(res2.Modes)
h.applyConfigs(res2.ConfigOptions)
```

`resumeHosted` (`adapters.go:343-369`) calls neither. `h.modes` and `h.configs`
stay nil, `publicModes(nil)` / `publicConfigs(nil)` produce nothing, `omitempty`
drops `Modes` and `Configs` off the roster row (`app.go:99-111`), and the FE
renders an empty settings block.

It is one level deeper than a missing call. The response is discarded at the
client:

```go
// internal/acp/client.go:153
return c.conn.Call(ctx, MethodSessionLoad, LoadSessionRequest{SessionID: sessionID, Cwd: cwd, McpServers: mcp}, nil)
```

That trailing `nil` is the response destination. There is no `LoadSessionResponse`
type in `internal/acp/types.go` at all — only `NewSessionResponse`, which does
carry `ConfigOptions`, `Modes` and `Models` (`types.go:125-135`). The load path
cannot currently represent what it needs to receive.

The logs show it plainly — a started session reports its modes, a resumed one has
none to report:

```
06:29:14  agentd: acp session started key=acp:2 … cwd=/home/mick mode=default modes=6
06:28:57  agentd: acp session resumed key=acp:1 … cwd=/home/mick/wash
```

---

## The work order

### M1 — the menu must offer sessions you can get back to

Split **live** from **reachable** in `publishHistory` and in the FE's `recent()`.
A row that is `detached` should appear in the History menu with a *reattach*
verb; a row that is genuinely running and attached stays hidden or greyed as it
is today. `Session` already has `Live`; it needs the detached bit alongside it,
and `AgentRoster` already has an `onReattach` path (`main.tsx`, `row_reattach`)
whose message the menu can reuse rather than inventing one.

Verify by detaching a session and opening the History menu — it must be listed
and clicking it must reattach, not spawn a second copy.

### M2 — carry modes and configs through `session/load`

1. Add `LoadSessionResponse` to `internal/acp/types.go`, mirroring
   `NewSessionResponse` (`ConfigOptions`, `Modes`, `Models`; no `SessionID`).
2. `internal/acp/client.go:146` — return it, pass `&res` rather than `nil`.
3. `apps/agentd/be/adapters.go` — in `resumeHosted`, call `h.applyModes(res.Modes)`
   and `h.applyConfigs(res.ConfigOptions)` after `LoadSession` succeeds, and add
   `mode=%s modes=%d` to the resumed log line so the two paths log alike.

**Verify against a real adapter before building on it.** This repo is careful to
write "Observed on codex-acp 1.1.9" rather than assume a shape, and
`HistoryPanel.tsx:15-19` explicitly refuses to ship a Fork button for exactly
this reason — an UNSTABLE capability whose method shape was never verified. Hold
that standard here. If `session/load` turns out not to return the block, the
fallback is re-requesting configs after load (there is already an
`applyConfigs` call fed by a notification at `acp.go:362` and by a response at
`acp.go:783`) — not inferring defaults.

### M3 — one guard, both views

Apply whatever M1 lands to the History panel as well, so resuming a live session
from the panel cannot duplicate it. The two views should disagree about
*presentation* — searchable list vs fast path — never about *what is safe to
click*.

### M4 — history store hygiene (small, do last)

`historyCap = 20` is a fortnight of real work on a quiet machine and about two
days on a busy one. Consider raising it, and consider not recording sessions that
never produced a turn — five entries titled `say something` and a fixture called
`Fake conversation` are occupying a quarter of the window. Test fixtures reaching
a user's real state file is its own small bug worth tracing.

## Traps

- **Both views must send the same verb for the same state.** The current split —
  menu filters, panel does not — is how one of them ended up able to duplicate a
  session. Fix the predicate in one place and consume it from both.
- **Do not "fix" the menu by removing the live filter.** That gives you
  duplicates, which is worse than an omission and much harder to notice.
- **`historyFlush` is 30s** (`history.go:37`) and the on-disk copy lags memory by
  that much. When testing recovery after a hard router exit, expect the last
  30 seconds of history updates to be missing; that is a separate concern from
  this doc but will confuse a repro.
- Another agent session may be live in this repo (`apps/ai/**` was under active
  edit on 2026-08-20). Check before touching shared files.
