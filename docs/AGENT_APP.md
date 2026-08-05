# Agent sessions over ACP — replacing the intercept tier

Goal, in one line: **wash launches the coding agent over the Agent Client
Protocol and reads its tool calls, state and permission requests off a
JSON-RPC wire — retiring the hook-install / OSC / pty-socket machinery that
inferred the same things by intercepting an agent harness we do not own.**

This supersedes the mechanism in `docs/AGENT_TERM.md` M1–M4, M6 and M7.
That document's M5 (smart paste) is independent and unaffected. Design only —
nothing here is built.

Decisions already made (discussion 2026-08-03):

- **The intercept tier is deprecated, not kept as a fallback.** Hook
  installation into a vendor's settings file, the OSC 7770 status channel,
  the per-tab decision socket and the typed-`y` tap all go. §12 records what
  that costs.
- **The approval centre survives untouched.** `apps/agentd/be/ask.go` is the
  asset; ACP becomes a producer for it. Nothing about the queue, its
  deadlines, its defer-on-nobody-home or its rule-writing changes.
- **Three surfaces, one component.** A standalone app, a pane in wash-term,
  and a panel in wash-edit — all rendering one `@wash/ui` component that owns
  no session state. Same promotion path the file tree took when edit became
  its second consumer.
- **Codex first.** Its adapter is a static binary; Claude's needs Node. The
  packaging decision drives the order.

## 1. Why the mechanism changes

The shipped design infers session state from things the agent was persuaded
to emit. It works, and it is honest about failing open — but every layer of
it is a guess at a private contract:

| Today | Reads | Breaks when |
|---|---|---|
| T0 foreground poll | process `comm` against a name table | an agent is renamed, wrapped, or run over ssh |
| T1 OSC 7770 | escape sequences our own installed hooks emit | the vendor renames a hook event or changes its payload |
| T2 decide socket | a `PreToolUse` hook's stdin JSON | the vendor changes the output schema — silently, because we fail open |
| `autoapprove.go` | the output stream, then types `y` | anything at all; it is spoofable by design |

ACP replaces all four with one thing wash is *told* rather than infers:
`session/update` for state, `session/request_permission` for approvals, both
over JSON-RPC 2.0 on the adapter's stdio. Tool calls arrive typed
(`read | edit | delete | move | search | fetch | execute | think | other`),
and permission options arrive with a declared kind
(`allow_once | allow_always | reject_once | reject_always`) instead of being
derived by pattern-matching a Bash string.

The vendor-tracking moves to the adapters, which are maintained by parties
with product depending on them (Zed, the ACP org, and behind them JetBrains,
Google and Microsoft shipping ACP clients).

## 2. Architecture

```
  com.wash.ai        wash-term pane        wash-edit panel
        └──────────────────┼──────────────────┘
                  <AgentSession>  (@wash/ui — renders, owns nothing)
                           │ app_msg
                           ▼
                    apps/session/be  ── gateway (netd/audio shape)
                           │
                           ▼
                  com.wash.agentd ── THE session host
                    ├─ ACP client  ──stdio JSON-RPC──►  adapter ──► agent
                    ├─ roster (StateService, unchanged wire states)
                    ├─ ask queue (ask.go, unchanged)
                    ├─ policy (moved here from wash-term)
                    └─ history (unchanged; session/load replaces --resume)
                           │
                           └── terminal/create ──► wash-term tab
                               fs/*            ──► wash's file layer
```

One host, three renderers, and the sidebar widget does not learn that the
mechanism changed.

## 3. What is reused

The point of the pivot is that most of the value is above the mechanism:

| Survives | Change |
|---|---|
| `apps/agentd/be/ask.go` | source-agnostic reply route (§4) |
| `apps/agentd/be/service.go` roster | none — same four wire states |
| `apps/agentd/be/history.go` | `session/load` replaces `--resume` argv |
| `apps/agentd/be/git.go` | none |
| `internal/agentpolicy` | gains ACP-derived rule text; still the `agents.json` schema |
| `apps/session/be` gateway | gains transcript subscribe/unsubscribe |
| `apps/session/fe` AgentsWidget | none on day one |
| notify plumbing (`EvtNotify.Source`, click-to-focus, taskbar badge) | none — all generic |
| `apps/term/be/agenttoast.go` | **moves** to agentd |
| `apps/term/be/policy.go` matcher | **moves** to agentd |
| `exec_tab` in `apps/term/be/app.go` | generalizes into `terminal/create` |
| `web/lib/src/terminal.tsx` | reused as the agent's shell pane |
| smart paste (AGENT_TERM M5) | untouched, unrelated |

## 4. M0 — the seam

`ask.go` is generic except for the reply route:

