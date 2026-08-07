# Agent tabs in wash-edit

Launch an agent from the editor the way you launch a terminal: a tab in the
bottom pane, working in the folder already open, with its tool rows wired to
the buffers beside it.

Status: **M0–M3 shipped.** You can launch an agent in wash-edit the way you
launch a terminal. What is left is persistence (§7.2) and adopting an
agent's own ACP terminals as tabs — the same tab-kind work, tracked in
`docs/AGENT_TERMINAL.md` M4.

## 1. Why the bottom pane, and not the editor pane

The justification for hosting an agent inside the editor at all is
`AgentSession`'s `onOpenTool` — "the host decides what that opens". In the
standalone Agent app a tool row naming a file is a dead end; in the editor it
opens that file in the buffer next to the transcript. That only works if the
transcript and the buffer are visible **at the same time**, which rules out
the editor pane: an agent tab there would be a buffer tab, and opening the
file would swap the transcript out. The one feature that justifies the work
would break itself.

The same argument runs the other way — while an agent works you want to watch
it *and* see what it is changing. Co-visibility is the point.

The bottom pane already has everything: an ordered tab list, activation, the
`edit_pct` splitter, persistence. An agent tab is a new `kind`, not new
furniture. And putting agents in the same strip as terminals says something
true: both are a process working on your behalf that you watch and interrupt.

**Known cost:** a transcript is a tall, narrow reading surface and the bottom
pane is short and wide. Mitigations, in order of cheapness: raise `edit_pct`
when an agent tab is activated; keep "open in the Agent app" as the escape for
long reading (the session lives in agentd, so it survives being viewed
elsewhere); and, if it still grates, promote the pane to a right-hand column —
the shape IDE assistants converge on. `AgentSession` is a self-contained
component taking callbacks, so that last move relocates one JSX block rather
than a rewrite. Do not do it first.

## 2. What already exists (do not rebuild any of it)

| Piece | Where | Note |
| --- | --- | --- |
| Session lifecycle, transcript, roster, ACP | `apps/agentd/be` | agentd owns all of it |
| Transcript + composer UI | `web/lib/src/agent-session.tsx` | exported from `@wash/ui`, host-agnostic |
| A worked example of a host | `apps/ai/be/app.go` | "a thin host" — 3 verbs out, 3 in |
| Tabbed pane, splitter, persistence | `apps/edit/fe/src/main.tsx` | `termTabs`, `activeTermID`, `edit_pct` |
| Fake ACP adapter for tests | `e2e/fixtures/acp-fake` | built as `out/e2e/codex-acp` |

The protocol a host speaks to agentd:

```
host → agentd   agent_start {agent, cwd, prompt?, req_id?}
                transcript_subscribe {key}
                agent_prompt {key, text}
                agent_answer {key, id, decision, rule?}
                agent_cancel / agent_set_mode / agent_set_config {key, …}
agentd → host   agent_started {req_id, key, session_id} | {req_id, error}
                transcript_snapshot {key, events}
                transcript_event {key, event}
                state (roster)
```

## 3. M0 — correlation id (landed)

`agent_start` carried no request id, and `agent_started` replied with only
`key`/`session_id` — on failure, with neither. That is sound for a host with
**one session per process**: wash-ai is `InstancingMulti` with a package-level
`var session struct{key, agent, title string}`, so a reply can only be about
the one thing it asked for.

An editor hosting several tabs breaks both halves: two concurrent starts are
indistinguishable, and a failed start cannot be attributed to the tab that
asked. So `startReq` gained an opaque `ReqID`, echoed on both the success and
the error reply. agentd never interprets it.

## 4. M1 — a keyed client, shared by both hosts — **DONE**

wash-ai's relay is the second copy waiting to happen. Factor it into
`internal/agentclient`:

```go
cl := agentclient.New(conn)          // wraps SendAppMsgTo(agentd)
id := cl.Start(agent, cwd, prompt)   // returns the req_id it minted
cl.Prompt(key, text); cl.Answer(key, askID, decision, rule)
cl.Cancel(key); cl.SetMode(key, id); cl.SetConfig(key, id, val)
cl.Handle(m) bool                    // routes agentd→host by key, else false
```

The difference from ai's code is that everything is **keyed**: `Handle` fans
`transcript_snapshot` / `transcript_event` to a per-session callback rather
than comparing against one package-level key. wash-ai then uses it with
exactly one entry, and stops being a special case.

Verify: unit tests over `Handle` (right session gets the event; an event for
an unknown key is dropped, not broadcast).

## 5. M2 — edit's BE — **DONE**

`apps/edit/be` becomes the second host. New FE verbs mirror ai's, each
carrying `key` except `agent_start`, which carries `req_id`. `cwd` defaults to
the editor's current root — the reason this is nicer in the editor than in the
Agent app is that nobody has to pick a folder.

## 6. M3 — edit's FE — **DONE**

`termTabs` → `paneTabs`, each `{id, kind: 'pty' | 'agent'}`; a pty tab keeps
its channel id, an agent tab its agentd session key. The pane body switches on
kind: `<Terminal/>` or `<AgentSession/>`. The `+` button becomes a small menu —
*New terminal* / *New agent ▸* (adapters come from the roster subscription;
`apps/ai/be/app.go:162` shows the probe).

`onOpenTool` opens the referenced file in the editor. That is the payoff.

## 7. Decisions still open

1. **Closing a tab.** Closing an Agent *window* asks what to do with the agent
   (keep it running / end it) — `e2e/tests/agent-session.spec.ts:178`. An agent
   tab needs the same question, and it now composes with the terminal close
   confirmation (`docs/TERM_LAYOUT.md`): one dialog listing shells *and*
   sessions, or two in sequence?
2. **Persistence.** `main.tsx:161` says terminal tabs are not persisted because
   PTYs die with the editor. Agent sessions do not — agentd outlives the window,
   which is why Resume exists. So agent tabs *should* be restored on reload, and
   that comment stops being true for half the strip.
3. **Keyboard ownership.** The composer is a text area; it will eat `Ctrl+S`,
   `Ctrl+P` and friends while focused. The terminal pane has this already, but
   a composer invites long typing, so the editor's shortcuts need an explicit
   escape.
4. **Roster identity — ANSWERED.** Free, and for a structural reason: agentd
   owns the session, so an editor-hosted one is on the roster exactly like a
   window-hosted one. Hosting was never ownership.

5. **Where the ✦ button cannot live.** The strip is inside the pane, so a
   button there cannot open the pane — you would have had to start a shell
   before you could start an agent. Starting a session is in the Terminal
   menu for that reason; the strip button is the shortcut once the pane is
   already up. Found by writing the spec, not by using it.