```go
type Ask struct { … TermInstance string `json:"term_instance"` }   // :61
answerTerminal(conn, p.TermInstance, p.reqID, decision, "desktop") // :169,188,192
```

Replace `TermInstance` with `{SourceApp, SourceInstance}` and
`answerTerminal` with an `answerSource` dispatching on app id. Roster keys
generalize from `<term instance>:<channel id>` to `<source>:<instance>:<key>`.

Self-contained, ships alone, and is the only change the old tier needs in
order to coexist during the transition.

## 5. `internal/acp` — the client

wash is the **client**; the adapter is the agent.

*Outbound:* `initialize`, `authenticate`, `session/new`, `session/load`,
`session/prompt`, `session/cancel`.

*Inbound handlers:* `session/update` (→ roster + transcript),
`session/request_permission` (→ the ask queue), `fs/read_text_file`,
`fs/write_text_file`, and `terminal/create | output | wait_for_exit | kill |
release`.

**Build, don't depend.** `ironpark/acp-go` (MIT) covers both sides including
permissions and terminals, but is ~30 stars, ~24 commits and self-describes
as unofficial and possibly lagging the spec. The subset above is six
outbound methods and nine handlers over a transport wash already writes by
hand. Read its types; hand-roll the client.

## 6. Adapters and packaging

*Verified 2026-08-04 by running both against `internal/acp`:*

- **Codex** — `@agentclientprotocol/codex-acp` 1.1.9, drives
  `codex app-server`. **An npm package, not a Rust binary** — the earlier
  claim here was wrong.
- **Claude** — `@agentclientprotocol/claude-agent-acp` 0.64.2, wraps the
  official Claude Agent SDK. Renamed from `@zed-industries/claude-code-acp`,
  which now only prints a deprecation warning.
- **Gemini CLI, Copilot CLI** — native ACP, no adapter.

**So Node is a prerequisite for the managed tier as a whole**, not just for
Claude, and the "Codex first because its adapter is static" argument does
not survive contact. Ordering is now a preference, not a constraint.

Packaging follows the wash-display precedent regardless: the managed tier
is **opt-in in deb/rpm/apk**, the base package gains nothing mandatory, and
an absent adapter is a greyed launcher row with a reason rather than a
failed spawn. `Adapter.launch()` prefers a globally-installed binary and
falls back to `npx --yes <package>`, which is how most boxes will have it.

Two operational notes from the same session:

- **Codex needs a usable sandbox.** `codex app-server` refuses to start on
  Ubuntu 24.04+ where `kernel.apparmor_restrict_unprivileged_userns=1`
  blocks bubblewrap, even with `bwrap` installed. Surfaces as an adapter
  that dies during the handshake; the stderr pump is what makes it
  diagnosable instead of a mystery hang.
- **`authMethods` is not "auth required".** codex-acp advertises `api-key`
  and `chat-gpt` *and opens sessions fine*. Refusing on a non-empty list —
  which the first cut of `startHosted` did — would reject every working
  install. The real signal is `session/new` failing; the list is then what
  makes the error actionable.

## 7. agentd as session host

New `apps/agentd/be/acp.go`:

- **Registry** keyed `(agent, session_id)`, each owning an adapter process,
  its stdio pump and its ACP session id.
- **`session/update` → the existing four wire states**
  (`running | working | needs-input | done`). Deliberate: the pivot must be
  invisible in the sidebar on day one.
- **`session/request_permission` → `ask.go`** through M0's source route.
- **Liveness is real now.** We own the process, so exit is a fact rather
  than a 60s inference. The TTL sweep stays only as a backstop.
- **Policy moves here.** The matcher from `apps/term/be/policy.go` and the
  `agents.json` schema in `internal/agentpolicy` land in one owner —
  necessary anyway, since ACP's `allow_always` is remembered **by the
  client**, and agentd is now the only client.

## 8. Terminals and files

`terminal/create` means the agent asks *us* to run its commands. It gets a
real wash-term tab: scrollback, copy-paste, split panes, and a human who can
type into it. The transcript shows one tail line and a link that focuses the
tab.

`fs/read_text_file` / `fs/write_text_file` map onto wash's file layer, so
wash-edit's parent-dir watcher reloads open files live while the agent writes.

This is the part no other ACP client can do, and the reason the transcript
can stay a one-line-per-call log instead of growing an embedded terminal and
a diff viewer.

## 9. The three surfaces

**`<AgentSession>` in `@wash/ui`** — props are a session id and a bus,
nothing else. It renders a transcript, a composer and a status line, and
owns no session state, no launcher and no approval logic. Exactly the
contract `terminal.tsx` has today (the host supplies policy; with the prop
absent the component just renders), and the same promotion the file tree got
when wash-edit became its second consumer.

| Surface | Shape | Notes |
|---|---|---|
| **`com.wash.ai`** | standalone window, `InstancingMulti`, one per session | empty state **is** the launcher; the default surface |
| **wash-term** | a pane in the layout tree | `Group.tabs` is `number[]` — the tree never asks what a channel is, so `layout.ts` needs **no change**. Needs a non-colliding id space, a renderer branch in `main.tsx`, and a prune rule matching TERM_LAYOUT §238 |
| **wash-edit** | a side panel | third consumer; already embeds `terminal.tsx`, so the seam exists |

The compelling case is the term pane: `terminal/create` can open the agent's
shell as a **sibling pane in the same window** — transcript left, its shell
right, one draggable divider. Only possible because split panes landed first.

Two rules that must be designed in, not discovered:

- **Two subscriber counters, not one.** agentd's `SubscriberCount` drives
  defer-on-nobody-home for approvals. A transcript subscription is not a
  roster subscription; sharing the counter would make opening a pane change
  approval behaviour, and closing the last pane defer a live question.
- **N renderers, zero affinity.** With three surfaces plus the sidebar, a
  pending ask is pure state in agentd with no per-view ownership. Answering
  anywhere resolves everywhere.

Naming: `com.wash.agent` is claimed by `docs/AGENT.md` (the
desktop-operating AI). This app is `com.wash.ai` unless that doc is renamed.

## 10. Removal — and the migration obligation

Deleted outright:

- `internal/agenthook/` (981 lines + 798 test) — `settings.go`, `decide.go`,
  `agenthook.go`, `cli.go`
- `cmd/wash-agent-hook/`, its multicall registry entry and Makefile `.PHONY`
- `internal/pty/agentosc.go` (466) + `SetAgentHandler` + the tee in `pty.Open`
- `apps/term/be/agentsock.go` (201), `askdesktop.go` (128),
  `autoapprove.go` (209) — and `SetOutputTap` / `Inject` if nothing else
  uses them
- the T0 agent table in `apps/term/be/agent.go` (the `ForegroundUser` seam
  itself stays — the root/ssh badges use it)
- e2e: `term-agent{,-notify,-policy,-roster,-ask,-resume}.spec.ts`,
  replaced by fake-adapter equivalents

**The obligation:** anyone who ran `wash agent-hooks install` has entries in
`~/.claude/settings.json` pointing at a binary that is about to stop
existing. A stale hook command is a per-tool-call error inside their agent.
So:

1. **M3 ships the unwind**: `wash agent-hooks install` prints a deprecation
   warning, `remove` keeps working, and wash logs a one-line warning at
   startup if marked entries are still present.
2. **M7 deletes the code**, but `wash agent-hooks remove` survives one
   further release as a cleanup-only command before it goes too.

Removal only ever touches entries marked by their `wash-agent-hook` command
string, exactly as install did.

`docs/AGENT_TERM.md` gains a header pointing here, and keeps §10/§9.5
(smart paste) live.

## 11. Milestones

Ordered standalone-first: the app is the thing to judge, so it ships and
gets used before anything is deleted and before the embedded surfaces are
built.

- **M0 — source-agnostic ask queue.** §4. No user-visible change.
- **M1 — `internal/acp`.** Client + adapter probe table, unit-tested against
  a scripted fake agent over pipes.
- **M2 — shared matcher.** The rule matcher moves from `apps/term/be/policy.go`
  into `internal/agentpolicy`, so both tiers evaluate rules identically
  during the overlap. A library move (the repo's second-consumer rule), not
  a transfer of ownership to agentd — that only happens once the old tier is
  gone.
- **M3 — agentd hosts sessions (Codex).** **Acceptance: a managed Codex
  session's permission request appears in the existing sidebar and answering
  it unblocks the agent — with no FE change at all.** The thesis is proven
  or dead here.
- **M4 — `<AgentSession>` + `com.wash.ai`.** Standalone window, launcher
  empty state, transcript, composer. Ships **without** `terminal/*`: with no
  terminal capability advertised, the adapter runs commands itself and
  reports output in `session/update`, which the transcript renders inline.
  **This is the milestone to live with before continuing.**
- **M5 — deprecation.** §10 — the unwind ships, then the intercept tier is
  deleted and its e2e replaced.
- **M6 — wash-term pane** + `terminal/*` and `fs/*` (§8). Bundled: a sibling
  shell pane is the reason the term surface is worth having, and it is what
  `terminal/create` is for.
- **M7 — wash-edit panel.** The third consumer.
- **M8 — remote.** Managed sessions on B surfaced on A (REMOTE §6.2 merge
  class), or ACP's HTTP/WebSocket transport once that RFD lands.

M0–M3 draw nothing and are the bet. M5 only runs after M4 has been used in
anger.

## 12. What is being given up

ACP sees only sessions wash launched. Deprecating the intercept tier
therefore removes:

- awareness of `claude` typed by hand in a wash terminal;
- awareness of any agent inside an ssh session, including over wash-remote,
  which T0 could never see anyway but T1 could (the hook fires wherever the
  agent runs, and `/dev/tty` carried it home).

Accepted deliberately: the launcher makes wash-started sessions the norm,
and a session wash started is one it can also resume, roster, approve for
and render in three places. The cheapest possible partial reversal, if the
loss bites, is **T0 alone** — a foreground-comm check that puts a muted dot
on a tab and nothing else, ~30 lines, no hooks and no install. Recorded here
so that decision stays deliberate rather than nostalgic.

## 12b. Protocol risk — v2 is drafted, and is not a superset

**v1 is the current stable version** and what `internal/acp` implements.
A v2 schema exists upstream and restructures precisely the message this
design leans on:

| | v1 (implemented) | v2 (drafted) |
|---|---|---|
| permission request | `toolCall` + `options[{optionId,name,kind}]` | `title` / `description` / `subject` + `options[{id,label}]` |
| option kinds | `allow_once`, `allow_always`, `reject_once`, `reject_always` | *(gone)* |
| outcome | `selected` / `cancelled` | `accept` / `decline` / `cancel` |
| `ToolCall` | `toolCallId,title,kind,status,content,locations,rawInput` | `{id, toolUse}` |
| `sessionUpdate` | `agent_message_chunk`, `tool_call_update`, `plan` | `agent_message`, `message_chunk`, `state_update`, … |

So §5's claim that ACP hands us a **durable-allow affordance for free** is
a *v1* claim. Under v2 the "Always allow \<rule\>" button may go back to
being wash's own derivation through `agentpolicy.SuggestRule` — which is
survivable, because that code exists and is what the terminal tier uses
today.

Mitigation is structural, not hopeful: the version is negotiated in
`initialize`, `Client.Initialize` **hard-errors** on any version it does
not speak rather than proceeding half-wrong, and `types.go` holds exactly
one version's shapes. v2 becomes a sibling file and a switch on the
negotiated number.

**Resolved 2026-08-04: the v1 types are now observed, not transcribed.**
`internal/acp` completed a full handshake, `session/new` and a prompt turn
against two independent adapters (claude-agent-acp 0.64.2, codex-acp 1.1.9)
with zero undecoded notifications. Confirmed on the wire: newline-delimited
framing, `initialize` in both directions, `session/new` → `sessionId`,
`session/prompt` → `stopReason`, and the `session/update` discriminator.

Three things real traffic taught that the spec pages did not:

- `codex app-server` omits `"jsonrpc"` from its responses entirely. The
  decoder classifies on "has id, no method" rather than validating the
  version field, so that leniency is load-bearing rather than sloppy.
- Adapters emit update variants beyond the documented set —
  `available_commands_update`, `usage_update`, `session_info_update`. All
  decoded; none consumed. They are named in `types.go` so that ignoring
  one is a decision rather than a surprise.
- `authMethods` advertises what is *available*, not what is *required*
  (§6).

Still unverified because no turn has needed one: **`session/request_permission`
has not been seen on a real wire.** It is the payload v2 restructures and
the one this design leans on hardest, so it stays the top conformance
target — a prompt that provokes a tool call is the next thing to run.

## 13. Testing

The pivot makes testing strictly easier, which is corroboration. Today's
agent e2e needs a shell script printf-ing OSC sequences, an executable named
`claude` to fool the comm poll, and a `PATH` shadow so the real CLI does not
sit at its trust prompt (AGENT_TERM §9.1, §9.7). Under ACP the stand-in is
**a fake adapter binary speaking JSON-RPC** — deterministic, scriptable, and
exercising the exact production path.

- Unit: `internal/acp` framing and dispatch; `session/update` → wire-state
  mapping (table-driven); the source-agnostic reply route; rule derivation
  from a typed `toolCall`.
- Component: `<AgentSession>` rendered by all three hosts (`.ctest.tsx`).
- e2e: fake adapter requests permission → Playwright asserts the sidebar row
  *and* the inline row, router log asserts the answer reaching the adapter.
- `make test-race` on the session registry — copy-on-write snapshots, the
  StateService rule.

## 14. Non-goals

- Keeping a hook-based fallback. Decided against; §12 records the cost.
- A kanban / parallel-session dashboard. The desktop is the switcher.
- Merging with `docs/AGENT.md` (the desktop-operating AI) — same word,
  different program.
- Prompt library, context feed — unchanged from AGENT_TERM §11.
